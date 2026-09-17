# ADR-044 — Branch Archive + Persisted Staleness (§5.4)

- **ADR ID:** ADR-044-branch-archive-staleness
- **Date:** 2026-09-17
- **Author:** subagent (issue #44)
- **Issue:** #44 Branch upkeep (30-day auto-archive + persisted staleness)
- **Status:** Accepted

## Context

Plan §5.4 requires two upkeep behaviours that issues #17 and #18 left open:
`MaxBranchDepth = 5` is enforced in `Fork`/`AncestorIDs`, but (a) no 30-day
auto-archive exists and (b) `DetectStale` (branch_diff.go, #18) is pure with
no persisted surfacing — nothing ever writes the plan's `potentially_stale`
signal. #17's `Branch` type, `branchColumns`, and `scanBranch` know nothing
of upkeep columns, and no migration owns them.

Constraints:

1. **Ownership** — this issue owns only `migrations/007_*`,
   `internal/store/branches.go` (+ `branches_test.go`), `docs/`. `branch_diff.go`
   (pure `DetectStale`, #18) must be reused, not reimplemented or reformatted
   (gofmt-clean, untouched).
2. **Column names are fixed by the task** — `memory_branches.archived_at
   TIMESTAMPTZ` + `potentially_stale BOOLEAN DEFAULT false`, verified against
   the `memory_branches` table in `migrations/005_branches.up.sql`.
3. **Acceptance is behavioural without live Postgres** — the 30-day rule must
   be a pure predicate and the persist paths must be provable via the
   scripted `DBTX` fake.

## Options Considered

1. **DB trigger / scheduled sweeper in SQL** — archive and flag maintenance
   inside Postgres. Rejected: hidden write paths and privilege surface on
   Aurora (same argument ADR-017 used against trigger-created `main`), and
   untestable without a live database.
2. **(Chosen) Migration 007 + application-layer upkeep** — additive
   `ALTER TABLE` for the two columns; `IsArchivable` pure 30-day predicate;
   `ArchiveBranch` (load → rule-check → stamp) and `MarkStale` /
   `SurfaceStaleness` (persist `DetectStale` outcome) as `BranchStore`
   methods over the existing `DBTX` seam.
3. **Reimplement staleness in branches.go** — rejected: duplicates #18 logic
   and forks the semantics (`behind` vs `diverged`); `SurfaceStaleness` calls
   `DetectStale` instead.

## Decision

- `migrations/007_branch_upkeep.up.sql`: `ALTER TABLE memory_branches ADD
  COLUMN archived_at TIMESTAMPTZ, ADD COLUMN potentially_stale BOOLEAN
  DEFAULT false`; `.down.sql` drops the two columns (reverse order),
  touching nothing else.
- `internal/store/branches.go`: `Branch` gains `PotentiallyStale bool` +
  `ArchivedAt *time.Time` (nil = active, sessions.go `EndedAt` pattern);
  `branchColumns`/`scanBranch` extended (`potentially_stale` COALESCEs to
  false so pre-007 rows scan cleanly, `archived_at` stays nullable);
  `BranchArchiveTTL = 30*24h`, `IsArchived()`, pure `IsArchivable(b, now)`
  (not-main, not-archived, known timestamps, `now-created_at >= TTL`,
  boundary inclusive, zero-times fail closed); `MarkStale` (SET
  `potentially_stale = true … RETURNING`), `ArchiveBranch` (load →
  `IsArchivable` gate → stamp `archived_at`; refusals are errors, never
  silent skips; zero `now` → `time.Now().UTC()`), `SurfaceStaleness`
  (`DetectStale` → `UPDATE potentially_stale = (len(stale) > 0)`, returning
  the items for review UIs).
- `main` is excluded from auto-archive: it is the project root every chain
  resolves against; archiving it would orphan reads.

## Why (Rationale)

- **Rule is structural and DB-free:** `IsArchivable` is pure —
  `TestBranchIsArchivable` pins old-eligible, exact-boundary, young-refused,
  main-excluded, already-archived, and zero-time-fail-closed cases.
- **Persist paths are proven via the scripted fake:**
  `TestBranchMarkStale` (one `potentially_stale = true` UPDATE),
  `TestBranchArchiveBranchEligible` (one `archived_at` UPDATE on a 31-day
  head), `TestBranchArchiveBranchRefusesYoung` (1-hour head errors with zero
  UPDATEs), `TestBranchSurfaceStalenessFlagsAndClears` (stale → UPDATE true
  + 1 item; clean → UPDATE false + empty).
- **No semantic fork:** staleness detection stays the #18 pure function;
  this issue only persists its boolean outcome, so `behind`/`diverged`
  semantics cannot drift.
- **Evidence:** `go build ./...` 0, `go vet ./internal/store/` 0,
  `go test ./internal/store/` full green (see `docs/issues/ISSUE-44.md`).

## Consequences

- A sweeper/cron caller is still needed to invoke `ArchiveBranch` (and feed
  resolved states into `SurfaceStaleness`); this issue provides the
  primitives, not the schedule.
- `Read`/`Fork` do not yet filter or refuse archived branches — follow-up
  policy decision (hide vs hard-block), deliberately left out to keep this
  change additive.
- Pre-007 rows read `potentially_stale` as false via COALESCE; no backfill
  required.

## Alternatives Rejected

Trigger/sweeper-in-SQL (option 1): hidden writes + Aurora privilege surface
+ untestable here, for no extra guarantee over the explicit store methods.
Staleness reimplementation (option 3): duplicate logic that would drift from
the `behind`-vs-`diverged` contract #18 owns.
