# ADR-028 — Migration Runner Idempotency

- **ADR ID:** ADR-028-migration-runner-idempotency
- **Date:** 2026-09-17
- **Author:** issue-28 subagent
- **Issue:** #28 Migration runner re-applies every `*.up.sql` on each boot
- **Status:** Accepted

## Context

`store.RunMigrations` (issue #2, consumed at boot by
`deploy/server-bootstrap/main.go`) Exec'd every `migrations/*.up.sql` file on
every boot with no record of what already applied. The first boot succeeds;
the second boot re-Execs plain `CREATE TABLE` DDL and the 004 agents seed
rows, failing with `already exists` / duplicate-key errors and crash-looping
the server container (boot migrates before `ListenAndServe`, so a migrate
failure never serves). The integration helper `issue2EnsureSchema` had to
tolerate `already exists` as proof of the bug. Two latent defects compounded
it: the 004 seed `INSERT` had no conflict handling (unsafe to re-encounter
even once), the 004 header claimed 002/003 were optional (`+002/003 when
present`) while its ALTERs enforce FKs into both, and the 001
`content`-length comment (`20-500`) contradicted its own
`CHECK (length(content) >= 20 AND length(content) <= 2000)`.

## Options Considered

1. **`schema_migrations` tracking table + skip-applied (chosen).** Create
   `schema_migrations(filename PK)` with `IF NOT EXISTS` on every run,
   `SELECT` the recorded base names, skip recorded files, `INSERT ... ON
   CONFLICT DO NOTHING` after each success. Pros: standard migration-runner
   semantics; second boot is a no-op; concurrent boots race safely on the PK;
   empty files are recorded so they are not reconsidered; works for all five
   existing migrations with zero DDL rewrites. Cons: one extra table; body
   Exec + record insert are not in one transaction (DBTX has no Begin —
   pre-existing follow-up from ADR-002), so a crash between them can re-Exec
   one file (DDL re-run still errors loudly rather than corrupting).
2. **`IF NOT EXISTS` / `ON CONFLICT` sprinkled through every migration.**
   Rejected: rewrites all five migrations' DDL semantics, weakens schema
   review (a typo'd column would silently pass), and still leaves no record
   of *which* files ran — ordering/partial-failure questions stay unanswered.
   Belt-and-braces kept only where re-encounter is historically real: the
   004 seed rows (pre-tracking databases already ran them once).
3. **External migrator (golang-migrate, psql sidecar, down-migration
   rollback).** Rejected: second runner to diverge from `RunMigrations`,
   contradicts the locked in-process-migrate decision (ADR-020) and the
   forward-only contract; rollback stays forward-only via new migrations.

## Decision

- `internal/store/db.go`: `RunMigrations` ensures `schema_migrations` via
  `CREATE TABLE IF NOT EXISTS`, reads applied base names with
  `SELECT filename FROM schema_migrations`, skips recorded files (no Exec),
  records each success with
  `INSERT INTO schema_migrations (filename) VALUES ($1) ON CONFLICT DO
  NOTHING`. Returns only newly applied base names; missing dir stays
  `(nil, nil)`; tracking-read/record failures fail closed with wrapped
  errors. `ListMigrationFiles` unchanged.
- `migrations/004_agents.up.sql`: seed `INSERT` ends with
  `ON CONFLICT (name) DO NOTHING` (safe re-encounter on pre-tracking
  databases); header now states `Applies on top of 001 + 002 + 003 in
  order` (the ALTERs enforce FKs into 003 `sessions` and 002 `events`).
- `migrations/001_initial.up.sql`: comment-only fix — `content` is
  `20-2000 chars enforced`, matching the CHECK constraint.
- `internal/store/db_test.go`: tracking-aware fake (`Query` serves the
  in-memory applied set; `INSERT INTO schema_migrations` Execs populate it)
  plus `TestMigrationDoubleApplyIsNoop`, `TestMigrationSkipApplied`,
  `TestMigrationSeedRerunIdempotent`, `TestMigrationQueryErrorFailsClosed`.

## Why (Rationale)

This is the only option that makes the production boot path safe: the
server bootstrap crash-loops today on every restart after the first, and no
DDL-sprinkling fixes the missing *record of what ran*. Tracking is the
minimal mechanism with precedent in every migration tool, and it preserves
all existing contracts — sorted `*.up.sql` order, missing-dir no-op,
forward-only, partial-`applied` on Exec error — while turning the second
boot into `([], nil)`. The 004 seed conflict clause is mandatory, not
optional: databases migrated by the old runner have the seven agent rows but
no tracking row, so the first new-runner boot re-Execs the 004 body and only
`ON CONFLICT DO NOTHING` saves it. The header/comment fixes are mandatory
for the same reason as the code: operators reading `when present` will apply
004 without 002/003 and hit missing-table ALTER failures, and readers of
`20-500` will misjudge valid content the CHECK accepts to 2000. Evidence:
`go build ./...` clean, `go vet ./internal/store/` clean, and the new
`TestMigration*` suite (double-apply no-op, skip-applied, seed re-run,
fail-closed tracking read) plus all pre-existing store tests pass — see
`docs/issues/ISSUE-28.md`.

## Consequences

- First boot after deploy applies pending files and records them; every
  later boot returns `[]` and Execs only the `IF NOT EXISTS` ensure plus one
  `SELECT` — `/readyz` `migrations_applied` now reports *newly* applied per
  boot (0 on steady-state restarts).
- Pre-tracking databases self-heal for seeds (004 `ON CONFLICT`) but DDL
  files they already ran will correctly attempt once more and surface the
  pre-existing `already exists` error; that one-time failure is the honest
  signal (the old helper's tolerate-`already exists` path can now be
  retired by the store owners).
- Follow-ups (not this issue): wrap body+record in a transaction when DBTX
  gains Begin (ADR-002 follow-up); multi-migrator leader election if the
  service ever scales past count 1 (ADR-020 runs a single boot migrator).

## Alternatives Rejected

See Options 2–3 above: per-file `IF NOT EXISTS` everywhere (rewrites DDL
semantics, records nothing) and external migrators/down-migration rollback
(second runner, contradicts ADR-020 forward-only contract).
