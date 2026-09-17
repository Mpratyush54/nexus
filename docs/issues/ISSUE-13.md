# ISSUE-13 — WebSocket Hub (Fan-out + Presence + Backpressure)

- **Issue:** #13 — WebSocket hub (`internal/server/ws.go`, plan §§3.2, 3.4)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** subagent
- **Scope constraint:** ONLY `internal/server/ws.go` (+ `ws_test.go`),
  `docs/`. Did NOT touch `internal/server/server.go`,
  `internal/server/routes.go`, `internal/store/*`, or any other package —
  extension is additive at runtime (same-package `init` + `EnableWS`, see
  below). Read `implementation-plan.md` §3.2, `internal/server/server.go` +
  `routes.go`, `internal/store/events.go` first, per the issue.
- **Plan refs:** `implementation-plan.md` §3.2 (WebSocket protocol), §3.4
  (presence), §2.1 (event-type registry reused for `action` validation).

## What was built

| File | Contents |
|---|---|
| `internal/server/ws.go` | §3.2 protocol types + `{"type":"error"}` extension; transport-agnostic `Hub` (`Register`/`Unregister`/`Broadcast`/`PublishEvent`/`PublishPresence`/`PublishMemoryUpdate`/`PublishEpisodeUpdate`/`NoteHeartbeat`/`ReapStale`/`PresenceSnapshot`/`HandleClientMessage`); per-client buffered `Send` + drop/slow-client eviction; presence state machine (online/typing/idle/offline, 5m idle, 30s typing decay, 90s offline via `store.OfflineAfter`); `WSConn` (7-method) + `Upgrader` seams with stdlib-only `hijackUpgrader` (handshake + RFC 6455 text/ping/pong/close, masked-client enforcement); JWT-authed `handleWS` (header-first, `?token=` fallback) + `EnableWS()` + per-`Server` hubs |
| `internal/server/ws_test.go` | 8 tests, all DB-free: fake channel-backed `WSConn`, stub `Upgrader`, controllable hub clock (full matrix below) |
| `docs/decisions/ADR-013-websocket-hub-stdlib-upgrade.md` | Why-mandatory ADR (no-new-dep justification, seam design, validity of the zero-edit extension, test evidence) |
| `docs/issues/ISSUE-13.md` | This file |

## Decisions (see ADR-013 for rationale)

1. **No nhooyr** (absent from `go.mod`, per the issue's ordering):
   stdlib `Hijacker` upgrade + ~150 lines of framing behind the
   `Upgrader → WSConn` seam, swappable later with zero hub changes.
2. **Routing:** project must match AND (message project-wide OR client is
   a project-level watcher OR sessions match) — session members see their
   session + project-wide; dashboards (empty session) see everything.
3. **Presence shares definitions**: offline threshold is
   `store.OfflineAfter` (not a copy); `action` event types validated with
   `store.IsValidEventType` (no duplicate registry).
4. **Backpressure:** non-blocking broadcast; full buffer ⇒ drop + counter;
   eviction past 16 drops so one stalled TCP conn never stalls fan-out.
5. **Zero-file-edit extension:** `init()` exempts `GET /ws` from `withAuth`
   (handler authenticates header + query itself); `EnableWS()` registers
   the route on the same-package mux (idempotent); hubs live in a
   `sync.Map` keyed by `*Server`. `server.go`/`routes.go` byte-identical.
6. **`{"type":"error"}` reply frame** (documented extension): the plan has
   no error shape; rejections would otherwise be silent.

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/server/` → exit 0
- `go test -count=1 ./internal/server/ -run TestHub|TestWS|TestPresence` →
  **PASS (8/8, ~1s)**: `TestHubFanoutSubSecond` (2 receivers, <1s,
  cross-project silence), `TestHubSessionIsolation` (session vs
  project-wide routing), `TestHubPublishMemoryEpisode` (both update types
  + vocabulary rejection), `TestHubSlowClientDrop` (bounded outbox,
  drops ≥ budget, eviction, survivor unaffected),
  `TestPresenceTransitions` (online→typing→decay→idle→offline, peer
  announcements + snapshots), `TestPresenceHeartbeatRevival`,
  `TestWSClientMessageValidation` (10 rejection cases + happy paths),
  `TestWSUpgradeAuth` (3× 401, query-token + header lifecycles,
  register→online→unregister→offline)
- Full `go test -count=1 ./internal/server/` → **PASS** (all pre-existing
  issue-#8 tests unmodified and green — no regressions)
- `gofmt` clean; `-race` not runnable here (MinGW `cc1.exe` lacks 64-bit
  cgo — environmental, unrelated)

## Follow-ups (not this issue)

- Production: call `srv.EnableWS()` once after `New` (no server
  entrypoint in `main.go` yet).
- Bridge `store.Subscribe` (LISTEN/NOTIFY) → `Hub.PublishEvent`: one
  subscription per node, hydrate via `GetEventByID`, replay via
  `ListEvents` on reconnect (per ADR-009's wake-up contract).
- Persist `action` frames through `EventStore.AppendEvent` (hub currently
  fans out only; returns receivers so the handler can 500 on store error
  later without protocol changes).
- `Hub.NoteHeartbeat` from the REST heartbeat handler (wire path already
  feeds it via pong).
- Edge: `wss://` termination, `Origin` checking, per-IP rate limits.
- CI: `go test -race` + a live-socket handshake test against the real
  `hijackUpgrader` (unit tests cover the hub via stub; framing is
  reviewed but not socket-tested here).
