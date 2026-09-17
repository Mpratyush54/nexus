# ISSUE-18 — Diff / Merge / Staleness (Pure Overlay)

- **Issue:** #18 — Diff/merge/staleness (`branch_diff.go` + tests only)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** subagent
- **Scope constraint:** ONLY `internal/store/branch_diff.go`
  (+ `branch_diff_test.go`), `docs/`. Did NOT touch `branches.go` (owned by
  parallel issue #17 — absent from this tree at implementation time),
  migrations, or any other package.
- **Plan ref:** `implementation-plan.md` §§5.2 (CoW diff/merge), 5.4
  (staleness detection); read Phase 5 first per task brief. Verified
  `internal/store/` contains no `branches.go` before starting.

## What was built

| File | Contents |
|---|---|
| `internal/store/branch_diff.go` | Local `MemoryView` projection (Key/Content/Status/Rev/UpdatedAt + #17 mapping doc); `Diff(base,head) → BranchDiff{Added,Modified,Deleted}` (key-level, content equality, sorted, non-nil); `DetectStale(fork,parentNow,childNow) → []StaleItem` (ParentChanged flag incl. parent-delete, ChildChanged behind-vs-diverged signal, IsStale/Diverged predicates); `Merge(source,target) → MergeResult{ToPropose (stamped PROPOSED), Conflicts, Skipped}` (two-way, additive, never overwrites, pure) |
| `internal/store/branch_diff_test.go` | 12 DB-free tests: diverged diff, identical/empty, metadata-ignored equality; non-conflicting auto-PROPOSED, conflict flagging + target-untouched, target-only no-delete, empty source; stale flagged, no-parent-change quiet, both-changed diverged, parent-delete flagged, local/new-key ignored |
| `docs/decisions/ADR-018-branch-diff-merge-staleness.md` | Why-mandatory ADR (pure-overlay choice, MemoryView↔schema mapping table, behind-vs-diverged, additive-merge rationale) |
| `docs/issues/ISSUE-18.md` | This file |

## Decisions (see ADR-018 for rationale)

1. Pure overlay on locally declared `[]MemoryView` — no import of unlanded
   #17 types, no stubbing of `branches.go`, zero collision surface.
2. Content (not Rev/timestamps) decides diff/merge equality — branches that
   converged on the same text are done, regardless of write history.
3. Staleness = parent-changed-since-fork on a forked key the child carries;
   `ChildChanged` separates fast-forwardable (behind) from review-required
   (diverged); parent-new and child-local keys excluded per plan §5.4.
4. Merge is additive and pure: source-only → PROPOSED inserts (§2.8 flow),
   conflicts preserve target verbatim for humans, no delete propagation,
   no DB I/O — applying results is #17's job.

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test ./internal/store/ -run 'TestDiff|TestMerge|TestStale' -v` →
  **12/12 PASS** (TestDiffDiverged, TestDiffIdenticalAndEmpty,
  TestDiffContentEqualityIgnoresMetadata, TestMergeNonConflictingAutoProposed,
  TestMergeConflictFlagging, TestMergeNoOverwriteTargetOnlyKeys,
  TestMergeEmptySource, TestStaleParentUpdateFlagged,
  TestStaleNoParentChange, TestStaleDivergedBothChanged,
  TestStaleParentDeleteFlagged, TestStaleIgnoresChildLocalAndParentNewKeys)
- Full `go test ./internal/store/` → ok (no regressions)
- Note: `go` is not on PATH in this env; invoked via
  `C:\Program Files\Go\bin\go.exe`.

Acceptance mapping: diverged diff (added/modified/deleted in
TestDiffDiverged), conflict flagging (TestMergeConflictFlagging),
staleness incl. parent-delete (TestStaleParentUpdateFlagged,
TestStaleParentDeleteFlagged), no-overwrite (target-slice-untouched assert
in TestMergeConflictFlagging + PROPOSED-not-CONFIRMED assert in
TestMergeNonConflictingAutoProposed).

## Follow-ups (not this issue)

- #17: bind `MemoryView` to resolved CoW states; apply `ToPropose` as
  PROPOSED inserts on target; surface `IsStale()` as `potentially_stale`.
- #17: reconstruct the fork baseline from `forked_at_event_id` for
  `DetectStale` callers.
- Optional three-way `MergeWithBase` (use fork baseline to auto-skip
  already-converged conflicts) — kept two-way per spec for v1.
- Branch limits from §5.4 (max depth 5, 30-day auto-archive) belong to #17.
