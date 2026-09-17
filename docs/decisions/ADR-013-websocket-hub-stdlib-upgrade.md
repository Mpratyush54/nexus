# ADR-013 — WebSocket Hub Without New Dependencies (Transport-Agnostic Hub + Stdlib Upgrade Adapter)

- **ADR ID:** ADR-013-websocket-hub-stdlib-upgrade
- **Date:** 2026-09-17
- **Author:** issue-#13 agent
- **Issue:** #13 WebSocket hub (`internal/server/ws.go`, plan §§3.2, 3.4)
- **Status:** Accepted

## Context

Phase 3 (plan §§3.2, 3.4) needs live multiplayer fan-out: Alice's actions
appear for Bob in <1s as `event` / `presence` / `memory_update` /
`episode_update` frames, with presence derived from heartbeats + ping/pong
(`online` / `typing` / `idle` / `offline`) and backpressure so one stalled
client cannot stall everyone.

Constraints colliding here:

1. **Plan §1.9 names `nhooyr.io/websocket`, but it is NOT in `go.mod`.**
   `go.mod` holds only `fsnotify`, `jwt/v5`, `pgx/v5`, `pgvector-go`. The
   issue orders: use nhooyr ONLY if already vendored — otherwise a
   stdlib-compatible approach with the hub transport-agnostic and the
   upgrade adapter defined by us, justified in this ADR.
2. **Parallel ownership** — `server.go` / `routes.go` belong to issue #8;
   this issue owns only `internal/server/ws.go` (+ `ws_test.go`) + `docs/`.
   The hub must compose with the existing JWT middleware, `Store` seam, and
   `store.OfflineAfter` / `store.IsValidEventType` untouched.
3. **Acceptance is behavioural** (sub-second fan-out, presence transitions,
   slow-client drop), so it must be unit-testable with fake conns — no live
   sockets, no real timers.
4. **Browsers cannot set headers on a WebSocket upgrade**, so a
   header-only Bearer check would lock out the dashboard SPA (Phase 3 web/).

## Options Considered

1. **Add `nhooyr.io/websocket` as a new dependency.**
   Pros: battle-tested framing, exactly what plan §1.9 names. Cons: new
   third-party network dependency for a ~7-method surface; the issue
   explicitly prefers avoiding it when absent from `go.mod`; adds supply-
   chain + version-skew surface for zero behavioural gain at our scale
   (frames are small JSON broadcasts, no extensions/compression needed).
2. **(Chosen) Transport-agnostic Hub + 7-method `WSConn` seam + stdlib
   `Hijacker` upgrade adapter.**
   The hub (`Register` / `Unregister` / `Broadcast` / `PublishEvent` (+
   presence/memory/episode variants)) does pure fan-out into per-client
   buffered `Send` channels and never touches the network. The only
   HTTP-touching code is the `Upgrader` interface (`Upgrade → WSConn`),
   default-implemented by `hijackUpgrader`: handshake validation, 101 via
   hijacked socket, minimal RFC 6455 framing (text + ping/pong/close,
   masked-client enforcement) in ~150 lines of stdlib (`bufio`, `sha1`,
   `encoding/binary`). A future nhooyr/gorilla adapter implements the same
   `WSConn` with no hub changes.
   Pros: zero new deps; every behaviour (routing, presence, drops) is a
   synchronous, fake-conn unit test; cross-platform (pure Go, no syscalls).
   Cons: we own framing edge cases (fragmentation, reserved opcodes) —
   mitigated by fail-fast closes (1002/1003/1009) and a strict reader.
3. **SSE / long-poll instead of WebSockets.**
   Pros: no upgrade code at all. Cons: contradicts plan §3.2 (bidirectional
   `subscribe`/`action`/`presence` over one connection); doubles the
   endpoint surface; rejected on plan fidelity alone.

## Decision

- `internal/server/ws.go` (only new source file): §3.2 protocol types
  verbatim + one pragmatic `{"type":"error"}` reply frame (the plan defines
  no error shape; without it a client cannot tell a typo from a drop);
  `Hub` with `Register` / `Unregister` / `Broadcast` / `PublishEvent` /
  `PublishPresence` / `PublishMemoryUpdate` / `PublishEpisodeUpdate` /
  `NoteHeartbeat` / `ReapStale` / `PresenceSnapshot` / `HandleClientMessage`;
  `WSConn` + `Upgrader` seams with the stdlib `hijackUpgrader`; `handleWS`
  (JWT before upgrade, header-first then `?token=` fallback) + `EnableWS`
  route registration + per-`Server` hubs via `sync.Map`.
- Presence: `online` at register/activity, `typing` on explicit messages
  (30s decay), `idle` after 5m silence, `offline` on unregister/reap past
  `store.OfflineAfter` (90s — shared constant, not a copy).
- Backpressure: `SendBufferSize` 64 default, non-blocking broadcast, drop
  counter per client, eviction past 16 drops.
- Zero edits to `server.go` / `routes.go`: the `/ws` auth exemption rides
  on an `init()` entry in the same-package `openPaths` map, the route is
  added by `EnableWS()` on the same-package mux, hubs are keyed per
  `*Server` — additive extension, no existing logic touched.

## Why (Rationale)

- **The dependency question is settled by `go.mod` itself**: nhooyr is
  absent, and the issue orders the stdlib path in exactly that case. The
  `Upgrader → WSConn` seam keeps the nhooyr option open at zero hub cost —
  swapping transports later touches only the adapter, proven by the handler
  tests which already run the full lifecycle against a stub adapter.
- **Seam composes untouched**: JWT via existing `verifyToken` (server
  clock, so expiry stays test-controlled); action `event_type` validated
  with `store.IsValidEventType` (§2.1 registry, no duplicate vocabulary);
  offline threshold reuses `store.OfflineAfter` (single definition shared
  with workspace presence). Verified: no tool ever wrote to `server.go` /
  `routes.go`, and the pre-existing suite passes unmodified.
- **Acceptance is proven DB-free and fast**: `TestHubFanoutSubSecond`
  (2 receivers, cross-project isolation, <1s), `TestHubSessionIsolation`
  (session vs project-wide routing), `TestPresenceTransitions`
  (online→typing→decay→idle→offline with peer announcements),
  `TestPresenceHeartbeatRevival`, `TestHubSlowClientDrop` (bounded outbox,
  drop counting, eviction, survivor unaffected), `TestWSUpgradeAuth`
  (401s + both token paths + register/unregister lifecycle).
- **Evidence:** `go build ./...` 0, `go vet ./internal/server/` 0,
  `go test -count=1 ./internal/server/ -run TestHub|TestWS|TestPresence`
  PASS (8 tests, ~1s), full `./internal/server/` suite PASS (no
  regressions). `-race` could not run in this env (MinGW `cc1.exe` lacks
  64-bit cgo support — environmental, unrelated to the change).

## Consequences

- Production wiring is one line after `New`: `srv.EnableWS()` (documented
  in ISSUE-13; no server entrypoint exists yet in `main.go`).
- Follow-ups (not this issue): bridge `store.Subscribe` LISTEN/NOTIFY →
  `Hub.PublishEvent` (one subscription per node); call
  `Hub.NoteHeartbeat` from the REST heartbeat handler; persist `action`
  frames via `EventStore.AppendEvent` (hub currently fans out only);
  `wss://` termination + origin checking at the edge; `-race` + live-DB
  replay in CI.
- If framing needs ever exceed small JSON broadcasts (compression,
  extensions), adopt nhooyr behind the existing `Upgrader` interface —
  no hub or protocol changes required.

## Alternatives Rejected

- nhooyr dependency (option 1): contradicts the issue's explicit
 ondo-only-if-vendored order for no behavioural gain at this scale.
- SSE/long-poll (option 3): contradicts plan §3.2's bidirectional protocol.
