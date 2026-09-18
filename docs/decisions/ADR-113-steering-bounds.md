# ADR-113 — Steering Bounds: Run Eviction, Queue Cap, Signal Lock Discipline

- **ADR ID:** ADR-113-steering-bounds
- **Date:** 2026-09-17
- **Author:** Muse Spark (opencode)
- **Issue:** #113 audit: Steering run history and prompt queue unbounded memory leak
- **Status:** Accepted

## Context

`InterruptManager` (`internal/steering/steering.go`, issue #22) is a
process-local, in-memory registry with no TTL: `Unregister` retained every
finished run forever, per-run event history grew without limit, the prompt
FIFO had no depth cap with an O(n)-per-pop implementation, and
`RequestInterrupt` invoked the external `DaemonBridge.Signal` while holding
the global mutex. All three audit claims verified true against master.

## Options Considered

1. **Do nothing / TTL-based expiry.** Pros: zero code. Cons: leak remains;
   a TTL needs a background sweeper for a package that is currently
   goroutine-free — rejected.
2. **Drop-oldest queue overflow (vs rejection).** Pros: `Steer` never
   errors. Cons: silently discards a spectator correction — the worst
   failure mode for a steering system — and no existing overflow convention
   or production caller requires it; rejected.
3. **(Chosen) Bounded retention + rejection + signal-outside-lock.**
   Finished-run FIFO cap, per-run history cap (drop oldest), queue cap with
   `ErrQueueFull` rejection, O(1) pop via head index, and
   reserve → unlock → signal → commit/rollback in `RequestInterrupt`.

## Decision

- **Completed runs:** `finished` completion-order FIFO, cap
  `MaxCompletedRuns=128`. Eviction is least-recently-finished-first (LRU
  where recency = finish time). Live runs are never evicted; double
  `Unregister` stays idempotent (no FIFO duplicates); re-`Register`ing an
  evicted ID re-arms fresh state. Post-mortem `Events()`/`State()` reads
  keep working within the cap.
- **Event history:** cap `MaxEventsPerRun=256` per run; `emit` compacts in
  place (bounded copy) and clears vacated tail entries so dropped events
  are not retained. Newest events survive — steering diagnostics read the
  tail, never the head.
- **Prompt queue:** cap `MaxQueueDepth=32` pending prompts; over-cap `Steer`
  returns `ErrQueueFull` wrapping a descriptive message (current depth, the
  cap, and the remedy: the agent must `TakeNextPrompt`). The 32-slot
  headroom covers burst steering while keeping worst-case per-run prompt
  memory trivial.
- **O(1) pop:** `run.head` index; pop clears the slot, advances head, and
  compacts once `head>=64 && head*2>=len(queue)`; a drained queue releases
  its backing array (`nil`). `Steer`/`QueueDepth`/queue-len payloads use
  `len-head`, never raw `len`.
- **Lock discipline:** `RequestInterrupt` reserves (`running →`
  `pause_requested` + owner) under `mu`, calls `Signal` unlocked, then
  re-locks to commit (`cancel` + guarded `close(done)` + event) or roll
  back to `running` on signal failure (rollback only if the reservation is
  still current). The reservation serializes concurrent interrupts without
  duplicate signals; a run that vanished mid-signal yields `ErrNoRun`.
  Audit of all other methods: no external calls under lock (`cancel` /
  `close` are stdlib and non-re-entrant), so no further changes.

## Consequences

- Manager memory is now O(`MaxCompletedRuns` × (`MaxEventsPerRun` +
  `MaxQueueDepth`)) worst-case, independent of uptime.
- `Steer` gains a new error (`ErrQueueFull`); the only in-repo consumer
  path (`GateBeforeToolCall`) pops rather than pushes, so nothing else
  changes. WS/daemon owners should surface the error text to the steerer.
- Interrupt latency no longer depends on bridge speed for other runs, and
  a re-entrant bridge cannot deadlock the manager (covered by
  `TestSignalNotInvokedUnderLock`).
- `-race` could not run on this host (gcc lacks 64-bit); CI must run
  `go test -race ./internal/steering/`.
