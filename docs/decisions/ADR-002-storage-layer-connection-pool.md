# ADR-002 — Storage Layer: Pool, Resolver & Heartbeats

- **ADR ID:** ADR-002-storage-layer-connection-pool
- **Date:** 2026-09-17
- **Author:** issue-2 subagent
- **Issue:** #2 [Phase 1] Storage Layer & Connection Pool (`internal/store/`)
- **Status:** Accepted

## Context

Phase 1 needs a Postgres data-access foundation that three later consumers
share: the central server (project resolve, workspace presence), the daemon
(register, heartbeat every 30s), and the memory/episode agents that already
started landing in `internal/store` (`memory.go`, issue #6, stdlib-only with
its own narrow `Querier`/`Rows` interfaces). Constraints: only two new deps
allowed (`github.com/jackc/pgx/v5`, `github.com/pgvector/pgvector-go`); code
against the planned schema in `implementation-plan.md` §1.1 (migrations owned
by issue #1, now `migrations/001_initial.up.sql`); acceptance demands
deterministic resolution with no duplicate projects, heartbeat expiry after
90s silence, and unit tests that run without a live Postgres.

## Options Considered

1. **pgxpool with explicit limits + `DBTX` interface seam** — pros: one pool
   owner (`*DB`), tunable bounds for Aurora Serverless v2, stores depend on an
   interface so unit tests run DB-free; `*DB.Query` returns the narrow
   `memory.go` `Rows`, so `*DB` satisfies `memory.Querier` with zero adapter.
   Cons: `*pgxpool.Pool` itself does not satisfy `DBTX` (its `Query` returns
   `pgx.Rows`), so callers wrap the pool in `*DB`; transactions are a
   follow-up.
2. **`database/sql` + lib/pq** — pros: familiar, `database/sql` interfaces
   everywhere. Cons: `lib/pq` is a third dependency (forbidden by the issue);
   no native composite/vector story; strictly worse than pgx for the planned
   `LISTEN/NOTIFY` and `COPY` paths.
3. **Per-call `pgx.Connect` (no pool)** — pros: trivially simple. Cons: a new
   TCP+TLS+auth handshake per heartbeat per daemon rediscovers the pool
   exhaustion from the plan's episode lore; fails the connection-pool
   acceptance outright.

## Decision

- `internal/store/db.go`: `Config`/`DefaultConfig` (MaxConns 16, MinConns 2,
  MaxConnLifetime 30m, MaxConnIdleTime 5m, HealthCheckPeriod 1m,
  ConnectTimeout 10s), `Connect` (parse → apply non-zero limits → fail-fast
  `Ping`), `Ping`/`Health`/`Close`/`Stat`, `Exec`/`Query`/`QueryRow`
  delegation, `DBTX` interface, `ParseEmbedding` (claims the pgvector-go
  root module), and `ListMigrationFiles`/`RunMigrations` over
  `migrations/*.up.sql` (sorted, missing-dir no-op, forward-only).
- `internal/store/projects.go`: `NormalizeRemoteURL` (lowercase, strip
  scheme/userinfo/port, unify scp/URL forms, strip one `.git`), pure
  `MatchProject` priority (canonical_url → root_commit → folder_name, empty
  signals never match, first-candidate tie-break), `ProjectStore.Resolve`
  (lookup with `ORDER BY created_at, id` + `INSERT … ON CONFLICT DO NOTHING`
  + re-resolve on race) and `ResolveLocal` over `project.Fingerprint`,
  plus CRUD (`Create`/`GetByID`/`UpdateDisplayName`/`Delete`/`List`).
- `internal/store/workspaces.go`: `OfflineAfter = 90s` as the single source
  of truth shared by pure predicates (`IsOnlineAt`/`IsStaleAt`/
  `IsOnlineAtPtr`/`ExpiryAt`/`FilterOnline`) and `make_interval` SQL in
  `ListActive` (flag AND timestamp predicates) and `MarkStaleOffline`;
  `Register` upsert on `(machine_id, path)`, `Heartbeat`,
  `SetDesignatedProcessor`, CRUD.
- Tests: table-driven unit tests for normalization, matching/dedup, and
  staleness (DB-free); `TEST_POSTGRES_DSN`-gated integration tests that apply
  the real migration and prove resolve-dedup and the heartbeat-expiry
  lifecycle end-to-end.

## Why (Rationale)

- **Pool over per-call connects (Option 1 beats 3):** the daemon fleet emits
  a heartbeat every 30s plus registration bursts on restart; Aurora
  Serverless v2 wakes from zero and throttles connection storms, so a small
  warm pool (MinConns 2) with a modest ceiling (MaxConns 16) absorbs bursts
  without overwhelming a cold cluster, while MaxConnLifetime 30m rotates
  connections through failovers. The plan's own episode lore (pool exhausted
  mid-WebSocket) is the measured warning this sizing answers.
- **Interface seam for testability:** the issue mandates unit tests for dedup
  + resolution that run without live Postgres — depending on `DBTX` instead
  of `*pgxpool.Pool` makes the pure cores (`NormalizeRemoteURL`,
  `MatchProject`, `IsOnlineAt`) trivially table-testable, and the fake-`Exec`
  test proves the seam carries the migration runner too
  (`go test ./internal/store/` → all unit tests PASS, integration SKIP
  without a DSN).
- **Narrow-`Rows` reuse:** `memory.go` (issue #6) already defined the minimal
  `Rows`/`Querier` and noted pgx rows satisfy `Rows` method-for-method; typing
  `DBTX.Query` to return that same `Rows` makes `*DB` a valid `memory.Querier`
  with no adapter file and no cross-issue breakage (both suites pass
  together — verified).
- **Deterministic resolution:** priority order is a pure function of
  (signals, rows), ties break on `ORDER BY created_at, id`, and the
  `ON CONFLICT DO NOTHING` + re-resolve path collapses concurrent inserts to
  one row — the acceptance "deterministic resolution, no duplicates" holds
  under races, not just in the happy path.
- **One expiry constant, two evaluators:** `OfflineAfter` feeds both the Go
  predicates and the SQL interval, with the boundary aligned (silence == 90s
  is still online on both sides; strict `<` in SQL matches `IsStaleAt`), so
  app-side filtering and authoritative queries can never disagree by a
  second. `ListActive` requires flag AND timestamp so a missed sweeper run
  cannot resurrect stale rows.
- **pgvector-go root module only:** the `/pgx` submodule is a separate Go
  module and therefore out of scope under the two-dep constraint; nothing in
  Phase 1 needs driver-level vector registration (embeddings travel as text
  literals — see `memory.go:FormatEmbedding`), so `ParseEmbedding` is the
  minimal honest claim on the dependency, with full registration deferred to
  the memory-write path.
- **Evidence:** `go build ./...` clean, `go vet ./internal/store/` clean,
  `gofmt` clean, `go test -count=1 ./internal/store/` → 31 PASS (incl. all
  13 pre-existing memory tests), 2 integration SKIP (no DSN in this
  environment).

## Consequences

- Server/daemon agents build on `Connect` + `ProjectStore`/`WorkspaceStore`;
  a sweeper (cron/loop ownership: server agent) must call
  `MarkStaleOffline` at least every ~30s or presence relies solely on the
  `ListActive` timestamp predicate (safe but leaves `is_online` flags stale).
- `internal/store` now imports `pgx/v5` + `pgvector-go`; `go.mod` holds
  exactly the two allowed additions (plus the daemon agent's `fsnotify`,
  untouched).
- Follow-ups: transaction support in `DBTX` (Phase 2 event append +
  memory-write atomicity), designated-processor failover (>1h offline),
  folder-fallback disambiguation for colliding leaf names, `LISTEN/NOTIFY`
  pool wiring (002).

## Alternatives Rejected

- **`database/sql` + lib/pq (Option 2):** rejected — third dependency,
  weaker async/vector story, no benefit over pgx for the planned 002+
  features.
- **Per-call connects (Option 3):** rejected — reintroduces the pool
  exhaustion failure the plan documents; violates the connection-pool
  acceptance.
- **Whole-string case preservation in `NormalizeRemoteURL`:** rejected —
  hosting providers compare case-insensitively, and deterministic dedup
  (acceptance) beats the theoretical collision of case-sensitive non-git
  hosts; documented here as the known trade-off.
