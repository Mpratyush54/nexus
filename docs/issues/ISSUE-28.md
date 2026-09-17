# ISSUE-28 — Migration Runner Idempotency (second boot no-op)

- **Status:** Done
- **Scope:** `internal/store/db.go`, `internal/store/db_test.go`,
  `migrations/004_agents.up.sql` (seed idempotency + header),
  `migrations/001_initial.up.sql` (comment only), plus
  `docs/decisions/ADR-028-migration-runner-idempotency.md` and this file.
  Nothing else touched.
- **Plan ref:** `implementation-plan.md` §1.1 (schema) via the forward-only
  `001_*…005_*` contract; `deploy/server-bootstrap/main.go` (migrate before
  `ListenAndServe` — a migrate failure crash-loops the container).

## Problem

`RunMigrations` Exec'd every `migrations/*.up.sql` on each boot with no
applied-tracking: boot 1 succeeded, boot 2 re-ran plain `CREATE TABLE` DDL
and the 004 agents seed and failed (`already exists` / duplicate key), so
the server never served after a restart. Additionally the 004 seed `INSERT`
had no conflict handling, the 004 header claimed 002/003 were optional
(`+002/003 when present`) despite ALTERs enforcing FKs into both, and the
001 `content` comment (`20-500 chars`) contradicted its CHECK (`20..2000`).

## What was built

| File | Change |
|---|---|
| `internal/store/db.go` | `RunMigrations` now ensures `schema_migrations(filename PK)` with `CREATE TABLE IF NOT EXISTS`, `SELECT`s recorded base names, skips recorded files without Exec, and records each success with `INSERT ... VALUES ($1) ON CONFLICT DO NOTHING`. Returns newly applied base names only; missing dir stays `(nil, nil)`; tracking-read/record failures fail closed. `ListMigrationFiles` unchanged. |
| `internal/store/db_test.go` | Tracking-aware fake (`Query` serves the applied set; tracking inserts populate it). Kept `TestStore_*` suites working under the new flow; added `TestMigrationDoubleApplyIsNoop` (boot twice → second is `[], nil`, no body re-Exec), `TestMigrationSkipApplied` (pre-recorded 001 skipped, 002 applies), `TestMigrationSeedRerunIdempotent` (real 004 carries `ON CONFLICT (name) DO NOTHING`, header states `001 + 002 + 003 in order`, runner second pass no-op), `TestMigrationQueryErrorFailsClosed` (tracking-read failure applies nothing). |
| `migrations/004_agents.up.sql` | Seed `INSERT` ends with `ON CONFLICT (name) DO NOTHING` (re-encounter safe, incl. pre-tracking databases); header fixed to `Applies on top of 001 + 002 + 003 in order`. DDL otherwise untouched. |
| `migrations/001_initial.up.sql` | Comment-only: `content ... 20-2000 chars enforced` to match the CHECK. No SQL change. |
| `docs/decisions/ADR-028-migration-runner-idempotency.md` | Why-mandatory ADR (tracking vs `IF NOT EXISTS`-everywhere vs external migrator). |

## Decisions

- See ADR-028 for rationale (tracking table is the only fix that records
  *what ran*; seed conflict clause heals pre-tracking databases; header and
  comment fixes prevent operator misreads).

## Verification

- `go build ./...` — **clean**
- `go vet ./internal/store/` — **clean, no findings**
- `go test ./internal/store/ -run 'TestMigration|TestMigrat'` — **new
  idempotency tests PASS** (`TestMigrationDoubleApplyIsNoop`,
  `TestMigrationSkipApplied`, `TestMigrationSeedRerunIdempotent`,
  `TestMigrationQueryErrorFailsClosed`)
- `gofmt -l` on touched Go files — **clean**

## Follow-ups (not this issue)

- Store owners: retire the `already exists`-tolerant path in
  `issue2EnsureSchema` (`internal/store/integration_test.go`) now that
  re-runs are `( [], nil)`; wrap body+record in a transaction when `DBTX`
  gains `Begin` (ADR-002 follow-up).
- Single boot-time migrator (service count 1, ADR-020) assumed; revisit with
  leader election if replicas ever migrate concurrently.
