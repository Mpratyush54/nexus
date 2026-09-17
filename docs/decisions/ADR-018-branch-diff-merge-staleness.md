# ADR-018 — Branch Diff / Merge / Staleness as Pure Overlay

- **ADR ID:** ADR-018-branch-diff-merge-staleness
- **Date:** 2026-09-17
- **Author:** subagent (issue #18)
- **Issue:** #18 Diff/merge/staleness
- **Status:** Accepted

## Context

Plan Phase 5 (§§5.2, 5.4) requires branch diff (key-level added/modified/
deleted), staleness flagging (parent updates after a child's fork point),
and merge (non-conflicting → auto-PROPOSED on target, conflicting →
human-review flag, never silent overwrite). The sibling issue #17 owns
`internal/store/branches.go` (MemoryBranch CRUD + CoW resolution) and the
`005_branches` migration; at implementation time no `branches.go` exists in
this tree, so #18 cannot import #17 types — and must not create them (that
would collide with the parallel agent's landing).

## Options Considered

1. **Wait for / stub #17 types and build diff on top of them** — would
   couple #18 to an unlanded API, risk merge conflicts with the parallel
   agent, and violate the "do NOT edit branches.go" ownership rule.
2. **Pure overlay on a locally declared projection (chosen)** — define
   `MemoryView` in the new file `branch_diff.go`, implement Diff /
   DetectStale / Merge as DB-free pure functions, document the field
   mapping so #17 can bind the projection to real rows later.
3. **SQL-backed diff in the store layer** — rejected: CoW resolution reads
   walk the parent chain (#17's job); diff/merge/staleness are set logic
   over *resolved* states, unit-testable without Postgres (same pattern as
   `IsVisibleToSession` in sessions.go, issue #12).

## Decision

New files only: `internal/store/branch_diff.go` (+ `branch_diff_test.go`).
Pure types operating on `[]MemoryView`:

| `MemoryView` field | Maps to (once #17 lands) | Notes |
|---|---|---|
| `Key` | `memory_items.key` | diff/merge grain |
| `Content` | `memory_items.content` | equality basis — Rev/UpdatedAt never decide equality |
| `Status` | `memory_items.status` | `Merge` forces `StatusProposed` on `ToPropose` |
| `Rev` | `source_event_id` / `events.id` | provenance; fork point = `memory_branches.forked_at_event_id` |
| `UpdatedAt` | `memory_items.updated_at` | carried for review UIs only |

Key semantics:

- `Diff(base, head)`: Added (head-only), Modified (same key, different
  content), Deleted (base-only). Content equality decides; metadata-only
  differences (Rev/Status/timestamps) count as converged.
- `DetectStale(fork, parentNow, childNow)`: flags a child key iff the
  parent's current content differs from the fork baseline (`ParentChanged`,
  including parent-delete). `ChildChanged` distinguishes behind (safe to
  fast-forward) from diverged (route to Merge conflict path). Parent-new
  keys (postdate the fork) and child-local keys are not staleness per plan
  §5.4, which flags on *update* of a forked key.
- `Merge(source, target)`: two-way, additive, never overwrites —
  source-only → `ToPropose` (stamped PROPOSED for the §2.8 confirmation
  flow); same content → `Skipped`; same key + different value →
  `MergeConflict` with target preserved verbatim; target-only keys ignored
  (no delete propagation). Pure: applying inserts and resolving conflicts
  is #17's store job.

## Why (Rationale)

This satisfies every #18 acceptance criterion while respecting the
parallel-ownership constraint: diverged diff, conflict flagging,
staleness, and no-overwrite are all covered DB-free by 12 tests
(`go test ./internal/store/ -run 'TestDiff|TestMerge|TestStale'` → PASS,
full `./internal/store/` suite also PASS, `go build ./...` and `go vet`
clean). The overlay needs no migration and touches no owned file, so #17
can land `branches.go` + `005_branches` independently and bind `MemoryView`
via the mapping table above.

## Consequences

- #17 (or a follow-up) wires the projection: resolved branch states →
  `[]MemoryView`; `ToPropose` → INSERT as PROPOSED on target;
  `StaleItem.IsStale()` → `potentially_stale` flag surface.
- No new dependencies (stdlib only), no migrations, no API changes.

## Alternatives Rejected

SQL-backed diff and #17-type coupling (options 1/3 above): both break
parallel ownership or testability for zero semantic gain — resolved-state
set logic is identical in Go and SQL, but only the Go version is verifiable
here without a live Postgres.
