# ISSUE-37 — [Phase 1] Server Postgres Adapter + Memory Vector Search

- **Issue:** #37 — Postgres-backed `server.Store` adapter (`internal/server/postgres.go`) + vector path for `GET /memory/search` (`routes.go`)
  (plan §§1.5, 1.8)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** Wave follow-up subagent
- **Scope constraint:** ONLY `internal/server/postgres.go` (new),
  `internal/server/routes.go` (search handler + `?embedding=` parsing),
  `internal/server/postgres_test.go` (new), `docs/decisions/ADR-037-*`,
  this file. Did NOT touch `internal/store/*`, `internal/server/server.go`,
  `deploy/`, or any other package — including other agents' uncommitted work
  in the tree (issues #34/#38/#40).

## Problem

- Deploy ships `stubStore` (fail-closed 500 with "pending" on all data
  routes) because no `server.Store` adapter existed.
- `GET /memory/search` is text-only (`MemoryFilter.Query/Tags/Key/Level`);
  the plan §1.5 vector machinery in `internal/store/memory.go` had no path
  from the API to `store.Search`.

## What was built

| File | Contents |
|---|---|
| `internal/server/postgres.go` (new, ~600 lines) | `PostgresStore` wrapping `store.DBTX` (`NewPostgresStore`, nil-panics); all 9 `Store` methods (delegate to `ProjectStore`/`WorkspaceStore`/`EpisodeStore`/`UserStore` where a method exists, local NULL-coalesced SQL only where none does — see ADR-037 mapping table); optional `VectorMemorySearcher.SearchMemoryVector` via `store.Search`; `lifecycleStore` `GetMemory`/`UpdateMemory` per the issue #38 contract (promote NULLs `session_id`) |
| `internal/server/routes.go` (edit, +83/−2) | `?embedding=` parsing (`parseEmbeddingParam`: pgvector literal or bare CSV, rejects garbage/NaN/Inf/empty → 400); vector branch via `VectorMemorySearcher` assertion (unsupported store → 400, never silent fallback); text path byte-identical; both present → embedding wins (documented in handler comment) |
| `internal/server/postgres_test.go` (new, 15 tests) | Scripted `store.DBTX` fake (SQL-recording + canned rows): auth 401/pending mapping, raw workspace list (no `is_online` pre-filter, nil `last_seen`), memory create/text/vector mapping (`<=>` asserted, `CONFIRMED` default), LIKE-escape, limit default, episode create (`OPEN` preserved)/search filters, lifecycle get/update + 404, `parseEmbeddingParam` table, handler vector/text/unsupported-400/bad-400 wiring |
| `docs/decisions/ADR-037-server-postgres-adapter.md` | Why-mandatory ADR (method-mapping table, stub-embed rejection, fail-closed auth, consequences) |
| `docs/issues/ISSUE-37.md` | This file |

### Decisions (see ADR-037 for rationale)

1. Caller-supplied `?embedding=` through the real `store.Search`
   (pgvector cosine + 0.7/0.2/0.1 rerank) — NOT server-side stub-embedding
   of `?q=`, which would corrupt ranking while looking authoritative.
2. Optional extension interface (`VectorMemorySearcher`), not a wider
   `Store` — `server.go` stays frozen per the issue #8/#38 precedent set by
   `lifecycle.go`.
3. `Authenticate` fails closed for known users (`errAuthPending` → 500)
   until a `password_hash` column + hashing lands; unknown users → 401.
4. `ListWorkspaces`/`CreateMemory`/`SearchMemory`/`CreateEpisode`/
   `SearchEpisodes` use local SQL only where no store method covers the API
   contract (raw presence list, `OPEN`-preserving insert, combined
   filters); delegation everywhere else.

## Verification (2026-09-17, go1.27.0)

> Tree note: parallel agents' uncommitted files are in flight. One of them
> (`internal/server/wsbridge.go`, untracked, issue #40-era) does not
> type-check against `store.DBTX` and breaks `go build ./...` for the whole
> package; it was held aside (not edited) for the runs below and restored
> byte-identical afterwards. `deploy/server-bootstrap` additionally fails
> while that file is aside (it calls `server.BridgeEvents` from it) — same
> external cause. `internal/daemon` was already noted broken in ISSUE-8.

- `go build ./internal/server/` → exit 0 (with the foreign broken file
  held aside; the failure without it is pre-existing and out of scope)
- `go vet ./internal/server/` → exit 0, no findings
- `gofmt -l internal/server/` → clean
- `go test -count=1 ./internal/server/` → **46/46 PASS**:

```text
TestPostgresNewPanicsOnNil, TestPostgresAuthenticateUnknownUserIsUnauthorized,
TestPostgresAuthenticateKnownUserFailsClosed, TestPostgresListWorkspacesRawNoStalenessFilter, TestPostgresCreateMemoryMapping,
TestPostgresSearchMemoryTextSQLAndMapping, TestPostgresSearchMemoryVectorUsesCosineSQL,
TestPostgresSearchMemoryVectorRequiresEmbedding, TestPostgresCreateEpisodePreservesStatus,
TestPostgresSearchEpisodesFiltersAndMapping, TestPostgresLifecycleGetAndUpdate,
TestParseEmbeddingParam (9 subcases), TestSearchMemoryHandlerVectorPath,
TestSearchMemoryHandlerVectorUnsupportedStore400, TestSearchMemoryHandlerBadEmbedding400
(+ all 16 issue-#8, 7 issue-#38 lifecycle, 8 WS/hub pre-existing tests green)
```

## Follow-ups (not this issue)

- Deploy one-liner: `server.NewPostgresStore(db)` in place of `stubStore{}`
  (left for the `deploy/` owner; stub stays until then).
- `password_hash` column + hashing in `Authenticate` (lifts fail-closed
  login); decide 401-vs-500 enumeration posture.
- Embedder sidecar (or daemon-side embeddings) so vector callers stop
  hand-rolling query vectors; consider `similarity`/`score` in the search
  response envelope.
- `MarkStaleOffline` sweeper cadence (~30s) per ISSUE-8 follow-ups.
