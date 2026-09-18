# ISSUE-97 — Branch Enumeration + Real Diff/Merge

- **Issue:** #97 — audit: Store lacks branch item enumeration; branch diff and merge are stubbed in server
- **Status:** Done (implementation + tests + docs; no live-DB verification — Postgres paths compile + follow existing conventions, MemStore paths tested)
- **Scope constraint:** ONLY `internal/store/branches.go` (+ `branches_test.go`), `internal/server/routes_extra.go` (+ `routes_extra_test.go`), `docs/`. No migration needed (005 already has `memory_items.branch_id`); no other files touched.

## What was built

| File | Contents |
|---|---|
| `internal/store/branches.go` | `BranchStore` gains `ListBranchItems(ctx, branchID)` (branch's OWN rows, latest-per-key, CONFIRMED/PROPOSED, sorted by key), `CopyItemsToBranch(ctx, from, to)` (fresh PROPOSED rows on target, returns count), `SetWorkspaceBranch` (see #104). MemStore: overlay-bucket read + snapshot-then-write copy (no bucket-mutex deadlock). Postgres: `branch_id = $1::uuid` + `ORDER BY updated_at DESC` with Go-side key dedupe (history rows accumulate, unlike the overlay map); existence via `pgBranchChain` → `ErrNotFound` |
| `internal/server/routes_extra.go` | `handleBranchDiff` adapts items to `branches.Entry` and returns real `added`/`removed`/`modified` via `branches.DiffBranches` (endpoint fields kept, `+unchanged`). `handleBranchMerge` runs `branches.Merge` with base = source parent-branch items (`ParentBranchID` set) else empty; writes `Merged` to target as PROPOSED via `WriteToBranch`; returns `conflicts` verbatim + `superseded`/`deleted`/`merged_keys`/`merged_count` with an honest note. Both stub notes removed |
| `internal/store/branches_test.go` | `TestListBranchItemsCopyRoundTrip` (empty fresh child, unknown→`ErrNotFound`, REJECTED invisible, copy lands 2×PROPOSED with fresh ids, source untouched, unknown endpoints→`ErrNotFound`) |
| `internal/server/routes_extra_test.go` | `TestBranchDiffRealChanges` (added/removed/modified each exactly one key), `TestBranchMergeWritesProposedAndConflicts` (sibling→sibling conflict + PROPOSED write + conflict non-write + clean merge) |
| `docs/decisions/ADR-097-branch-diff-merge.md` | Rationale + honest CoW limitations |

## Decisions (see ADR-097 for rationale)

1. Overlay-only enumeration: inherited parent/main-line keys are NOT listed (fork copies zero rows; enumeration = what was written ON the branch).
2. Base = source parent's CURRENT items, not a fork-point snapshot (no snapshot exists — `ForkBranch` records only `forked_at_event_id`). Consequence: child→main merges cannot conflict on parent-resident keys (target == base); real conflicts arise on sibling→sibling merges. Test proves this shape.
3. Deletions reported, not executed (no branch-item delete primitive); superseded values shadowed, not marked SUPERSEDED (no overlay status-update primitive). Both stated in the merge `note`.

## Verification (2026-09-17, go1.27.0 windows/amd64)

- `gofmt -w` on touched files → clean
- `go build ./...` → exit 0
- `go vet ./internal/store/ ./internal/server/` → no findings
- `go test -count=1 ./internal/store/ ./internal/server/ ./internal/branches/` → all PASS (incl. new tests above; all pre-existing tests green)

## Follow-ups (not this issue)

- Live-Postgres verification of `ListBranchItems`/`CopyItemsToBranch` (no DB available here).
- Fork-point snapshot for merge base (would need content versioning at fork time).
- Branch-item delete primitive so merges can propagate deletions; overlay status-update so superseded rows are marked SUPERSEDED.
