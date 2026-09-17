# ISSUE-36 — Designated-processor election (single designatee + stale-owner failover)

- **Status:** Done
- **Scope:** `migrations/008_processor_election.up.sql`,
  `migrations/008_processor_election.down.sql`,
  `internal/store/workspaces.go` (`ElectDesignatedProcessor`,
  `ReassignStaleDesignated`, index note on `SetDesignatedProcessor`),
  `internal/store/workspaces_test.go` (fake + 7 tests), plus
  `docs/decisions/ADR-036-processor-election.md` and this file. No other files
  touched. `SetDesignatedProcessor` behavior unchanged. `go.mod`/`go.sum`
  untouched.
- **Plan refs:** `implementation-plan.md` §6.3 (failover when the owner goes
  offline); `docs/issues/ISSUE-2.md` follow-ups (deferred failover);
  `docs/decisions/ADR-010-memory-processor.md` (designated-only gating).

## Problem

The designated-processor flag was a bare flip: `SetDesignatedProcessor`
updates one row with no project-wide guard, so multiple workspaces of the
same project could all carry `is_designated_processor` (forked extraction),
and a dead owner kept the flag while its daemon's processor — gated on
`IsDesignated` — silently stopped extracting for the whole project.

## What was built

`migrations/008_processor_election.*` (numbered 008: 007 belongs to the
parallel branch-upkeep track; depends on 001's `workspaces` columns only):

| Piece | Behavior |
|---|---|
| `uq_workspaces_designated_processor_online` | Partial unique index: at most one `is_designated_processor AND is_online` workspace per `project_id`; conflicts fail instead of forking extraction; swept-offline corpses leave the index so successors never conflict |
| down migration | `DROP INDEX IF EXISTS uq_workspaces_designated_processor_online` (columns untouched — owned by 001) |

`internal/store/workspaces.go`:

| Piece | Behavior |
|---|---|
| `ElectDesignatedProcessor(projectID)` | Validates ID (no DB touch on empty); revokes all project flags, then designates the freshest online workspace (`is_online` AND `last_seen` within `OfflineAfter`, `ORDER BY last_seen DESC, id`); revoke-before-grant order never transiently violates the index; no online candidate → wrapped `ErrNotFound` (revoke still stands — no corpse designatee) |
| `ReassignStaleDesignated()` | Clears `is_online` + `is_designated_processor` on designatees silent past `OfflineAfter` or never seen; strict `<` + NULL semantics identical to `MarkStaleOffline`/`IsStaleAt`; returns swept count; successor election is a follow-up `Elect` per project |
| `SetDesignatedProcessor` | Unchanged logic + doc note: still the explicit admin path, now index-guarded |

`internal/store/workspaces_test.go` — workspace-scoped scripted `DBTX` fake
(`wsElectFakeDB`/`wsElectFakeRow`, no collision with the branch/agent/session
fakes; reuses `stmtsMention`-style assertions) + 7 tests: empty-project
validation without DB touch; revoke-then-grant order/fragments/args/returned
row; empty-grant `ErrNotFound`; revoke error propagation (grant never runs);
sweep fragments/args/`RowsAffected` via `pgconn.NewCommandTag("UPDATE 2")`;
exec-error propagation; strict-`<` boundary alignment with `IsStaleAt`.

## Decisions

- See `docs/decisions/ADR-036-processor-election.md` (partial unique index
  over read-then-write election and over a deferrable EXCLUDE constraint;
  separate sweeper over folding into `MarkStaleOffline`; deterministic
  `last_seen DESC, id` winner so concurrent electors converge; unique
  violations mean "retry the election").
- One subtlety the tests pin: `UPDATE ... SET flag = (id = NULL-winner)`
  would write NULL when nobody is online, so the grant is a separate
  conditional statement whose no-match surfaces as `ErrNotFound` instead.

## Verification

- SQL self-review: index columns (`project_id`, `is_online`,
  `is_designated_processor`) verified against `001_initial.up.sql`;
  predicate style matches 003's bare-`WHERE` partial indexes; down migration
  drops only the index.
- `go build ./internal/store/` — **OK** (`go build ./...` still fails, but
  only in parallel agents' uncommitted files: `internal/daemon/watcher.go`
  unused `encoding/json` import, `internal/server/wsbridge.go` `*pgxpool.Pool`
  vs `store.DBTX` mismatch — both untouched per scope).
- `go vet ./internal/store/` — **clean**.
- `gofmt -l internal/store/workspaces.go internal/store/workspaces_test.go`
  — **clean** (other `internal/store` flags are pre-existing parallel-agent
  dirt, untouched).
- `go test ./internal/store/ -run 'TestDesignat|TestElect|TestWorkspace' -v
  -count=1` — **all 7 PASS** (`TestElectDesignatedProcessorRejectsEmptyProject`,
  `TestElectDesignatedProcessorRevokesThenGrantsFreshest`,
  `TestElectDesignatedProcessorNoOnlineCandidateIsNotFound`,
  `TestElectDesignatedProcessorPropagatesRevokeError`,
  `TestDesignatedProcessorReassignStaleSweepsOnlySilent`,
  `TestDesignatedProcessorReassignPropagatesExecError`,
  `TestWorkspaceElectionBoundaryStaysAlignedWithPredicates`).
- `go test ./internal/store/ -count=1` (full package incl. all pre-existing
  suites) — **PASS**.

## Follow-ups

1. Server sweep loop (~30s, alongside `MarkStaleOffline`): call
   `ReassignStaleDesignated`, then `ElectDesignatedProcessor` per affected
   project; retry elections that hit the unique index.
2. Gated live-Postgres test (`TEST_POSTGRES_DSN`): apply 008 and prove a
   second online designation conflicts while an offline designatee does not.
