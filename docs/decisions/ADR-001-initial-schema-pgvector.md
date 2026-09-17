# ADR-001 — Initial Schema & pgvector (Migration 001)

- **ADR ID:** ADR-001-initial-schema-pgvector
- **Date:** 2026-09-17
- **Author:** issue-1 subagent
- **Issue:** #1 [Phase 1] Database Schema Migrations: Core Tables & pgvector (001_initial.up.sql)
- **Status:** Accepted

## Context

Phase 1 must prove the Project → Workspace → Daemon → MCP pipeline end-to-end for one
user, one project, one agent, with vector search wired from day 1 (no keyword-only
interim). The schema has to run on AWS RDS Aurora Serverless v2 (PostgreSQL +
pgvector), stay cross-platform (daemon is Go on Windows/macOS/Linux — SQL must not
embed OS paths or platform constructs), and transcribe `implementation-plan.md` §1.1
exactly so later migrations (002 events, 003 sessions, 004 agents, 005 branches) can
assume its tables, columns, and index names. Forward references already exist in the
plan: `episode_events.event_id` gains its FK to `events(id)` in migration 002, and
`memory_items.session_id` gains its FK to `sessions(id)` in 003 — so 001 must ship
those columns *without* the constraints.

## Options Considered

1. **pgvector `vector(1536)` columns + IVFFlat indexes from day 1 (plan §1.1)** —
   pros: semantic memory search works immediately (the product's reason to exist);
   single-engine stack (no sidecar like Qdrant/pg_embedding external service);
   fixed 1536 dims match the v1 embedding model. Cons: ties us to the pgvector
   extension on RDS; IVFFlat needs maintenance at scale.
2. **Keyword/full-text search first, vectors deferred to Phase 2** — pros: zero
   extension dependency, shippable without pgvector on RDS. Cons: contradicts the
   locked plan decision ("Vector search from day 1 + full-text fallback"); keyword
   matching is too weak to feed LLMs, so the Phase 1 exit criterion
   (`memory_search` via vector similarity) could not be met.
3. **HNSW instead of IVFFlat for the embedding indexes** — pros: better recall/latency
   at large scale without retuning lists. Cons: higher build memory and graph
   overhead when tables are small; plan explicitly prescribes IVFFlat under ~100K
   items with HNSW as the later switch, so adopting HNSW now diverges from the plan
   for no measurable v1 benefit.

## Decision

Ship `migrations/001_initial.up.sql` as a verbatim transcription of plan §1.1:
`pgcrypto` + `vector` extensions; tables `users`, `projects` (with separate
`UNIQUE(canonical_url)` / `UNIQUE(root_commit)` matching the identity-priority
fallback chain), `workspaces` (`UNIQUE(machine_id, path)`, `is_designated_processor`
flag), `memory_items` (`vector(1536)`, content 20–2000 CHECK, level/scope/status
CHECKs, IVFFlat + GIN indexes), `episodes` (+ IVFFlat/GIN indexes), `episode_events`
(plain `BIGINT event_id`, FK deferred to 002), `watched_files`, `tasks`; plus a
`001_initial.down.sql` that drops tables in reverse dependency order and keeps the
extensions installed.

## Why (Rationale)

- **Vectors day 1 is load-bearing, not optional:** the Phase 1 exit criterion
  requires `memory_search` over vector similarity returning an XML context block;
  Option 2 cannot satisfy it, and the plan's locked-decisions table mandates
  "Vector search (pgvector) from day 1". Hence Option 1.
- **IVFFlat now, HNSW later:** v1 data volume is far below 100K items, where
  IVFFlat (`lists = 100` memory / `lists = 50` episodes per plan) is cheap to
  build and maintain; HNSW's recall advantage only pays off at scale, so follow
  the plan's explicit scaling note instead of over-engineering now.
- **Separate UNIQUEs (not composite) on `projects`:** the identity resolver matches
  `canonical_url` first, then `root_commit`, then `folder_name` — either signal
  alone must be sufficient to find the project, so each gets its own uniqueness
  guarantee (verified against `internal/project` fingerprint semantics and
  `adapters/walk.go` move-proof `Repo`/`Root` identity).
- **Bare `event_id`/`session_id` columns without FKs:** migrations 002 and 003 own
  the `events` and `sessions` tables; adding the FKs now would reference tables
  that don't exist yet and break ordered migration runners. Deferral is the only
  ordering-safe choice.
- **Down migration keeps extensions:** `vector`/`pgcrypto` are cluster-level,
  later migrations depend on them, and `DROP EXTENSION` on RDS needs elevated
  privileges — dropping them on rollback risks breaking 002+ re-runs for
  permission reasons. Tables (and their indexes) are fully removed, which is the
  correct rollback granularity.
- **Evidence:** transcription verified line-by-line against plan §1.1 (8 tables,
  13 indexes, 2 extensions); SQL self-review (balanced parens/quotes, FK targets
  all defined in-file except the two intentionally deferred); `go vet ./...`
  clean (no Go files touched).

## Consequences

- Migration runners (e.g. golang-migrate `file://migrations`) can apply `001`
  against Aurora Serverless v2 once `pgvector` is enabled in the parameter group.
- `internal/store` (issues #2+) can assume all 8 tables and the documented index
  names; vector search SQL from plan §1.5 works unmodified.
- Follow-ups: 002 must add `events` + the `episode_events → events` FK +
  `LISTEN/NOTIFY` trigger; 003 must add `sessions` + the `memory_items → sessions`
  FK; a future migration switches IVFFlat → HNSW past ~100K embeddings; embedding
  dimension (1536) is now load-bearing — changing models requires a migration.

## Alternatives Rejected

- **Full-text-first (Option 2):** rejected — fails the Phase 1 exit criterion and
  contradicts the locked plan decision.
- **HNSW now (Option 3):** rejected — unjustified build/maintenance cost at v1
  scale; revisit via a dedicated migration when embeddings approach ~100K rows.
- **Dropping extensions in down.sql:** rejected — breaks forward migrations and
  risks RDS permission failures; tables-only rollback is sufficient and safe.
