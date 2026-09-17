# ADR-022 — Live Agent Steering, Interruption & Intervention (Standalone `internal/steer` Package Behind Narrow Seams)

- **ADR ID:** ADR-022-live-agent-steering-interruption
- **Date:** 2026-09-17
- **Author:** ParthKhandelwal537
- **Issue:** #22 Live Agent Steering, Interruption & Intervention (`internal/steer/`, plan Phase 3 WS protocol §3.2 extension)
- **Status:** Accepted

## Context

Issue #22 needs watcher-driven live control of a running agent: a spectator
presses Interrupt/Redirect, the agent pauses its pending tool call, ingests
the redirect prompt, and continues corrected. Four new events carry the
handshake: `AGENT_INTERRUPT_REQUESTED`, `AGENT_STEER_PROMPT`, `AGENT_PAUSED`,
`AGENT_RESUMED`.

Constraints colliding here:

1. **Strict ownership** — this issue owns ONLY `internal/steer/` (new
   package: `steer.go` + test) plus `docs/`. `internal/server/ws.go`,
   `internal/daemon/*`, and `internal/mcp/*` MUST NOT be edited, yet the
   feature conceptually touches all three (WS fan-out, daemon SIGINT/MCP
   cancel to the active runner, prompt injection before the next tool call).
2. **Plan §3.2 names no steering vocabulary.** The WS protocol covers
   `subscribe`/`action`/`presence` client frames and
   `event`/`presence`/`memory_update`/`episode_update` server frames only.
   The four steering events are therefore a protocol extension that must be
   justified exactly like #13's `{"type":"error"}` frame was.
3. **Conflicting spectators** — N watchers may interrupt/steer at once;
   without a lock two redirects could interleave and the agent would obey a
   mash-up of both.
4. **Zero new dependencies** — `go.mod` holds only `fsnotify`, `jwt/v5`,
   `pgx/v5`, `pgvector-go`. Steering coordination needs nothing beyond
   `sync` + `context`.

## Options Considered

1. **Edit `ws.go` / `daemon` / `mcp` directly** (add hub publish helpers,
   daemon signal routes, an MCP steer tool in this issue).
   Pros: shortest path to end-to-end wiring. Cons: violates the ownership
   rule outright and collides with the parallel owners of those files on
   this shared branch (`feat/wave-4-parth-20-22-24-26`); rejected on
   ownership alone.
2. **(Chosen) Standalone `internal/steer` package behind two narrow local
   seams (`Emitter`, `RunnerBridge`) + a runner-side `BeforeToolCall` gate.**
   The package owns the state machine, the single-winner locks, the
   priority steer queue, and context-based cancel propagation. The daemon
   later implements `RunnerBridge` (SIGINT to the agent child + cancel
   token over MCP, exactly how the harvester/interceptor `EventSink` seam
   deferred the real server client), and the server later bridges `Emitter`
   to `store.AppendEvent` + `Hub` publish. The runner calls
   `BeforeToolCall` before every tool call — no daemon/MCP/server import
   needed anywhere in this package.
   Pros: zero edits outside `internal/steer` + `docs` (verified:
   `git status` shows no modified tracked files); every behaviour
   (interrupt→paused, priority drain, single-winner races, cancel) is a fast
   DB-free unit test. Cons: end-to-end wiring is deferred to follow-ups —
   accepted explicitly, with the wiring map below so no design guesswork
   remains.
3. **Distributed lock (DB advisory lock / LISTEN/NOTIFY round-trip) for the
   spectator race.**
   Pros: works across nodes. Cons: steering races are per-session and
   decided in microseconds — a network round-trip per interrupt adds
   latency to exactly the path that must feel instant, and needs a live
   Postgres in tests. A single in-process mutex per `Controller` settles the
   race deterministically; cross-node arbitration (if ever needed) can key
   off the ordered event log later with no API change.

## Decision

- `internal/steer/steer.go` (only new source file, stdlib only):
  `Running → InterruptRequested → Paused → Running` state machine per
  session run; `RequestInterrupt` (cancels the run context, emits
  `AGENT_INTERRUPT_REQUESTED`, signals the bridge) /
  `AcknowledgePaused` (emits `AGENT_PAUSED`) / `EnqueueSteer` (emits
  `AGENT_STEER_PROMPT`, max one pending) / `BeforeToolCall` gate (hands the
  pending steer over for priority injection, consumed exactly once; reports
  `Paused` while not `Running`) / `Resume` (emits `AGENT_RESUMED`) /
  `StartRun` / `EndRun` / `State` / `Pending`.
- **Single-winner locks:** second concurrent interrupt →
  `ErrAlreadyInterrupted`; second concurrent steer while one is pending →
  `ErrSteerPending`. Losers observe explicit errors, never silent drops.
- **Seams defined locally, never imported:** `Emitter`
  (`Emit(eventType, payload)` — mirrors the daemon `EventSink` signature
  style) and `RunnerBridge`
  (`SignalInterrupt(ctx, sessionID, runID)` — the daemon's SIGINT/MCP-cancel
  hook). Bridge failure after cancellation is reported but never rolled
  back (a context cannot be uncancelled — documented on the method).
- **Wiring map (follow-ups, other owners):**
  1. `store` owner: register the four event constants in `ValidEventTypes`
     (this package's constants are the proposed canonical strings).
  2. `server` owner: bridge `Emitter` → `EventStore.AppendEvent`, then fan
     the stored rows out as `{"type":"event"}` frames via the existing
     `Hub.PublishEvent` (no protocol change needed server-side — steering
     events ride the generic event frame, same as #13's action-envelope
     precedent).
  3. `daemon` owner: implement `RunnerBridge` with SIGINT to the agent
     child process + cancellation token pushed over MCP to the active
     runner; call `BeforeToolCall` in the tool-dispatch path before every
     tool call so a redirect always lands before new tool effects.
  4. `mcp` owner (optional): surface `agent_interrupt` / `agent_steer`
     tools that call into the controller; the catalog stays at 8 tools
     until that issue lands.

## Why (Rationale)

- **The ownership rule forces the seam design, and the seam design is
  sufficient:** every acceptance criterion is provable without touching the
  forbidden files — `TestInterruptToPaused` (watcher interrupts, run
  context fires, runner acks, event order exact),
  `TestSteerQueuePriority` (redirect delivered at the very next gate,
  exactly once, resume continues), `TestConcurrentSteerSingleWinner` (16
  racers → exactly 1 winner), `TestConcurrentInterruptSingleWinner` (8
  racers → exactly 1 winner), `TestCancelPropagation` (interrupt cancels
  the run context bridge-less; parent cancel flows through).
- **Precedent fits:** #13 extended the §3.2 protocol with an `error` frame
  rather than editing the plan, and the daemon's interceptor/harvester
  deferred the real server client behind the `EventSink` seam. This issue
  follows both patterns: protocol extension + deferred wiring behind local
  seams.
- **Evidence:** `go build ./...` 0, `go vet ./internal/steer/` 0,
  `go test -count=1 -v ./internal/steer/` PASS (9/9, <1s), `gofmt` clean.
  `-race` could not run in this env (MinGW `cc1.exe` lacks 64-bit cgo
  support — environmental, same as ADR-013; the race tests are written and
  await CI). Full `./...` suite: all packages green except
  `internal/governance`, whose test file (another agent's untracked work on
  this shared branch) fails to compile — pre-existing, unrelated, flagged
  as a follow-up.

## Consequences

- Production enablement is three small wiring tasks (store registry,
  emitter bridge, daemon bridge + gate call) owned by follow-up issues —
  none requires changing this package's API.
- If cross-node spectator arbitration is ever needed, order the
  `AGENT_INTERRUPT_REQUESTED` / `AGENT_STEER_PROMPT` rows by event id and
  let the lowest id win; the in-process single-winner errors already give
  callers a retry signal, so no API change is required.
- CI must run `go test -race ./internal/steer/` (covers the two race
  tests) plus a live-DB replay once the store registry lands.

## Alternatives Rejected

- Direct edits to `ws.go`/`daemon`/`mcp` (option 1): violates the issue's
  explicit ownership constraint; rejected without technical evaluation.
- DB-backed distributed lock (option 3): network latency on the
  must-feel-instant path plus live-DB test burden, for a race that is
  inherently per-session and in-process today.
