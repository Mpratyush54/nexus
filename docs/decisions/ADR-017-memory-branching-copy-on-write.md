# ADR-017 — Memory Branching (Copy-on-Write)

- **ADR ID:** ADR-017-memory-branching-copy-on-write
- **Date:** 2026-09-17
- **Author:** issue-#17 agent
- **Issue:** #17 Memory branching (`migrations/005_*` + `internal/store/branches.go`)
- **Status:** Accepted

## Context

Phase 5 (plan §§5.1–5.2) needs Bob to fork from Alice, diverge privately, and
(issue #18) diff/merge back. The acceptance bar for THIS issue is narrower:
fork copies zero rows; reads walk child → parent → main first-match; writes
always land on the current branch, leaving parents unchanged.

Constraints colliding here:

1. **Plan §5.1 SQL references `events(id)`**, which lives in migration 002 —
   so 005 must declare its ordering dependency explicitly.
2. **Parallel ownership** — `001_initial`, `db.go`, `memory.go`,
   `projects.go`, `workspaces.go` belong to other issues; this issue owns only
   `migrations/005_*`, `internal/store/branches.go` (+ tests), `docs/`. In
   particular `MemoryItem` (issue #6) has no `BranchID` field and cannot gain
   one here.
3. **The issue text says `owner`, the plan says `owner_id`** — one name must
   win for the migration.
4. **Acceptance is behavioural** (zero-copy fork, precedence, isolation), so
   the resolution rule must be provable without a live database.
5. **Diff/merge belong to #18** — this issue must expose the seam without
   building on it.

## Options Considered

1. **Copy-on-fork (duplicate parent rows into the child).**
   Pros: reads are single-branch lookups. Cons: fork cost scales with parent
   size; diverging copies drift from later parent updates with no lineage;
   directly contradicts plan §5.2 ("Fork: New branch row … Zero data
   copied") — rejected on plan fidelity alone.
2. **(Chosen) Copy-on-write heads + chain resolution.**
   `memory_branches` rows carry the parent link; `memory_items.branch_id`
   scopes each row to exactly one branch; `FirstMatch` walks the child-first
   chain; `WriteToBranch` INSERTs only. Fork is one row, O(1).
3. **DB trigger auto-creating `main` on project insert.**
   Pros: "automatic" literally. Cons: hidden write path, trigger privilege
   surface on Aurora, untestable without live Postgres. Rejected in favour of
   an explicit idempotent `EnsureMainBranch` (upsert + select) the project
   creation flow calls.

## Decision

- `migrations/005_branches.up.sql`: `memory_branches` per plan §5.1 exactly
  (`project_id → projects`, `owner_id → users` — plan name wins over the
  issue text's `owner`, `parent_branch_id` self-FK, `forked_at_event_id
  BIGINT → events(id)`, `visibility` CHECK private/shared,
  `UNIQUE(project_id, name)`); three operational indexes (project lookup,
  parent walk, branch-scoped items); `ALTER TABLE memory_items ADD COLUMN
  branch_id` with a NAMED FK (`fk_memory_branch`, same pattern as 003's
  `fk_memory_session` — the plan's inline REFERENCES cannot be dropped
  explicitly); main auto-creation documented as application-layer via
  `EnsureMainBranch` (shared, `ON CONFLICT DO NOTHING`).
- `migrations/005_branches.down.sql`: drop `idx_memory_branch` → FK →
  `branch_id` column → table. The column IS dropped (it belongs to 005,
  unlike 003 where `session_id` belonged to 001 and was preserved).
- `internal/store/branches.go`: `Branch` type (empty owner/parent = NULL,
  fork-event 0 = NULL); `BranchStore` (`EnsureMainBranch`, `GetBranchByID`,
  `GetBranchByName`, `Fork` with cross-project + `MaxBranchDepth = 5`
  guards, `AncestorIDs` DB walk, `Read` chain-scoped first-match,
  `WriteToBranch` INSERT-only); `BranchMemory` embedding `MemoryItem` +
  `BranchID` (legacy NULL rows resolve as main-level); pure
  `AncestorChain`, `FirstMatch`, `BuildBranchReadSQL`, `NormalizeVisibility`,
  `ValidateBranchName`, `IsVisibleToUser` (shared → all, private → owner,
  ownerless → nobody); explicit `#18 extension-points` comment, no diff/merge
  code.

## Why (Rationale)

- **Zero-copy is structural, not promised:** `Fork` issues exactly one
  `INSERT INTO memory_branches` — proven by `TestBranchForkZeroCopy`, which
  fails the suite if any statement mentions `memory_items`.
- **Precedence/isolation are proven DB-free:** `TestBranchFirstMatchPrecedence`
  (child shadows, parent fallback, legacy-last), `TestBranchReadPrecedence`
  (+ fallback + not-found via scripted `DBTX`), `TestBranchWriteIsolation`
  (single INSERT, no UPDATE, parent chain still resolves its own value).
- **Seam composes untouched:** only `DBTX`, `ErrNotFound`, `nullUUID`,
  `LevelProject`/`StatusProposed`, `MemoryItem` from sibling files — verified
  `git status` shows only `migrations/005_*`,
  `internal/store/branches*.go`, `docs/`.
- **Deviations are named, not silent:** `owner_id` over `owner` (plan wins,
  FK needs the users target); named FK + indexes (operational, 003-pattern);
  main via `EnsureMainBranch` (testability); `BranchMemory` wrapper instead
  of editing `MemoryItem` (ownership).
- **Evidence:** `go build ./...` 0, `go vet ./internal/store/` 0,
  `go test ./internal/store/ -run TestBranch` unit tests pass (full output
  in `docs/issues/ISSUE-17.md`).

## Consequences

- Project-creation flow (whoever owns it) must call `EnsureMainBranch` —
  until then, a project has no `main` and branch reads 404 on the head
  lookup. Follow-up: wire the call + a `TEST_POSTGRES_DSN`-gated round-trip.
- `MemoryItem` still lacks `BranchID`; unifying `BranchMemory` into it is a
  follow-up for the memory owner (#6), not taken here.
- Plan §5.4 staleness flags (`potentially_stale`) and 30-day auto-archive are
  NOT implemented — follow-ups (staleness likely rides with #18 merge).
- #18 builds diff on `BuildBranchReadSQL` + `FirstMatch` per head and merge
  on `WriteToBranch` (PROPOSED rows on target) — seam is ready, no rework.

## Alternatives Rejected

- Copy-on-fork (option 1): contradicts plan §5.2, O(parent) fork cost, no
  lineage.
- Trigger-created main (option 3): hidden writes + Aurora privilege surface +
  untestable without live DB, for no extra guarantee over the idempotent
  upsert.

## Status update (2026-09-18, issue #151)

- Staleness flags (`potentially_stale`) and 30-day auto-archive ARE implemented (`internal/store/branches_upkeep.go`: `MarkStale`, `ArchiveBranch`); the NOT-implemented note in Consequences above is historical.
