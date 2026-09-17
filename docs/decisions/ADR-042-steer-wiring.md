# ADR-042 — Steer Wiring: Store Registry + Server Emitter + Daemon Bridge

- **ADR ID:** ADR-042-steer-wiring
- **Date:** 2026-09-17
- **Author:** Muse Spark (opencode)
- **Issue:** #42 Steer wiring (follow-up to #22)
- **Status:** Accepted

## Context

Issue #22 (`internal/steer/`, ADR-022) shipped the steering state machine
behind two narrow locally-defined seams — `Emitter` (event fan-out) and
`RunnerBridge` (active-runner signalling) — plus a runner-side
`BeforeToolCall` gate, with end-to-end wiring explicitly deferred to
follow-ups under strict ownership rules. Three gaps remain, which is this
issue:

1. **Store rejects steering events.** `store.ValidEventTypes` (§2.1
   registry) knows nothing of `AGENT_INTERRUPT_REQUESTED` /
   `AGENT_STEER_PROMPT` / `AGENT_PAUSED` / `AGENT_RESUMED`, so
   `EventStore.AppendEvent` fails them with `unknown event type` — and the
   WS `action` path (`Hub.HandleClientMessage`, which gates on
   `store.IsValidEventType`) rejects them too.
2. **`Emitter` is interface-only.** No production code bridges a steer
   `Controller` to `EventStore.AppendEvent` + `Hub.PublishEvent`, so
   steering transitions are invisible to the event log and live watchers.
3. **`RunnerBridge` is interface-only and `BeforeToolCall` is never
   called.** No daemon code implements out-of-band signalling, and no
   tool-dispatch path consults the gate — a redirect can never land before
   new tool effects.

Constraints: do NOT touch `internal/steer/steer.go` (the #22 package is
frozen); do NOT edit `internal/daemon/daemon.go` (daemon-core ownership —
the hook is documented here for the daemon owner instead); minimal
additive edits only; no new dependencies (`go.mod` unchanged); no DB
migration needed (`migrations/002_events.up.sql` declares `event_type`
as unconstrained `TEXT NOT NULL` — the Go registry is the only gate).

## Options Considered

1. **Edit `steer.go` / `daemon.go` directly** (move the seams, hard-wire
   the gate into `handleCommandRun` in this issue).
   Pros: shortest path. Cons: violates both ownership constraints
   (`steer.go` frozen, `daemon.go` owned elsewhere) and collides with the
   parallel daemon owner; rejected on ownership alone.
2. **(Chosen) Registry + two adapter files, zero edits to owned files.**
   Register the four types in `ValidEventTypes`; add
   `internal/server/steer_emit.go` (Emitter adapter) and
   `internal/daemon/steerbridge.go` (RunnerBridge impl + gate helper);
   document the one-line dispatch hook here for the daemon owner to apply.
   Pros: ownership-clean, each piece unit-testable DB-free, no migration,
   no protocol change (steering rides the generic `{"type":"event"}` frame).
   Cons: the dispatch call-site itself still lands with the daemon owner —
   accepted explicitly, with the exact line below so no design guesswork
   remains.
3. **DB CHECK-constraint migration enumerating event types.**
   Pros: defense in depth at the SQL layer. Cons: the schema intentionally
   leaves `event_type` unconstrained (registry = Go-side fail-fast);
   a migration adds rollout coupling for zero behavioural gain; rejected.

## Decision

- `internal/store/events.go` (additive only, nothing else in the file):
  four `EventAgent*` constants (canonical strings proposed by
  `internal/steer`) + four entries in `ValidEventTypes`. Registry test
  updated additively (23 → 27 types).
- `internal/server/steer_emit.go` (new): `SteerEmitter` implementing
  `steer.Emitter` (compile-time asserted) over a narrow `SteerEventStore`
  seam (`AppendEvent`; `*store.EventStore` satisfies it with no adapter)
  plus `Hub.PublishEvent`. Per emit: copy payload, `AppendEvent`, publish
  the stored row — falling back to the envelope when the store is nil or
  errors (live continuity beats strict consistency for steering signals).
  Nil-receiver/nil-seam safe, panic-isolated (same contract as the daemon
  interceptor `Emit`).
- `internal/daemon/steerbridge.go` (new): `SteerBridge` implementing
  `steer.RunnerBridge` (compile-time asserted) over a context-cancel
  registry (`TrackRun` at dispatch start / `UntrackRun` at end /
  `SignalInterrupt` fires the cancel — the in-process SIGINT; nil bridge =
  no-op success since the Controller cancels the run context itself).
  Plus `GateBeforeToolCall(ctrl, sessionID) (hold, prompt)` — the one-line
  hook helper delegating to `Controller.BeforeToolCall`, nil-controller
  pass-through.
- **One-line hook for the daemon owner** (NOT applied here — `daemon.go`
  untouched): at the top of every tool-dispatch path (`handleCommandRun`
  before `RunCommand`, and the file read/write dispatch equivalents once
  they carry a session ID):

  ```go
  if hold, prompt := GateBeforeToolCall(steerCtrl, sessionID); hold {
      // Run is interrupted/paused: do not issue the tool call; wait for Resume.
  } else if prompt != nil {
      // Inject prompt.Prompt with priority (e.g. as a system message) before the tool call.
  }
  ```

  `TrackRun`/`UntrackRun` bracket the run lifecycle wherever the daemon
  owner starts/ends an agent run; a future SIGINT-to-child / MCP-cancel
  push slots in beside the `cancel()` call inside `SignalInterrupt` with
  no API change.

## Why (Rationale)

- **The ownership rules force the adapter shape, and the shape is
  sufficient:** every acceptance criterion is provable without touching
  frozen/owned files — `TestEventTypeConstantsComplete` (27/27 registered),
  `TestSteerEmitterControllerEndToEnd` (a real `steer.Controller` driving
  `SteerEmitter` persists + fans out `AGENT_INTERRUPT_REQUESTED` /
  `AGENT_PAUSED` with no adapter beyond the constructor), persist-only /
  envelope-fallback / store-error / panic-isolation emitter cases.
- **Precedent fits:** ADR-022 deferred wiring behind local seams exactly
  so follow-ups like this could land adapter-side; #13's `error`-frame and
  action-envelope precedent means steering needs no WS protocol change —
  `PublishEvent` already carries arbitrary event rows, and `IsValidEventType`
  now admits the four types on the `action` path for free.
- **No migration, no deps:** verified `event_type TEXT NOT NULL` (no CHECK)
  in `migrations/002_events.up.sql`; `go.mod` byte-identical.
- **Evidence:** `go build ./...` 0; `go vet` on the four touched packages
  0; `go test ./internal/steer/ ./internal/server/ ./internal/store/`
  green (see ISSUE-42 for output).

## Consequences

- Steering transitions are now durable (event log) and live (WS event
  frames); dashboards can `subscribe` + filter on the four types.
- Production enablement left: daemon owner applies the one-line hook +
  `TrackRun`/`UntrackRun` at run boundaries (snippet above); `mcp` owner
  may surface `agent_interrupt` / `agent_steer` tools (catalog unchanged
  until then).
- If cross-node arbitration is ever needed, order the steering rows by
  event id, lowest id wins (ADR-022) — no API change required.

## Alternatives Rejected

- Direct edits to `steer.go`/`daemon.go` (option 1): violates the issue's
  explicit ownership constraints; rejected without technical evaluation.
- SQL CHECK-constraint migration (option 3): rollout coupling for no
  behavioural gain against an intentionally unconstrained column.
