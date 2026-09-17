# ADR-036-processor-election

- **ADR ID:** ADR-036-processor-election
- **Date:** 2026-09-17
- **Author:** issue-36 (store subagent)
- **Issue:** #36 Designated-processor election (bare flag allows duplicates; dead owner stalls extraction)
- **Status:** Accepted

## Context

`WorkspaceStore.SetDesignatedProcessor` (issue #2) is a bare per-row flip:
nothing stops two workspaces of the same project from both carrying
`is_designated_processor`, and a dead owner keeps the flag while extraction
silently stops — the daemon processor gates everything on
`Processor.IsDesignated` (`internal/daemon/processor.go:868`: non-designated
instances buffer nothing). ISSUE-2 deferred failover to plan §6.3; this issue
implements it. Constraints: `DBTX` has no transaction/begin support (election
must compose atomic single statements); all new store paths must stay DB-free
testable via the scripted-fake pattern; migration numbering must skip the
parallel tracks (007 exists as `migrations/007_branch_upkeep.*`, hence 008).

## Options Considered

1. **Partial unique index + revoke-before-grant election + stale sweep (chosen).**
   `WHERE is_designated_processor AND is_online` unique index makes the
   database the single-designatee arbiter; election revokes project-wide then
   grants the freshest-heartbeat online workspace; a sweeper clears dead
   owners. Pros: races fail loudly on the index instead of forking extraction;
   swept (offline) corpses leave the index so successors never conflict.
2. **Application-level read-then-write election.** Check current designatee,
   then flip. Rejected: check-then-act across two round trips has a race
   window with no arbiter — two electors both see "no designated" and both
   designate. The index in option 1 closes exactly this hole.
3. **Deferrable EXCLUDE constraint instead of a partial unique index.**
   Expressible (`EXCLUDE ... WHERE (designated AND online) DEFERRABLE`), and
   would allow single-statement clear+set. Rejected: exotic next to the
   repo's plain partial-index style (003's `uq_session_participants_*`,
   `idx_sessions_active`); ordered revoke-then-grant achieves the same safety
   with familiar DDL.
4. **Fold failover into `MarkStaleOffline`.** Rejected: that sweeper owns the
   `is_online` flag globally; mixing role revocation into it couples two
   lifecycles and hides the election precondition. A separate
   `ReassignStaleDesignated` mirrors its boundary semantics exactly (strict
   `<`, NULL counts as stale) while staying independently callable.

## Decision

- `migrations/008_processor_election.up.sql` — `CREATE UNIQUE INDEX
  uq_workspaces_designated_processor_online ON workspaces(project_id) WHERE
  is_designated_processor AND is_online` (columns verified against
  `001_initial.up.sql`; bare-WHERE style matches 003); down migration drops it.
- `WorkspaceStore.ElectDesignatedProcessor(projectID)` — validates the ID,
  revokes all project flags, then designates the freshest online workspace
  (`is_online` AND `last_seen` within `OfflineAfter`, `ORDER BY last_seen
  DESC, id`, same authoritative predicate as `ListActive`); empty grant maps
  `pgx.ErrNoRows` to `ErrNotFound`.
- `WorkspaceStore.ReassignStaleDesignated()` — clears both flags on
  designated workspaces silent past `OfflineAfter` (or never seen), returns
  the swept count; successor election is a follow-up `Elect` call per project.
- `SetDesignatedProcessor` unchanged (explicit admin path, now index-guarded).
- 7 new DB-free tests via a workspace-scoped scripted `DBTX` fake.

## Why (Rationale)

This is the minimal combination closing both failure modes with proof:

- Duplicates become impossible at the storage layer: the index rejects a
  second online designatee, and the revoke-before-grant order can never
  transiently hold two (verified by statement-order assertions in
  `TestElectDesignatedProcessorRevokesThenGrantsFreshest`).
- Dead owners stop stalling extraction: the sweep revokes silent designatees
  under the exact `IsStaleAt` boundary contract (pinned by
  `TestWorkspaceElectionBoundaryStaysAlignedWithPredicates` + the 90s cases in
  `TestStore_IsOnlineAt`), and the successor grant reuses `ListActive`'s
  predicate so electability and visibility never disagree.
- Verification is green: `go build ./internal/store/` OK,
  `go vet ./internal/store/` clean, targeted
  `-run 'TestDesignat|TestElect|TestWorkspace'` 7/7 PASS, full
  `go test ./internal/store/ -count=1` PASS (see `docs/issues/ISSUE-36.md`).

## Consequences

- A divergent concurrent election fails with a unique violation; callers
  (server sweep loop / daemon) must retry `ElectDesignatedProcessor`.
- Election churn: a healthy designatee is revoked and re-granted each run
  (same deterministic winner, one extra write, `last_seen` untouched).
- Follow-ups: a server-side sweep loop calling `ReassignStaleDesignated`
  then per-project `Elect` (~30s cadence, like `MarkStaleOffline`); gated
  live-Postgres test proving the index rejects a second online designatee
  (`TEST_POSTGRES_DSN`, same gate as the issue-2 integration tests).

## Alternatives Rejected

See Options 2–4 above: app-level election races without an arbiter; EXCLUDE
constraint trades familiar DDL for exotic DDL with no safety gain; folding
into `MarkStaleOffline` couples flag lifecycle to role lifecycle.
