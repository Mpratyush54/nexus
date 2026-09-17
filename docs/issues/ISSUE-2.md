# ISSUE-2 — [Phase 1] Storage Layer & Connection Pool

- **Status:** Done
- **Scope:** `internal/store/` (`db.go`, `projects.go`, `workspaces.go`,
  `db_test.go`, `projects_test.go`, `workspaces_test.go`,
  `integration_test.go`) + `docs/` only. `migrations/`,
  `internal/daemon`, `internal/context`, `internal/project`,
  `internal/scan` untouched. `go.mod`/`go.sum` changed solely via `go get`
  for the two allowed deps.
- **Plan ref:** `implementation-plan.md` §§1.1 (schema), 1.2 (resolver), 1.8
  (server endpoints' store backing), 1.9 (deps)

## What was built

- `internal/store/db.go` — `Config`/`DefaultConfig` pool limits (16/2,
  30m/5m/1m/10s), `Connect` with fail-fast `Ping`, `Health` (`Ping` +
  `SELECT 1`), `Close`/`Stat`, `Exec`/`Query`/`QueryRow` delegation, `DBTX`
  interface seam, `ParseEmbedding` (pgvector-go read-path parser),
  `ListMigrationFiles` + `RunMigrations` (sorted `*.up.sql`, missing-dir
  no-op, forward-only). `*DB` satisfies both `DBTX` and memory.go's
  `Querier` — no adapter needed (pgx rows satisfy the narrow `Rows`).
- `internal/store/projects.go` — `NormalizeRemoteURL` (24-case table:
  scp/URL forms, userinfo/port stripping, case folding, `.git`/slash
  trimming, Windows paths), pure `MatchProject` priority chain
  (canonical_url → root_commit → folder_name, empty never matches, stable
  first-candidate tie-break), `ProjectStore.Resolve` (ordered candidate
  lookup + `ON CONFLICT DO NOTHING` insert + re-resolve: no duplicates under
  races), `ResolveLocal` reusing `project.Fingerprint`, full CRUD.
- `internal/store/workspaces.go` — `OfflineAfter = 90s` single source of
  truth; pure `IsOnlineAt`/`IsStaleAt`/`IsOnlineAtPtr`/`ExpiryAt`/
  `FilterOnline`; `Register` upsert on `(machine_id, path)`, `Heartbeat`
  (revives without re-register), `ListActive` (flag AND timestamp
  predicates), `MarkStaleOffline` (strict `<`, boundary-aligned with the Go
  predicates), `SetDesignatedProcessor`, CRUD.
- Tests — 18 DB-free unit tests (normalizer, matcher incl. dedup
  determinism, staleness incl. the exact-90s boundary, migration runner via
  fake `DBTX`, config, embedding round-trip); 2 integration tests gated on
  `TEST_POSTGRES_DSN` that apply the real `migrations/001_initial.up.sql`
  and prove resolve-dedup (url/root/folder re-resolves → same ID) and the
  full heartbeat-expiry lifecycle (register → re-register upsert →
  heartbeat → active → 5-min silence excluded pre-sweep → sweep marks
  offline → heartbeat revives).
- Deps added (only these): `github.com/jackc/pgx/v5 v5.11.0`,
  `github.com/pgvector/pgvector-go v0.4.1`.
- `docs/decisions/ADR-002-storage-layer-connection-pool.md` — full ADR.
- `docs/issues/ISSUE-2.md` — this file.

## Verification

- `go build ./...`: **clean** (whole repo, incl. parallel agents' packages).
- `go vet ./internal/store/`: **clean, no findings**.
- `gofmt -l internal/store/`: **clean**.
- `go test -count=1 ./internal/store/`: **31 PASS** (18 new unit + 13
  pre-existing memory tests, proving no cross-issue breakage), **2 SKIP**
  (`TestIntegration_ProjectResolveDedup`,
  `TestIntegration_WorkspaceLifecycle` — no `TEST_POSTGRES_DSN` in this
  environment; they compile and run where a Postgres 16 + pgvector instance
  is available).
- `go test -race`: **not runnable here** — the environment's MinGW gcc
  lacks 64-bit support (`cc1.exe: sorry, unimplemented: 64-bit mode not
  compiled in`), so cgo-based race builds fail for any package. Re-run on a
  machine with a working 64-bit toolchain (follow-up below).

## Follow-ups

- Run the gated integration tests against a real Postgres 16 + pgvector
  instance (`TEST_POSTGRES_DSN=... go test ./internal/store/`) and `go test
  -race` with a 64-bit gcc — CI/integrator owned.
- Sweeper ownership: something (server loop/cron) must call
  `MarkStaleOffline` every ~30s; until then `is_online` flags go stale
  (reads via `ListActive` stay correct).
- `DBTX` transaction support for Phase 2 (event append + memory-write
  atomicity); scripted-rows fake for store-method unit tests.
- Folder-fallback disambiguation when colliding leaf names (e.g. two
  `api/` dirs) resolve to the first row — confirmation/choice flow.
- Designated-processor failover when the owner is offline >1h (plan §6.3).
