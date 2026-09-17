# ISSUE-16 — Materializer (Debounced Push-Model File Generation)

- **Issue:** #16 — [Phase 4] Materializer (`internal/materializer/materializer.go`, plan §4.2)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/materializer/` + `docs/`. Did NOT touch
  `internal/store/` (no `agents.go` created — #15 owns it; no event-bus import),
  `internal/daemon/` (no `fileops.go` changes — sandbox injected later via
  `FileWriter`), or any other package. Read `implementation-plan.md` §4.2,
  `internal/daemon/fileops.go`, and `internal/store/events.go` + `memory.go`
  first (concepts only, no edits).
- **Plan refs:** `implementation-plan.md` §§4.1 (agents table:
  `context_budget`, `output_file`, `output_format`) and 4.2 (trigger,
  5s debounce, managed delimiters, watcher interplay).

## What was built

| File | Contents |
|---|---|
| `internal/materializer/materializer.go` | Package core (stdlib only): `EventMemoryConfirmed`/`EventMemorySuperseded` constants (local, value-identical to store registry); `Event` + `EventHandler` (`HandleEvent` marks project dirty, ignores other types/blank projects); `Memory` (render fields, not `store.MemoryItem`); `Target` (plan §4.1 row shape: `ProjectID`/`OutputPath`/`ContextBudget`/`Format`); `MemorySource` (`ListMemories(ctx, projectID)`, confirmed-only contract); `FileReader`/`FileWriter`/`FileStore` seams; `Clock` (`Now`+`After`, `SystemClock` production); `Materializer` (`New` validation, `AddTarget`/`RemoveTarget` idempotent, `DueProjects`, `Flush` clearing pending even on failure, `Regenerate` whole-project re-list, `Run` ticker at debounce/3); pure `RenderMemories` (whole-item budget fill, markdown/text), `WrapManaged`/`SplitManaged`/`MergeManaged` (delimiter splice, half-section-safe append) |
| `internal/materializer/materializer_test.go` | 8 tests with fake clock/source/files (no sleep, no DB, no FS): event-type filtering, debounce coalescing, delimiter preserve + idempotence, append + half-section merge, budget truncation at item granularity + text format, superseded removal, split round-trip, constructor/target validation |
| `docs/decisions/ADR-016-materializer-push-model.md` | Why-mandatory ADR (boundary seams, fake-clock debounce, whole-item budget, half-section preservation) |
| `docs/issues/ISSUE-16.md` | This file |

## Decisions (see ADR-016 for rationale)

1. No `internal/store` import (event bus, memory rows, agents table all
   re-declared as minimal local seams) — ownership constraint + DB-free tests.
2. No `os.WriteFile` — all writes go through the injected `FileWriter`;
   the daemon plugs its sandboxed `WriteFileSandboxed` in later.
3. Debounce as `pending[project] = now` + `Flush(now)` (watcher-style
   timing-free hook) with injectable `Clock`; `Run` ticks at debounce/3
   so regen lands within ~5s of quiet.
4. Budget fill at whole-item granularity (skip-and-count-dropped, never
   mid-line cuts); superseded removal via whole-project re-list (no
   targeted deletes); half-sections treated as user content (append,
   never delete).

## Verification (2026-09-17)

- `go build ./...` → exit 0
- `go vet ./internal/materializer/` → exit 0 (no output)
- `go test -count=1 -v ./internal/materializer/` → **8 PASS, 0 FAIL**:
  `TestHandleEventFiltersTypes`, `TestDebounceCoalescing`,
  `TestDelimiterPreserve`, `TestMergeManagedAppendAndHalfSection`,
  `TestBudgetTruncation`, `TestSupersededRemoval`,
  `TestSplitManagedRoundTrip`, `TestDebounceDefaultAndValidation`
  (`ok central-memory/internal/materializer 0.511s`)
- `gofmt -l internal/materializer/` → clean

Acceptance mapping: regen within 5s of quiet (`TestDebounceCoalescing`:
burst → 0 writes at 4s quiet, exactly 1 at 5s; `Run` tick bounds
production to ~debounce + tick); user notes preserved
(`TestDelimiterPreserve`: before/after content byte-preserved, stale
block replaced, regen idempotent; half-section case appends without
deleting); budget truncation (`TestBudgetTruncation`); superseded
removal (`TestSupersededRemoval`).

## Follow-ups (not this issue)

- #15 agents registry: adopt `Target`'s shape or supply the store→`Target` mapper.
- Daemon: inject sandbox-backed `FileStore`; adapt store `Subscribe`
  notifications → `HandleEvent` (project filter per workspace); watcher
  treats outside-delimiter edits as `INSTRUCTION_FILE_CHANGED` input.
- `Run` lifecycle wiring (daemon startup/shutdown) + `go test -race`
  on a 64-bit-gcc machine.
