# ADR-037 — Server Postgres Adapter + Vector Search Path

- **ADR ID:** ADR-037-server-postgres-adapter
- **Date:** 2026-09-17
- **Author:** Wave follow-up subagent
- **Issue:** #37 Postgres-backed `server.Store` adapter; vector path for `GET /memory/search`
- **Status:** Accepted

## Context

Deploy (`deploy/server-bootstrap/main.go`) ships a `stubStore` that fails
closed on every data route, and `GET /memory/search` is text-only: the
narrow `Store` seam (`internal/server/server.go`, frozen since issue #8)
has no vector path, while `internal/store` already owns the full plan §1.5
machinery (`SearchQuery`, `BuildSearchSQL`, `Search` + 0.7/0.2/0.1 rerank in
`memory.go`). Parallel-agent ownership forbids editing `internal/store/*`,
and issue #38 froze `server.go` (its `lifecycle.go` adds handlers via an
optional extension interface instead). Uncommitted work in the tree also
includes `internal/store/users.go` (issue #34, `UserStore`) and
`internal/server/lifecycle.go` (issue #38, `lifecycleStore` extension the
"future Postgres adapter" is instructed to implement).

## Options Considered

1. **Widen `Store`/`MemoryFilter` in `server.go`** (add `Embedding` to the
   filter or a 10th method) — rejected: violates both the issue #8 freeze
   and the issue #38 precedent; forces every fake to change.
2. **Server-side stub-embed of `?q=`** (hash text into a fake vector so the
   text path "becomes" vector search) — rejected: deterministic fake
   vectors would corrupt cosine ranking while looking authoritative; IR
   quality would be unverifiable and misleading.
3. **Caller-supplied `?embedding=` routed through `store.Search`, behind an
   optional extension interface** — chosen: real pgvector ranking, zero
   changes to `server.go`/`internal/store/*`, text path untouched, old
   stores fail closed with an explicit 400 instead of silent text results.

## Decision

- NEW `internal/server/postgres.go`: `PostgresStore` wrapping any
  `store.DBTX` (`*store.DB` satisfies it; tests inject scripted fakes),
  implementing all 9 `Store` methods by delegation where a store method
  exists and by local SQL only where none does (table below). Plus two
  optional extensions: `VectorMemorySearcher.SearchMemoryVector` (vector
  path via `store.Search`) and `lifecycleStore.GetMemory/UpdateMemory`
  (the exact SQL semantics `lifecycle.go` prescribes, including NULLing
  `session_id` on promote).
- `internal/server/routes.go` only: `handleSearchMemory` accepts optional
  `?embedding=` (`[0.1,0.2]` pgvector literal or bare `0.1,0.2`;
  `parseEmbeddingParam` rejects garbage/NaN/Inf/empty with 400). Embedding
  present → type-assert `VectorMemorySearcher` (unsupported store → 400,
  never silent fallback); absent → byte-identical text path. Both present →
  embedding wins, `?q=` ignored (documented in the handler comment).
- NEW `internal/server/postgres_test.go`: 15 DB-free tests (scripted
  `store.DBTX` fake records SQL + returns canned rows; handler tests prove
  vector/text routing and the 400s).

Method mapping (`D` = delegates to existing store type, `S` = local SQL
because no store method covers the contract):

| Store method | Route | Why |
|---|---|---|
| `Authenticate` → `UserStore.GetByUsername` (D) | login | Unknown user → `ErrUnauthorized` (401). Known user → fail-closed `errAuthPending` (500): migration 001 has no credential column, so success must be impossible until the `password_hash` follow-up. Never pretends a comparison happened. |
| `ResolveProject` → `ProjectStore.Resolve` (D) | resolve | Exact plan §1.2 upsert. |
| `RegisterWorkspace` → `WorkspaceStore.Register` (D) | register | Upsert on machine+path. |
| `HeartbeatWorkspace` → `WorkspaceStore.Heartbeat` (D) | heartbeat | Revive-without-register preserved. |
| `ListWorkspaces` (S) | active | No store method returns an UNFILTERED per-project list (`ListActive` pre-filters `is_online`+`last_seen`, which would hide rows the server-side `IsOnlineAt` 90s-boundary gate must see — the same contract `fakeStore` proves). Raw `SELECT … WHERE project_id=$1`. |
| `CreateMemory` (S) | POST /memory | No store create method exists. `INSERT … RETURNING id, created_at`. |
| `SearchMemory` (S) | text search | No store text-search method exists. `ILIKE` (literally escaped, `ESCAPE '\'`) + `tags @>` (contains-all, matches fake semantics) + key/level + clamped `LIMIT` (reuses `defaultSearchLimit`/`maxSearchLimit`). |
| `SearchMemoryVector` → `store.Search` (D) | vector search | Real §1.5 path: `BuildSearchSQL` guards + rerank. `Status` defaults to `CONFIRMED` (the SQL guard, not selected back), `Source` to `""` (not selected by `BuildSearchSQL`) — documented limits. |
| `CreateEpisode` (S) | POST /episodes | `EpisodeStore.Create` forces `RESOLVED` and drops tags; the API defaults to `OPEN` and round-trips tags. Direct `INSERT … RETURNING` is the only fidelity-preserving route. |
| `SearchEpisodes` (S) | episode search | No single store method covers the status/type/`=ANY` patterns+file/`ILIKE`/`LIMIT` combination (`ByErrorPattern`/`ByFile`/`Similar` are single-purpose). |
| `GetMemory`/`UpdateMemory` (S) | lifecycle | Implements the issue #38 extension per its quoted SQL; promote NULLs `session_id` exactly when level flips to `project` (ADR-012). |

## Why (Rationale)

This option wins because it is the only one that (a) ships a real vector
path instead of a convincing fake (option 2 would silently degrade recall
with no measurable signal), (b) respects both freezes (`server.go`,
`internal/store/*`) following the in-tree `lifecycle.go` precedent that
solved the identical constraint, and (c) keeps every security-relevant
unknown fail-closed: unsupported vector stores 400, unverifiable passwords
500, unparseable embeddings 400. Measured evidence: `go build
./internal/server/` exit 0, `go vet ./internal/server/` clean, `gofmt`
clean, `go test -count=1 ./internal/server/` **46/46 PASS** (16 pre-existing
issue #8 + 7 issue #38 lifecycle + 8 WS/hub + 15 new adapter tests), all
DB-free. `internal/store/*` untouched (`git status` shows no modifications
there from this issue).

## Consequences

- Deploy can swap `stubStore{}` for `server.NewPostgresStore(db)` in a
  one-line follow-up (kept out of this issue: `deploy/` is owned by issue
  #20/#40 work currently in flight); `/readyz` + migration-on-boot are
  unaffected.
- Login stays fail-closed until the follow-up lands a `password_hash`
  column (+ hashing, e.g. bcrypt/argon2) and flips `Authenticate` to a real
  comparison; user enumeration via 401-vs-500 is accepted and documented
  until then.
- Vector callers must supply embeddings (1536-d per migration 001); the
  server never embeds. A future embedder sidecar would post the same param.
- No new dependencies, no migrations, no `server.go`/`internal/store/*`
  edits.

## Alternatives Rejected

- Widening the frozen seam: breaks the issue #8/#38 ownership contract for
  zero functional gain (extension interfaces compose identically).
- Stub-embedding `?q=`: unmeasurable ranking corruption disguised as a
  feature; rejected on truthfulness grounds.
- Reusing `EpisodeStore.Create` / `WorkspaceStore.ListActive` where their
  contracts contradict the API (forced `RESOLVED`, pre-filtered presence):
  fidelity loss at the seam; direct SQL is smaller and honest.
