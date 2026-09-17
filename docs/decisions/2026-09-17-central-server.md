# Central Server REST API — Phase 1.8 Decisions

Date: 2026-09-17
Scope: `internal/server/` (server.go, routes.go, auth.go) for nexus issue #8 / implementation-plan.md §1.8.
Status: implemented against `internal/store` interface (`MemStore`); Postgres/RDS Aurora store to follow.

## Endpoints

| Method | Route | Handler | Store call |
|---|---|---|---|
| POST | /auth/login | handleLogin | (none — stub; see below) |
| POST | /projects/resolve | handleProjectResolve | ResolveProject |
| POST | /workspaces/register | handleWorkspaceRegister | RegisterWorkspace |
| POST | /workspaces/heartbeat | handleWorkspaceHeartbeat | Heartbeat |
| GET | /workspaces/{projectId}/active | handleWorkspaceActive | GetActiveWorkspace |
| POST | /memory | handleMemoryCreate | CreateMemoryItem |
| GET | /memory/search | handleMemorySearch | SearchMemory |
| POST | /episodes | handleEpisodeCreate | CreateEpisode |
| GET | /episodes/search | handleEpisodeSearch | SearchEpisodes |
| GET | /healthz | handleHealth | (none, unauthenticated) |

All routes except `/auth/login` and `/healthz` require `Authorization: Bearer <token>`
via `requireAuth`. Failures use a single JSON error envelope:
`{"error":{"code":<status>,"message":"..."}}`.

## Decision 1 — stdlib `net/http` ServeMux, no router dependency

Why:
- Go 1.22+ (we run 1.26.1) supports method-qualified patterns (`"POST /memory"`)
  and path parameters (`/workspaces/{projectId}/active` + `r.PathValue`), which covers
  every Phase 1.8 route with zero dependencies.
- `go.mod` stays dependency-free, matching the repo's locked single-user baseline
  (`central-memory, go 1.26.1, zero deps`). The full dependency set
  (pgx/v5, pgvector-go, websocket, jwt/v5, fsnotify) lands with the Postgres/daemon
  phases, not here.
- Fewer moving parts for the daemon-lifecycle acceptance test
  (resolve → register → heartbeat → active runs over `httptest` with no framework).

Consequence: if routes later need middleware chains, path prefix groups, or OpenAPI
generation, revisit a lightweight router — but only with a measured need.

## Decision 2 — HMAC-SHA256 JWT-shaped stub instead of `golang-jwt/jwt/v5`

Why:
- Task constraint: stdlib only, do not touch `go.mod`. Adding `jwt/v5` now would
  break that contract for no functional gain in Phase 1.
- The stub mints structurally real JWTs (`base64url(header).base64url(payload).base64url(sig)`,
  HS256, `sub`/`exp`/`iat` claims) with constant-time signature comparison and expiry
  enforcement, so clients already send `Bearer` tokens and handlers already validate
  them. The migration to `jwt/v5` is mechanical: replace `Authenticator.Generate` /
  `Validate` internals with `jwt.NewWithClaims` + `jwt.ParseWithClaims` (marked with
  `TODO(jwt-v5)` in auth.go); no route changes needed.
- Key comes from `CENTRAL_MEMORY_JWT_KEY` with an explicit insecure dev fallback;
  production will source it from AWS Secrets Manager alongside the Aurora credentials.

What the stub does NOT do (explicit non-goals for v1):
- No password verification: `/auth/login` accepts any non-empty username+password.
  Real credential checks against the `users` table come with the Postgres store.
- No refresh tokens, revocation list, or key rotation — deferred to the RDS phase.

## Decision 3 — 90-second offline threshold (heartbeat every 30s)

Why:
- The daemon heartbeats every 30s (plan §1.3). A 90s window tolerates two consecutive
  missed beats (network blip, GC pause, laptop sleep) before declaring a workspace
  offline, while keeping presence fresh enough for the Phase 3 <1s live-collaboration
  goal.
- It is enforced in one place conceptually: `OfflineThreshold = 90s` in server.go,
  `MemStore.GetActiveWorkspace` filters `LastSeen > now-90s`, and handlers re-check
  via `WorkspaceIsOnline` (defense in depth so a future SQL store cannot leak stale
  presences). Unit test `TestHeartbeatOffline` pins this behavior.
- "Transition to offline" is currently a read-time derivation (stale rows read as
  offline / 404 on `/active`), not a background sweeper. A sweeper that flips
  `is_online=false` and emits `WORKSPACE_OFFLINE` events belongs to the Phase 2
  event-store work, where LISTEN/NOTIFY can broadcast the transition.

## RDS Aurora notes (not yet wired)

- `Server` depends only on the `store.Store` interface, so the Aurora/pgx store is a
  drop-in replacement for `MemStore` with no handler changes.
- Search endpoints return `{"items":[...],"count":n}` deliberately: the Postgres
  implementation will run pgvector cosine search first with full-text fallback and
  keep the same response shape.
- Memory content length (20–2000 chars) is validated in `handleMemoryCreate` to mirror
  the `CHECK` constraint in `migrations/001_initial.up.sql`, failing fast with 400
  instead of leaking a SQL error.

## Verification

- `go build ./internal/server/` — passes.
- `go vet ./internal/server/` + `go test ./internal/server/ -v` — 8 tests pass:
  login round-trip, credential rejection, auth middleware 401, full
  resolve→register→heartbeat→active lifecycle, 90s offline rule, memory CRUD +
  search, episode CRUD + search, stub tamper/expiry handling.
- `go.mod` untouched (still zero dependencies).
