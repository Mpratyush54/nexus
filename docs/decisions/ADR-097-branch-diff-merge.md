# ADR-097 — Real Branch Diff/Merge over Store Enumeration

- **ADR ID:** ADR-097-branch-diff-merge
- **Date:** 2026-09-17
- **Author:** subagent (issue #97)
- **Issue:** #97 Store lacks branch item enumeration; diff/merge stubbed
- **Status:** Accepted

## Context

`handleBranchDiff`/`handleBranchMerge` returned empty changes with "no rows
copied" notes because no `Store` method enumerated branch contents, leaving
the stdlib-only `internal/branches` engine (`DiffBranches`, `Merge`) orphaned.
Migration 005 already provides `memory_items.branch_id`, so no schema change
was needed — only the enumeration + wiring. Ownership is limited to
`internal/store/branches.go`, `internal/server/routes_extra.go`, their tests,
and docs (a cross-file helper would have to be inlined; `branchEntries` and
`branchCheckoutView` live in `routes_extra.go` for exactly this reason).

## Options Considered

1. **Resolved-view enumeration (walk chain + main-line fallback per key).**
   Rejected: it would duplicate inherited rows onto the target on copy,
   breaking the CoW zero-copy spirit, and needs N+1 `ResolveRead`s on
   Postgres for no endpoint-visible benefit (diff/merge compare branch-local
   snapshots either way).
2. **(Chosen) Overlay-only enumeration.** `ListBranchItems` returns what was
   written ON the branch: MemStore overlay map values, Postgres
   `branch_id = $1::uuid` rows, both latest-per-key + CONFIRMED/PROPOSED +
   sorted. Copy = snapshot + fresh PROPOSED `WriteToBranch` rows (new ids,
   parents untouched). Diff/merge adapt rows to `branches.Entry{Key,Content}`
   since branch semantics need only key + content (the engine's documented
   seam).
3. **Fork-point snapshot as merge base.** Rejected: no snapshot exists
   (`ForkBranch` records only `forked_at_event_id`, an event pointer, not
   content). Base = source parent's CURRENT items, else empty.

## Decision

- `BranchStore` gains `ListBranchItems` / `CopyItemsToBranch` on BOTH backends
  (`var _ BranchStore` assertions kept compiling); Postgres follows file
  conventions (UUID casts, `pgBranchChain` existence check → `ErrNotFound`,
  `scanMemoryItem`, `WriteToBranch` for inserts, CONFIRMED/PROPOSED filter).
- Diff returns real `added`/`removed`/`modified` (+ `unchanged`), endpoint
  fields unchanged, slices normalized to `[]` never `null`.
- Merge writes `Merged` to target as PROPOSED, returns `conflicts` verbatim,
  and documents the two honest gaps in the response `note` (no stub text).

## Consequences (honest limitations)

- A fresh child enumerates empty until written to; inherited keys never appear
  in diff/merge snapshots.
- Child→main merges cannot conflict on parent-resident keys (target == live
  base by construction); genuine conflicts arise sibling→sibling (proven in
  `TestBranchMergeWritesProposedAndConflicts`).
- `deleted` keys are reported, not removed (no branch-item delete primitive);
  superseded values are shadowed, not marked SUPERSEDED (no overlay
  status-update). Both are follow-ups, stated in the `note`.
- Postgres paths are convention-verified, not live-DB verified (no DB here).
