# ISSUE-44 — Branch Upkeep: 30-Day Auto-Archive + Persisted Staleness

- **Issue:** #44 — Branch upkeep (`migrations/007_branch_upkeep.up.sql`/`.down.sql`, store, §5.4)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** subagent
- **Scope constraint:** ONLY `migrations/007_*`,
  `internal/store/branches.go` (+ `branches_test.go`), `docs/`. Did NOT touch
  `001_*`–`005_*`, `branch_diff.go` (owned by #18 — gofmt-clean, reused not
  edited), `db.go`, `projects.go`, `workspaces.go`, `memory.go`, or any other
  package. Read `migrations/005_branches.up.sql`,
  `internal/store/branches.go` (`EnsureMainBranch`, `Fork`, `Read`,
  `WriteToBranch`), `branch_diff.go` (`DetectStale`), `docs/issues/ISSUE-17.md`
  + `ISSUE-18.md` first.
- **Plan ref:** `implementation-plan.md` §5.4 (30-day auto-archive,
  `potentially_stale` on parent-update); depth-5 already enforced by #17.

## What was built

| File | Contents |
|---|---|
| `migrations/007_branch_upkeep.up.sql` | `ALTER TABLE memory_branches ADD COLUMN archived_at TIMESTAMPTZ, ADD COLUMN potentially_stale BOOLEAN DEFAULT false` (table/column names verified against `005_branches.up.sql`) |
| `migrations/007_branch_upkeep.down.sql` | Reverse-order rollback: drop `potentially_stale` → drop `archived_at` |
| `internal/store/branches.go` | `Branch` += `PotentiallyStale bool` / `ArchivedAt *time.Time`; `branchColumns`/`scanBranch` extended; `BranchArchiveTTL` (30d), `IsArchived()`, pure `IsArchivable(b, now)`; `MarkStale` (SET stale RETURNING), `ArchiveBranch` (load → 30-day gate → stamp `archived_at`), `SurfaceStaleness` (`DetectStale` → `UPDATE potentially_stale`) |
| `internal/store/branches_test.go` | Fake extended (`*bool`, `**time.Time`; `branchRow` → 10 cols via `branchRowUpkeep`); 5 new tests: `TestBranchIsArchivable`, `TestBranchMarkStale`, `TestBranchArchiveBranchEligible`, `TestBranchArchiveBranchRefusesYoung`, `TestBranchSurfaceStalenessFlagsAndClears` |
| `docs/decisions/ADR-044-branch-archive-staleness.md` | Why-mandatory ADR (SQL-vs-app choice, main exclusion, no-logic-fork rationale) |
| `docs/issues/ISSUE-44.md` | This file |

## Decisions (see ADR-044 for rationale)

1. Additive migration 007 (no backfill; pre-007 rows COALESCE to `false`).
2. Application-layer upkeep over DB triggers (ADR-017 precedent: testability, no hidden writes).
3. `main` never auto-archives (chain root); refusals are errors, not silent skips.
4. `DetectStale` stays pure (#18 owns it); this issue persists only its boolean outcome (flag set when stale, cleared when clean).
5. Zero timestamps fail closed; zero `now` in `ArchiveBranch` means `time.Now().UTC()`.

## Verification

- Structural SQL review of up/down (table `memory_branches` + no other columns touched, types match spec, down covers every object up creates in reverse order): **passed**.
- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test ./internal/store/` → full suite **PASS** (no regressions; new upkeep tests green — full output below).

Acceptance mapping: 30-day rule (`TestBranchIsArchivable`: 31-day eligible,
exact-30-day boundary, 29-day refused, main/already-archived refused);
persisted staleness (`TestBranchMarkStale`: one `potentially_stale = true`
UPDATE; `TestBranchSurfaceStalenessFlagsAndClears`: stale → UPDATE true,
clean → UPDATE false); auto-archive persist (`TestBranchArchiveBranchEligible`
stamps `archived_at`; `TestBranchArchiveBranchRefusesYoung` errors with zero
UPDATEs).

## Follow-ups (not this issue)

- Sweeper/cron caller for `ArchiveBranch` + resolved-state feeder for `SurfaceStaleness` (primitives only here, no schedule).
- `Read`/`Fork` policy on archived branches (hide vs hard-block) — left additive on purpose.
- `TEST_POSTGRES_DSN`-gated live round-trip (007 up → mark/archive/surface → down).
