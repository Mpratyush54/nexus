# ISSUE-1 — [Phase 1] Database Schema Migrations: Core Tables & pgvector

- **Status:** Done
- **Scope:** `migrations/` + `docs/` only (no `internal/` or other dirs touched)
- **Plan ref:** `implementation-plan.md` §1.1 (transcribed exactly)

## What was built

- `migrations/001_initial.up.sql` — extensions (`pgcrypto`, `vector`) + 8 tables:
  `users`, `projects` (separate `UNIQUE(canonical_url)` / `UNIQUE(root_commit)`),
  `workspaces` (`UNIQUE(machine_id, path)`, `is_designated_processor`), `memory_items`
  (`vector(1536)`, content 20–2000 CHECK, level/scope/status CHECKs, IVFFlat + GIN
  indexes), `episodes` (+ IVFFlat/GIN indexes), `episode_events` (bare `BIGINT`
  `event_id`, FK deferred to 002), `watched_files`, `tasks`.
- `migrations/001_initial.down.sql` — reverse-dependency-order `DROP TABLE IF EXISTS`
  for all 8 tables; extensions intentionally kept (shared infra for 002+, RDS
  privilege safety).
- `docs/decisions/ADR-001-initial-schema-pgvector.md` — full ADR (Context / Options /
  Decision / Why / Consequences / Alternatives Rejected) with rationale for every
  critical choice.
- `docs/issues/ISSUE-1.md` — this file.

## Verification

- Line-by-line self-review of up.sql against plan §1.1: 8/8 tables, 13/13 indexes,
  2/2 extensions present; all in-file FK targets resolve; the only intentionally
  unresolved references are `episode_events.event_id → events(id)` (owned by 002)
  and `memory_items.session_id → sessions(id)` (owned by 003).
- Structural SQL sanity check via script (balanced parentheses/quotes, statement
  count, down.sql covers all 8 tables): **passed** (see output below).
- `go vet ./...`: **clean, no findings** (no Go files touched, as expected).
- No live Postgres available in this environment, so `APPLY`/`ROLLBACK` against a
  real Aurora/pgvector instance is left as a follow-up for CI or the integrator.

## Follow-ups

- Migration 002 (`events` table + `episode_events` FK + `LISTEN/NOTIFY`) and 003
  (`sessions` + `memory_items` FK) must land before the store layer issues.
- Confirm `pgvector` is enabled in the Aurora Serverless v2 parameter group before
  first apply; IVFFlat → HNSW switch when embeddings approach ~100K rows.
