# ISSUE-17 — Memory Branching, Copy-on-Write (Schema + Store)

- **Issue:** #17 — Branching (`migrations/005_branches.up.sql`/`.down.sql`, store, CoW resolution)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** subagent
- **Scope constraint:** ONLY `migrations/005_*`,
  `internal/store/branches.go` (+ tests), `docs/`. Did NOT touch `001_*`,
  `002_*`, `003_*`, `db.go`, `projects.go`, `workspaces.go`, `memory.go`,
  `episodes.go`, `events.go`, `sessions.go`, or any other package. Diff/merge
  (plan §5.2, last two bullets) belong to #18 — left as documented extension
  points, no code.
- **Plan ref:** `implementation-plan.md` §§5.1 (branch schema + auto-main),
  5.2 (CoW read/write/fork); read `migrations/001_initial.up.sql` (base
  tables) and `internal/store/memory.go` (search seam + `DBTX`-adjacent
  `Querier` conventions) first.

## What was built

| File | Contents |
|---|---|
| `migrations/005_branches.up.sql` | `memory_branches` (plan §5.1 exact: `owner_id → users`, self-FK, `forked_at_event_id BIGINT → events(id)` — requires 002 applied first, `visibility` CHECK, `UNIQUE(project_id, name)`); 3 operational indexes; `ALTER TABLE memory_items ADD COLUMN branch_id` + named FK `fk_memory_branch`; main auto-create documented as `EnsureMainBranch` (not a trigger) |
| `migrations/005_branches.down.sql` | Reverse-order rollback: `idx_memory_branch` → FK → `branch_id` column → table (column dropped: it belongs to 005) |
| `internal/store/branches.go` | `Branch` type; `BranchStore`: `EnsureMainBranch` (idempotent upsert, shared), `GetBranchByID/ByName`, `Fork` (zero-copy, cross-project + depth guards), `AncestorIDs` (cycle-guarded, depth-capped), `Read` (chain first-match), `WriteToBranch` (INSERT-only); `BranchMemory` (embeds `MemoryItem` + `BranchID`, legacy NULL = main-level); pure `AncestorChain`, `FirstMatch`, `BuildBranchReadSQL`, `NormalizeVisibility`, `ValidateBranchName`, `IsVisibleToUser`; #18 extension-point comment |
| `internal/store/branches_test.go` | 14 DB-free tests: visibility/name validation, chain walk (linear/root/cycle/depth-cap), first-match precedence, read-SQL shape, fork zero-copy + validation + cross-project + depth, read precedence/fallback/not-found via scripted `DBTX` fake, write isolation, ensure-main |
| `docs/decisions/ADR-017-memory-branching-copy-on-write.md` | Why-mandatory ADR (CoW over copy-on-fork, trigger rejection, ownership seam, named deviations) |
| `docs/issues/ISSUE-17.md` | This file |

## Decisions (see ADR-017 for rationale)

1. Copy-on-write heads, not copy-on-fork (plan §5.2: fork = one branch row, zero data copied).
2. Plan's `owner_id` wins over the issue text's `owner` (FK target must be `users(id)`).
3. Named FK (`fk_memory_branch`, 003-pattern) + 3 indexes as documented operational additions.
4. `main` auto-creation = application-layer `EnsureMainBranch` (shared, `ON CONFLICT DO NOTHING`), not a DB trigger.
5. `BranchMemory` wraps `MemoryItem` instead of editing it (issue #6 owns that type).
6. `MaxBranchDepth = 5` enforced in `Fork` + `AncestorIDs` (plan §5.4); staleness flags + auto-archive deferred.
7. Diff/merge NOT implemented (#18 owns it); seam = `AncestorIDs`/`AncestorChain` + `FirstMatch` + `WriteToBranch`.

## Verification

- Structural SQL review of up/down (balanced parens/quotes, FK targets resolve against 001+002+005, down covers every object up creates, 002-before-005 ordering noted): **passed**.
- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test ./internal/store/ -run TestBranch` → unit tests **PASS** (full output below; `-v` names prove fork-zero-copy, read precedence, write isolation).

Acceptance mapping: fork-zero-copy (`TestBranchForkZeroCopy`: zero
statements touch `memory_items`, exactly one `INSERT INTO memory_branches`);
read precedence (`TestBranchFirstMatchPrecedence` + `TestBranchReadPrecedence`/
`FallsBackToParent`: child → parent → main, legacy-last); write isolation
(`TestBranchWriteIsolation`: single INSERT, no UPDATE, parent chain still
resolves parent-value).

## Follow-ups (not this issue)

- #18: diff (per-head `BuildBranchReadSQL` + `FirstMatch` compare) + merge (source items → PROPOSED via `WriteToBranch`, conflicts surfaced).
- Project-creation flow must call `EnsureMainBranch`; add `TEST_POSTGRES_DSN`-gated live round-trip (fork → diverge → read-precedence → write-isolation against real rows).
- Memory owner (#6): unify `BranchMemory.BranchID` into `MemoryItem` if desired.
- Plan §5.4: `potentially_stale` flags on parent-update + 30-day branch auto-archive.
- Live apply of 005 up/down against Aurora/pgvector in CI (no live Postgres in this env).
