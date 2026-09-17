# ADR-016 — Materializer Push-Model File Generation

- **ADR ID:** ADR-016-materializer-push-model
- **Date:** 2026-09-17
- **Author:** issue-#16 agent
- **Issue:** #16 Materializer (`internal/materializer/materializer.go`)
- **Status:** Accepted

## Context

Phase 4 (plan §§4.1–4.2) needs the same confirmed memory to serve two
consumer classes: live MCP agents (Claude, OpenCode) pull via
`memory_search`, while file-based agents (Copilot, Cursor, Windsurf)
read static instruction files (`.github/copilot-instructions.md`,
`.cursorrules`, `.windsurfrules`). The materializer bridges the gap:
subscribe to `MEMORY_CONFIRMED` / `MEMORY_SUPERSEDED`, debounce 5s,
render within each agent's `context_budget`, and write into a managed
delimited section that preserves user content outside it.

Constraints colliding here:

1. **Parallel ownership** — only `internal/materializer/` + `docs/` may
   be touched. `internal/store/agents.go` does not exist yet (issue #15
   owns the agents table) and the store event bus (`events.go`,
   `Subscribe`) belongs to issue #9, so the materializer cannot import
   either — it must define its own `Event`/`EventHandler`,
   `Memory`/`MemorySource`, `Target`, and `FileWriter` seams.
2. **Daemon sandbox** (issue #3, `fileops.go`) is the only correct file
   writer (lexical `Clean` + `HasPrefix` confinement, secret-pattern
   refusal), but it lives in another package — the materializer must
   write through an injected `FileWriter` interface the daemon fills in
   later, never `os.WriteFile` directly.
3. **Acceptance is behavioural and timing-sensitive** (regen within 5s
   of quiet; user notes preserved; budget truncation; superseded
   removal), so debounce and rendering must be unit-testable without
   sleeping, without Postgres, and without the filesystem.

## Options Considered

1. **Import store event bus + memory rows directly.**
   Pros: no duplicated types. Cons: violates the ownership constraint;
   couples a pure renderer to pgx/Postgres; untestable without a live
   DB — rejected.
2. **(Chosen) Dependency-free core with boundary-mapped interfaces +
   fake-clock debounce + pure render/merge functions.**
   `Event`/`EventHandler`, `MemorySource`, `Target`, `FileReader` /
   `FileWriter` / `FileStore`, `Clock` (injectable `Now`/`After`);
   `Materializer` (per-project dirty map, `Flush`/`Regenerate`/`Run`);
   pure `RenderMemories` (whole-item budget fill) and
   `MergeManaged`/`SplitManaged`/`WrapManaged` (delimiter splice).
3. **Real-timer debounce (`time.AfterFunc` per event) with sleep-based tests.**
   Pros: production-familiar. Cons: tests sleep 5s+ per case (slow,
   flaky on loaded CI); timer lifecycle (stop/reset races) untestable
   deterministically — rejected in favour of the `pending[project] =
   now` + `Flush(now)` pattern proven by the watcher (`watcher.go`).

## Decision

- `internal/materializer/materializer.go`: event constants
  (`MEMORY_CONFIRMED` / `MEMORY_SUPERSEDED`, values identical to the
  store registry but locally declared); `EventHandler` (`HandleEvent`
  marks project dirty, ignores all other types and blank projects);
  `Target` (project-scoped `OutputPath` + `ContextBudget` +
  `Format`, mirroring the plan §4.1 agents row without importing it);
  `MemorySource.ListMemories(ctx, projectID)` (confirmed-only
  contract); `FileWriter.WriteFile` (+ `FileReader.ReadFile`) seam;
  `Clock` (`Now` + `After`, `SystemClock` production, fake in tests);
  `Materializer` (`New` with nil-clock → system / non-positive
  debounce → 5s validation, `AddTarget`/`RemoveTarget` idempotent,
  `DueProjects`, `Flush` clearing pending even on source failure,
  `Regenerate` re-listing the whole project, `Run` ticker at
  debounce/3 so worst-case regen lands inside ~5s of quiet).
- Rendering: `RenderMemories` fills the budget at whole-item
  granularity (oversized items skipped and counted dropped, smaller
  later items may still fit — output is always a prefix-complete list,
  never mid-line truncation); `markdown` vs `text` line templates.
- Managed section: exact plan delimiters
  `<!-- BEGIN CENTRAL MEMORY — DO NOT EDIT -->` /
  `<!-- END CENTRAL MEMORY -->`; `MergeManaged` replaces the block
  between them byte-preserving outside content, appends a wrapped block
  when absent, and treats half-sections (BEGIN without END) as user
  content (preserved, new block appended — never deleting hand-written
  text). `SplitManaged`/`WrapManaged` are pure and round-trip tested.
- Superseded removal falls out of re-listing: the source no longer
  returns the item, so it vanishes from the managed block with no
  targeted delete path.

## Why (Rationale)

- **Seam composition is untouched:** the package imports only stdlib
  (`context`, `errors`, `sort`, `strings`, `sync`, `time`) — no
  `internal/store`, no `internal/daemon` (verified: `go build ./...`
  exit 0, `gofmt` clean). The daemon injects its sandboxed writer and
  the server maps store events at the boundary (their call).
- **Debounce is proven without sleeping:** `TestDebounceCoalescing`
  advances a fake clock (3-event burst → no write at 4s quiet, exactly
  1 write at 5s quiet); `flushDue`-style `Flush(now)` mirrors the
  `watcher.go` timing-free hook. `Run`'s tick (debounce/3) bounds
  production regen to ~debounce + tick, inside the 5s acceptance.
- **Acceptance maps 1:1 to tests:** delimiter preserve
  (`TestDelimiterPreserve` + half-section case), budget truncation at
  item granularity (`TestBudgetTruncation`), superseded removal
  (`TestSupersededRemoval`), coalescing (`TestDebounceCoalescing`),
  event filtering (`TestHandleEventFiltersTypes`).
- **Evidence:** `go build ./...` 0, `go vet ./internal/materializer/`
  0, `go test -count=1 ./internal/materializer/` **8 PASS, 0 FAIL**
  (output in ISSUE-16.md).

## Consequences

- #15 (agents registry) should adopt `Target`'s shape
  (`output_file`, `output_format`, `context_budget`) or provide the
  store→`Target` mapper; the materializer takes targets as config and
  needs no change either way.
- Daemon follow-up: inject `WriteFileSandboxed`-backed `FileStore` and
  wire store `Subscribe` → `HandleEvent` adapter (project filter per
  workspace); file watcher must treat edits outside the managed
  section as `INSTRUCTION_FILE_CHANGED` input (plan §4.2).
- No new dependencies (stdlib only, per ADR-000-go-deps).

## Alternatives Rejected

- Store-bus import (option 1): ownership violation + DB coupling for a
  pure renderer.
- Real-timer debounce with sleeping tests (option 3): slow/flaky;
  reset-race untestable — the pending-timestamp + `Flush(now)` seam
  gives deterministic coverage of the same behaviour.
