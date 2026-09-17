# ISSUE-22 — Live Agent Steering, Interruption & Intervention

- **Issue:** #22 — Live Agent Steering, Interruption & Intervention
  (`internal/steer/`, plan Phase 3 WS protocol §3.2 extension)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** ParthKhandelwal537
- **Scope constraint:** ONLY `internal/steer/` (new package: `steer.go` +
  `steer_test.go`) + `docs/`. Did NOT touch `internal/server/ws.go`,
  `internal/daemon/*`, `internal/mcp/*`, `internal/store/*`, or any other
  package — integration rides on two narrow locally-defined seams
  (`Emitter`, `RunnerBridge`; wiring documented in ADR-022). Read
  `implementation-plan.md` Phase 3 WS protocol (§3.2),
  `internal/server/ws.go`, `internal/daemon/*.go` first, per the issue.
- **Plan refs:** `implementation-plan.md` §3.2 (WS protocol — extended with
  four steering events, same precedent as #13's `error` frame), §3.4
  (presence/liveness context), §§1.3–2.1 (daemon tool interception and the
  event-type registry the steering events will join).

## What was built

| File | Contents |
|---|---|
| `internal/steer/steer.go` | Steering events (`AGENT_INTERRUPT_REQUESTED` / `AGENT_STEER_PROMPT` / `AGENT_PAUSED` / `AGENT_RESUMED`); `Running → InterruptRequested → Paused → Running` state machine (`StartRun` / `RequestInterrupt` / `AcknowledgePaused` / `Resume` / `EndRun` / `State`); single-pending priority steer queue (`EnqueueSteer` / `BeforeToolCall` gate / `Pending`, 8KB prompt cap); single-winner spectator locks (`ErrAlreadyInterrupted` / `ErrSteerPending`); context cancel propagation (run context cancelled on interrupt); local `Emitter` + `RunnerBridge` seams (`EmitFunc` / `BridgeFunc` adapters, both nil-safe) |
| `internal/steer/steer_test.go` | 9 tests, all DB-free, stdlib only (matrix below) |
| `docs/decisions/ADR-022-live-agent-steering-interruption.md` | Why-mandatory ADR (seam design, lock choice, protocol-extension justification, wiring map for store/server/daemon/mcp owners, test evidence) |
| `docs/issues/ISSUE-22.md` | This file |

## Decisions (see ADR-022 for rationale)

1. **New package, zero edits elsewhere:** `git status` shows no modified
   tracked files — `ws.go`, `daemon/*`, `mcp/*`, `store/*` byte-identical.
2. **Protocol extension, not plan edit:** the four steering events ride the
   existing generic `{"type":"event"}` WS frame once bridged (same pattern
   as #13's `error` frame and action envelopes).
3. **Single-winner locks:** one mutex per `Controller`; losers get explicit
   errors (`ErrAlreadyInterrupted`, `ErrSteerPending`), never silent drops.
4. **Priority injection via runner gate:** `BeforeToolCall` hands the
   pending steer over before the next tool call, consumed exactly once;
   `Paused=true` holds tool calls while not `Running`.
5. **Cancel = context:** `StartRun` derives a run context; `RequestInterrupt`
   cancels it (the in-process SIGINT). Bridge errors are reported but never
   rolled back — a context cannot be uncancelled.
6. **Resume never drops redirects:** an undrained steer survives `Resume`
   and is delivered at the next gate.

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/steer/` → exit 0
- `go test -count=1 -v ./internal/steer/` → **PASS (9/9, <1s)**:
  `TestInterruptToPaused` (interrupt→paused, run-context fired, bridge got
  s1/r1, event order exact), `TestSteerQueuePriority` (redirect at next
  gate, single delivery, resume continues, 4-event order exact),
  `TestResumeGuards` (resume only from Paused — handshake unskippable),
  `TestConcurrentSteerSingleWinner` (16 racers → 1 winner + 15
  `ErrSteerPending`), `TestConcurrentInterruptSingleWinner` (8 racers → 1
  winner + 7 `ErrAlreadyInterrupted`), `TestCancelPropagation` (interrupt
  cancels bridge-less; parent cancel flows through),
  `TestSteerWhileRunning` (steer against a running agent held at next
  gate), `TestNoActiveRunErrors` (unknown-session behaviour),
  `TestValidationEdges` (duplicate run, blank prompt, early ack, 8KB
  truncation marker, idempotent `EndRun`)
- `gofmt` clean; `-race` not runnable here (MinGW `cc1.exe` lacks 64-bit
  cgo — environmental, same as ADR-013; race tests await CI)
- Full `go test ./...`: all packages green **except**
  `internal/governance`, whose test file is another agent's untracked work
  on this shared branch and fails to compile there — pre-existing and
  unrelated to this issue

## Follow-ups (not this issue)

- `store` owner: register the four event constants in `ValidEventTypes`
  (strings proposed in `steer.go`).
- `server` owner: bridge `Emitter` → `EventStore.AppendEvent` → existing
  `Hub.PublishEvent` as `{"type":"event"}` frames.
- `daemon` owner: implement `RunnerBridge` (SIGINT to agent child +
  cancellation token over MCP) and call `BeforeToolCall` before every tool
  dispatch.
- `mcp` owner (optional): `agent_interrupt` / `agent_steer` tools over the
  controller (catalog stays at 8 until then).
- CI: `go test -race ./internal/steer/` + live-DB replay once the registry
  lands; fix the pre-existing `internal/governance` test compile error.
