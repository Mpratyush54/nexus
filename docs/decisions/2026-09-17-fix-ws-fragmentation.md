# Fix WS Fragmentation Reassembly (issue #13 follow-up)

Date: 2026-09-17
Scope: `internal/server/ws.go` (`wsReadFrame`, `wsReadLoop`) + `internal/server/ws_test.go`
Status: implemented; `go build ./...` and `go test ./internal/server/` green.

## Context

`wsReadFrame` returned only `opcode` (`hdr[0] & 0x0F`), dropping the FIN bit
(`hdr[0] & 0x80`). `wsReadLoop` therefore could not tell a first fragment
(`text`, FIN=0) from a complete message: it dispatched the incomplete Frame 1
to `HandleClientMessage` (JSON parse fails → spurious error reply), then hit
`if op == wsOpContinuation { continue }` on Frame 2, so the completed message
was never dispatched. Any client that fragments (proxies, large payloads,
browsers under pressure) hit this.

## Decision

- `wsReadFrame` now returns `(op byte, fin bool, payload []byte, err error)`,
  surfacing `fin = hdr[0]&0x80 != 0` alongside the opcode.
- `wsReadLoop` reassembles per RFC 6455 §5.4: append fragment payloads to
  `frag` and dispatch (`msg := frag; frag = nil; HandleClientMessage`) only
  when `fin == true`.
- Protocol-error handling (minimal, connection-preserving):
  - Control frames (ping/pong/close) are handled inline and never buffered
    into `frag`, so interleaved pings don't corrupt reassembly. A control
    frame with FIN=0 is a protocol error → loop returns (fails the
    connection) per RFC 6455 §5.5.
  - Continuation with no open fragment (`frag == nil`) → ignored, not
    dispatched.
  - New data (text) frame while `frag != nil` (previous message unfinished)
    → discard the stale buffer and restart from the new frame, so one bad
    peer can't wedge the loop.
  - Reassembled size is capped at `MaxWSMessageBytes` (1 MiB); overflow drops
    the buffer and keeps the loop alive (DoS guard).
- `frag` is forced non-nil on the first fragment even for empty payloads, so
  "started" is distinguishable from "idle" (`frag == nil`).

## Alternatives

- **Fail (close) the connection on any protocol error** (strict RFC 6455
  §7.1.7): more correct on paper, but harsher for transient peer bugs and a
  bigger behavioural change; reset-and-continue keeps one bad sequence from
  killing an otherwise healthy socket.
- **Buffer unconditionally until continuation FIN=1 regardless of opcode**:
  rejected — conflates control frames into the message buffer and breaks
  interleaved ping/pong keepalives.
- **Third-party WS library**: out of scope; transport stays stdlib-only per
  the Phase 3 decision, and the fix is ~30 lines.

## Why

FIN is the only signal that distinguishes "first fragment" from "complete
message". Without it, premature dispatch (JSON fail + error reply) and lost
completions are inevitable. Gating dispatch on `fin == true` with control
frames kept out-of-band is exactly the RFC 6455 §5.4 reassembly contract.

## Consequences

- Fragmented `text(FIN=0) + continuation(FIN=1)` now dispatches once with the
  full payload; unfragmented `text(FIN=1)` path is unchanged.
- Interleaved ping/pong between fragments works (pong echoed, buffer intact).
- Stray continuations and mid-message restarts degrade to drop-and-continue
  instead of spurious JSON errors or wedged buffers.
- Tests added in `ws_test.go` (all via `net.Pipe` through the real read path):
  `TestWSReadFrameFINBit`, `TestWSFragmentedMessageSingleDispatch` (asserts no
  premature dispatch + single complete dispatch), `TestWSUnfragmentedStillWorks`,
  `TestWSInterleavedPingPreservesFrag`, `TestWSStrayContinuationIgnored`.
- Only `internal/server/ws.go` and `internal/server/ws_test.go` touched;
  `routes.go` / `db.go` untouched.
