# ISSUE-42 — Steer Wiring (Store Registry + Server Emitter + Daemon Bridge)

- **Issue:** #42 — Steer wiring (follow-up to #22: `Emitter`/`RunnerBridge`
  interface-only, `AGENT_*` types rejected by store validation,
  `BeforeToolCall` never called)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/store/events.go` (additive registry)
  + `internal/store/events_test.go` (registry test update) + NEW
  `internal/server/steer_emit.go` + `steer_emit_test.go` + NEW
  `internal/daemon/steerbridge.go` + `docs/`. Did NOT touch
  `internal/steer/steer.go` (frozen), `internal/daemon/daemon.go`
  (ownership — hook documented in ADR-042 for the daemon owner), or any
  other package/file. Read `internal/steer/steer.go` (Emitter/RunnerBridge
  seams), `internal/store/events.go` (`ValidEventTypes`),
  `internal/server/ws.go` (`PublishEvent`), `internal/daemon/daemon.go`
  (tool dispatch path, read-only) first, per the issue.
- **Plan refs:** `implementation-plan.md` Phase 3 WS protocol §3.2
  (steering rides the generic `{"type":"event"}` frame — no protocol
  change, same precedent as #13's envelopes), §§1.3–2.1 (tool interception
  and the event-type registry the steering events now join).

## What was built

| File | Contents |
|---|---|
| `internal/store/events.go` | Four `EventAgent*` constants (`AGENT_INTERRUPT_REQUESTED` / `AGENT_STEER_PROMPT` / `AGENT_PAUSED` / `AGENT_RESUMED`, canonical strings from `internal/steer`) + four `ValidEventTypes` entries — minimal additive edit, nothing else in the file |
| `internal/store/events_test.go` | Registry test updated additively (23 → 27 types) |
| `internal/server/steer_emit.go` | `SteerEmitter` implementing `steer.Emitter` (compile-time asserted): `EventStore.AppendEvent` persistence + `Hub.PublishEvent` fan-out, envelope fallback on store error, nil-safe, panic-isolated |
| `internal/server/steer_emit_test.go` | 6 tests, all DB-free (fake store + real Hub), incl. controller end-to-end seam proof |
| `internal/daemon/steerbridge.go` | `SteerBridge` implementing `steer.RunnerBridge` (compile-time asserted) over a context-cancel registry (`TrackRun` / `UntrackRun` / `SignalInterrupt`) + `GateBeforeToolCall` one-line hook helper delegating to `Controller.BeforeToolCall` |
| `docs/decisions/ADR-042-steer-wiring.md` | Why-mandatory ADR (seam design, options, one-line hook for the daemon owner, test evidence) |
| `docs/issues/ISSUE-42.md` | This file |

## Decisions (see ADR-042 for rationale)

1. **Registry-only store change:** `event_type` is unconstrained `TEXT`
   in `migrations/002_events.up.sql`, so the Go registry is the only gate
   — no migration needed. WS `action` accepts the four types for free via
   the existing `IsValidEventType` check.
2. **Persist-then-publish with envelope fallback:** the stored row is
   fanned out when persistence succeeds; when the store is nil or errors,
   the envelope still reaches live watchers (continuity over consistency
   for steering signals). Sinks never break transitions (panic-isolated).
3. **Context-cancel bridge, daemon-owned hook site:** `SignalInterrupt`
   fires the registered run cancel (in-process SIGINT; nil bridge = no-op
   success). `daemon.go` untouched — the exact hook line lives in ADR-042
   for the daemon owner (`TrackRun`/`UntrackRun` bracket run boundaries).
4. **No protocol change:** steering frames reuse `Hub.PublishEvent`
   (`{"type":"event"}`), same as #13's action envelopes.

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/steer/ ./internal/server/ ./internal/daemon/ ./internal/store/` → exit 0
- `go test ./internal/steer/ ./internal/server/ ./internal/store/` → green
  (steer 9/9 incl. single-winner races; server incl. 6 new emitter tests;
  store incl. 27-type registry — full output in the report)

## Follow-ups (not this issue)

- `daemon` owner: apply the ADR-042 one-line hook in the tool-dispatch
  path + `TrackRun`/`UntrackRun` at run boundaries; optionally add
  SIGINT-to-child / MCP-cancel beside `cancel()` in `SignalInterrupt`.
- `mcp` owner (optional): `agent_interrupt` / `agent_steer` tools over the
  controller (catalog stays until then).
- CI: `go test -race` on the touched packages + live-DB replay covering
  the four new types.
