# Fix: Versioned Migrations with schema_migrations (nexus issue #2 review)

Date: 2026-09-17. Scope: `internal/store/db.go` (+ `internal/store/migrations_test.go`).
Every choice below carries Context / Decision / Alternatives / Why /
Consequences.

---

## 1. Track applied migrations in schema_migrations instead of re-running all files

- **Context:** `RunMigrations` executed every `migrations/*.up.sql` file
  unconditionally on each server launch. Re-execution was only safe by
  convention (every file idempotent), wasted work on every boot, and would
  break the moment a non-idempotent migration landed. Code review on nexus
  issue #2 flagged this.
- **Decision:** Create `schema_migrations (version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now())` on every run, `SELECT`
  applied versions, execute only files whose version
  (`strings.TrimSuffix(name, ".up.sql")`, e.g. `001_initial`) is unrecorded,
  then `INSERT` the version in the same transaction as the migration body.
- **Alternatives:** (a) Leave unconditional re-runs — zero bookkeeping but
  every boot replays DDL and any future non-idempotent statement corrupts
  data. (b) Filesystem marker / in-memory flag — does not survive restarts
  or multi-instance fleets; the database is the only shared ground truth.
  (c) Third-party migrate library (golang-migrate, goose) — heavier
  dependency and migration-format churn for a 5-file schema.
- **Why:** The database already coordinates all daemon/server instances; a
  one-table ledger is the minimal shared state that makes "applied exactly
  once" checkable. Version = filename keeps the mapping obvious and
  `migrate.sh` ordering (lexical) unchanged.
- **Consequences:** `RunMigrations` now requires `CREATE` + `SELECT` +
  `INSERT` rights on `schema_migrations`. A file whose content changes
  without a rename is NOT re-applied (version already recorded) — schema
  fixes must ship as new numbered files, which is the standard contract.

## 2. Backward compatibility via re-apply-then-record on first versioned run

- **Context:** Production databases already have 001–005 applied with no
  tracking rows. The first deploy of versioned code sees an empty
  `schema_migrations` and treats every file as pending.
- **Decision:** Do NOT pre-seed or checksum-sniff; simply re-execute each
  unrecorded file (safe: all current migrations use `CREATE TABLE/INDEX IF
  NOT EXISTS`, guarded `DO`-block `ALTER`s, `INSERT ... ON CONFLICT DO
  NOTHING`), then record the version. From the second boot on, the skip
  path applies.
- **Alternatives:** (a) Assume-empty-DB and fail if tables exist — would
  break every existing deploy. (b) Mark-all-applied-without-running —
  would silently skip a partially-applied DB (e.g. crashed mid-004) and
  leave schema behind code. (c) Checksum-compare before deciding — precise
  but complex; checksums drift with comment edits and add a failure mode.
- **Why:** Re-apply is the only option that converges both a fresh DB and a
  legacy DB to the same state with the same code path, and the current
  migration set was audited idempotent (001–005 verified) so the extra
  replay is provably harmless.
- **Consequences:** First versioned boot does ~5 redundant DDL replays
  (one-time cost, transaction-wrapped). Future migrations MUST preserve
  the idempotency convention so this guarantee holds for the next
  "untracked" edge (e.g. a DB restored from an old snapshot).

## 3. One transaction per migration (body + version INSERT atomically)

- **Context:** A crash between "DDL applied" and "version recorded" must
  not mark a half-applied migration done, nor leave it half-recorded.
- **Decision:** `BEGIN`, `EXEC` migration SQL, `INSERT INTO
  schema_migrations ... ON CONFLICT DO NOTHING`, `COMMIT` per file.
  Failure rolls back both body and record, so retry replays the whole
  file cleanly. `ON CONFLICT DO NOTHING` on the INSERT absorbs the
  two-instances-boot-simultaneously race (both run the body idempotently,
  one wins the record).
- **Alternatives:** (a) No transaction (old behavior + separate INSERT) —
  crash window leaves applied-but-unrecorded (replay, benign but noisy) or
  recorded-but-unapplied (schema behind code, dangerous). (b) Single
  transaction for ALL files — one bad file rolls back all good ones and
  holds DDL locks longer.
- **Why:** Per-file atomicity is the smallest unit that keeps "recorded"
  exactly meaning "fully applied", while keeping lock scope minimal.
- **Consequences:** Migration SQL must stay transaction-safe — no `CREATE
  INDEX CONCURRENTLY`, `CREATE DATABASE`, or other non-transactional DDL.
  If such a statement is ever needed, it must ship as a specially-handled
  migration (documented in code comment), not silently added to a `.up.sql`.

## 4. Pure helpers + DB-guarded integration test (no hard test dependency on Postgres)

- **Context:** `go test ./internal/store/` must stay green on machines
  without Postgres (MemStore tests are DB-free by design); the skip/record
  logic still needs coverage.
- **Decision:** Extract `migrationVersion` (filename→version) and
  `filterPendingMigrations` (sorted files × applied set → pending) as pure
  functions with table-driven unit tests (parsing, partial-applied,
  record-then-empty idempotency, unknown-version robustness). Cover the
  live path (record-once, skip-on-rerun, tracking-wipe backward compat)
  in `TestRunMigrationsRecordsAndSkips`, skipped unless `DATABASE_URL` is
  set.
- **Alternatives:** (a) Only integration tests — CI/dev machines without
  Postgres go red. (b) No tests — the exact logic the review flagged stays
  unverified. (c) Mock the pgx pool — pgxpool interfaces are heavy to
  fake and the mock would test itself, not the SQL.
- **Why:** The bug class (wrong skip predicate, wrong version string)
  lives entirely in the pure functions; testing them without a DB gives
  deterministic coverage where it matters, and the guarded integration
  test proves the SQL/transaction wiring where a real DB exists.
- **Consequences:** `DATABASE_URL` runs exercise real DDL with temp tables
  (`migtest_*`) and clean up after themselves; reviewers must run that one
  manually against Postgres at least once per migration change.
