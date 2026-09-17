# ADR-008 — Central REST API with JWT Auth and Narrow Store Seam

- **ADR ID:** ADR-008-central-rest-api-jwt-auth
- **Date:** 2026-09-17
- **Author:** issue-#8 agent
- **Issue:** #8 Central REST API (`internal/server/` + routes, plan §1.8)
- **Status:** Accepted

## Context

Issue #8 owns `internal/server/` (plan §1.8: `POST /projects/resolve`,
`POST /workspaces/register`, `POST /workspaces/heartbeat`,
`GET /workspaces/:projectId/active`, `POST /memory`, `GET /memory/search`,
`POST /episodes`, `GET /episodes/search`, plus JWT login per plan §1.8
"Auth: JWT tokens (simple username/password login for v1)") while three
constraints collide:

1. **Parallel ownership** — `internal/store/*.go` belongs to issues #1/#2/#6
   (other agents). I must not edit it, yet my handlers need projects,
   workspaces, staleness, and memory shapes from it.
2. **No Postgres yet for this issue** — there is no `users.go`,
   `episodes.go`, or memory-insert in the store package, so login, memory
   creation, and episodes cannot be backed by real queries today.
3. **Presence correctness** — plan §1.3 says "server marks offline after 90s
   silence"; the store documents the same rule (`OfflineAfter`,
   `IsOnlineAt`/`IsStaleAt`, `ListActive` flag-AND-timestamp predicates).
   The server must agree with that definition exactly, even when the DB
   sweeper (`MarkStaleOffline`) has not run.

## Options Considered

1. **Concrete store wiring: import `ProjectStore`/`WorkspaceStore` directly.**
   Pros: real DB behavior now. Cons: forces a live Postgres for every test;
   memory/episode/user paths have no store methods to call, so the seam
   would be half-concrete anyway; couples server compile to other agents'
   in-flight files.
2. **(Chosen) Narrow `Store` interface (one method per endpoint) + server-side
   staleness gate.**
   `Store` declares `Authenticate / ResolveProject / RegisterWorkspace /
   HeartbeatWorkspace / ListWorkspaces / CreateMemory / SearchMemory /
   CreateEpisode / SearchEpisodes`. Project/workspace signatures reuse
   `store.ProjectParams`, `store.WorkspaceParams`, `store.Project`,
   `store.Workspace` (read-only import, zero edits). `ListWorkspaces`
   returns the project's workspaces unfiltered and the server applies
   `store.IsOnlineAtPtr` itself — the shared predicate, so definitions
   cannot drift. Tests substitute a mutex-guarded in-memory fake with a
   controllable clock.
3. **Skip real JWT for a test seam (e.g. opaque bearer map).**
   Rejected: plan §1.9 explicitly allow-lists `github.com/golang-jwt/jwt/v5`,
   and opaque tokens would need a second auth design before Phase 2. The
   only new dependency is the allowed JWT module; everything else is
   stdlib `net/http` (Go 1.22+ method+template mux, no router dep).

## Decision

- `internal/server/server.go`: `Store` interface, server-owned JSON shapes
  (`Memory`, `Episode`, filters), `Server`/`Options` (required `JWTSecret`,
  default 24h TTL, injectable `Now`), HS256 mint/verify
  (`jwt.WithTimeFunc` bound to the server clock), `withAuth` middleware
  (open: `POST /auth/login`, `GET /healthz`; everything else 401 on
  missing/bad/expired token), `OfflineAfter` alias + `IsOnlineAt`
  delegating to the store package.
- `internal/server/routes.go`: stdlib mux table, handlers with
  boundary validation mirroring the Postgres CHECKs (content 20–2000,
  level/scope/status/episode_type vocabularies), 201 on creates,
  `{"error": ...}` envelope, 1 MiB body cap, unknown-field rejection,
  `?limit` default-20/clamp-100.
- `go.mod`/`go.sum`: `github.com/golang-jwt/jwt/v5 v5.3.1` promoted to a
  direct dependency (plan §1.9 allow-list). No other deps.

## Why (Rationale)

- **Seam over concrete stores:** the fake proves each handler with zero
  Postgres — including the 90s offline transition, where the fake
  deliberately returns *unfiltered* workspaces so exclusion from `/active`
  can only come from the server's `IsOnlineAt` gate. The required
  register→heartbeat→active lifecycle plus boundary (90s online / 91s
  offline) plus heartbeat-revive are all asserted in
  `TestWorkspaceLifecycle` / `TestOfflineAfter90sSilence`.
- **No staleness drift by construction:** `OfflineAfter` is an alias of
  `store.OfflineAfter` and `IsOnlineAt` delegates to
  `store.IsOnlineAtPtr` — there is no second literal or second predicate
  to diverge, matching the store's dual-predicate reasoning (flag AND
  timestamp) from the read side.
- **JWT, not a seam:** `TestTokenExpiry401` advances the server clock past
  the TTL and asserts 401, proving expiry is evaluated against server time
  (restart-safe, sweeper-independent). `TestAuthMiddleware401` covers
  missing/garbage/wrong-scheme/wrong-secret.
- **Evidence:** `go build ./internal/server/ ./internal/store/` exit 0,
  `go vet ./internal/server/` exit 0, `gofmt -l internal/server/` clean,
  `go test -count=1 ./internal/server/` **15/15 PASS** (full output in
  `docs/issues/ISSUE-8.md`).

## Consequences

- A Postgres-backed `Store` adapter (mapping each method to
  `ProjectStore`/`WorkspaceStore`/memory/episode SQL + a `users` lookup)
  is a follow-up; the interface is one-method-per-endpoint so the mapping
  is mechanical.
- `GET /memory/search` is currently text/tag/level/ key filtering, not
  pgvector cosine search — vector ranking wires into `SearchMemory` when
  the adapter lands (plan §1.5 query already exists in
  `store.BuildSearchSQL`).
- Passwords are verified by the `Store` (`Authenticate`); hashing policy
  (bcrypt/argon2) belongs to the users-table follow-up, not this package.
- Pre-existing `go build ./...` failure in `internal/daemon`
  (`processor.go:646: firstLine redeclared`, also in `gitops.go:116`) is
  another agent's package — untouched per ownership, reported in ISSUE-8.

## Alternatives Rejected

- Concrete store wiring (option 1): untestable without Postgres and
  incomplete (no user/episode/memory-write store methods exist yet).
- Opaque test-seam tokens (option 3): plan §1.9 already permits real JWT;
  a placeholder auth would be thrown away in Phase 2.
- Client-side `limit` unbounded: rejected — default 20 / clamp 100 mirrors
  `store.DefaultSearchLimit`/`MaxSearchLimit`.
