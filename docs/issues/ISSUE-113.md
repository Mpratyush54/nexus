# ISSUE-113 — Steering Run History + Prompt Queue Bounds, Signal Lock Discipline

- **Issue:** #113 — audit: Steering run history and prompt queue unbounded memory leak (`internal/steering/steering.go`, type `InterruptManager`)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/steering/steering.go` + `internal/steering/steering_test.go` + `docs/`. No other packages touched (no git operations per task rules).

## Claim verification (against `InterruptManager` on master)

| Audit claim | Verdict |
|---|---|
| 1. Completed runs + event histories grow without eviction | **Confirmed.** `Unregister` marked runs `finished` but never deleted them (`runs` map grew forever); `history` appended unbounded per run. |
| 2. Prompt queue unbounded + O(n) pop | **Confirmed.** `Steer` appended with no depth check; `TakeNextPrompt` did `append([]SteerPrompt(nil), queue[1:]...)` — O(n) copy per pop (no backing-array leak, but O(n) time). |
| 3. `RequestInterrupt` calls `bridge.Signal()` under `Manager.mu` | **Confirmed.** `Signal` ran under `defer m.mu.Unlock()`; a slow or re-entrant bridge could stall/deadlock the subsystem. It was the only external call under lock (`cancel()`/`close(done)` are stdlib, non-re-entrant — kept under lock). |

## What was built

| File | Contents |
|---|---|
| `internal/steering/steering.go` | Bounds (`MaxCompletedRuns=128`, `MaxEventsPerRun=256`, `MaxQueueDepth=32`) + `ErrQueueFull`; finished-run FIFO with oldest-finished-first eviction; history truncation (newest kept); queue-full rejection in `Steer`; O(1) `TakeNextPrompt` via head index + compaction + drain release; `RequestInterrupt` reserve → unlock → `Signal` → commit/rollback |
| `internal/steering/steering_test.go` | 6 new tests (eviction, history cap, queue-full, pop-all stability, re-entrant-signal no-deadlock, signal-failure rollback) |
| `docs/decisions/ADR-113-steering-bounds.md` | Rationale for every policy choice |
| `docs/issues/ISSUE-113.md` | This file |

## Decisions (see ADR-113 for rationale)

1. **Eviction:** completion-order FIFO (= LRU where recency = finish time), live runs never evicted, evicted IDs re-registerable; history drops oldest, keeps newest.
2. **Queue:** rejection with `ErrQueueFull` (no overflow convention existed; `Steer` has no production callers depending on silent drops; `GateBeforeToolCall` only pops).
3. **Lock:** reservation under lock serializes concurrent interrupts; signal-failure rolls back to running; run-vanished-mid-signal returns `ErrNoRun`.

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/steering/` → exit 0
- `go test -count=1 -v ./internal/steering/` → 9/9 green (3 pre-existing + 6 new)
- `go test -race` → NOT runnable here: toolchain gcc lacks 64-bit (`cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`); the re-entrancy test is `-race`-friendly for CI
- `go test ./internal/daemon/` (steering consumer) → ok; `./internal/server/` has one failure in `TestBranchMergeWritesProposedAndConflicts`, which has zero steering references — pre-existing, unrelated
