# ISSUE-8 — [Phase 1] Central REST API

- **Issue:** #8 — Central REST API `internal/server/server.go` + `routes.go`
  (plan §1.8)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** Wave-1 subagent
- **Scope constraint:** ONLY `internal/server/`, `docs/`, and
  `go.mod`/`go.sum` (JWT dep). Did NOT touch `internal/store/`,
  `internal/daemon`, `internal/context`, or any other package.

## What was built

| File | Contents |
|---|---|
| `internal/server/server.go` | `Store` narrow interface (9 methods, one per endpoint); server-owned `Memory`/`Episode`/`MemoryFilter`/`EpisodeFilter` JSON shapes; `Server`/`Options`/`New` (required `JWTSecret`, 24h default TTL, injectable `Now`); HS256 mint/verify via `golang-jwt/jwt/v5` (time func bound to server clock); `withAuth` middleware (open: `POST /auth/login`, `GET /healthz`); `OfflineAfter` alias + `IsOnlineAt` delegating to `store` |
| `internal/server/routes.go` | Stdlib mux table (9 API routes); handlers: login, project resolve, workspace register, heartbeat, active workspaces, memory create/search, episode create/search, health; CHECK-mirroring validation (content 20–2000, vocabularies), 201 on creates, `{"error"}` envelope, 1 MiB body cap, unknown-field rejection, `?limit` default-20/clamp-100 |
| `internal/server/server_test.go` | Mutex-guarded in-memory fake `Store` sharing the server clock; 15 tests (see below) |
| `docs/decisions/ADR-008-central-rest-api-jwt-auth.md` | Why-mandatory ADR (narrow seam, no-drift staleness, real JWT, validation at boundary) |
| `go.mod` / `go.sum` | Promoted plan-§1.9-allowed `github.com/golang-jwt/jwt/v5 v5.3.1` to direct dependency. Only dep change |

### Endpoints

- `POST /auth/login` `{username,password}` → `{token,expires_at,user_id}` (open)
- `GET /healthz` → `{status:ok}` (open, LB probe)
- `POST /projects/resolve` → upsert by origin/root-commit/folder (reuses `store.NormalizeRemoteURL` semantics via the adapter seam)
- `POST /workspaces/register` → 201 workspace (upsert on machine+path in adapter)
- `POST /workspaces/heartbeat` `{workspace_id,branch,commit_sha,is_dirty}` → refreshed workspace; revives without re-register
- `GET /workspaces/{projectID}/active` → `{workspaces,count}`, server-gated by `IsOnlineAt` (90s)
- `POST /memory` → 201 item (defaults: `project`/`fact`/`PROPOSED`/1.0)
- `GET /memory/search?project_id=&q=&tags=&key=&level=&limit=` → `{items,count}`
- `POST /episodes` → 201 episode (default `OPEN`)
- `GET /episodes/search?project_id=&q=&error_pattern=&file=&status=&episode_type=&limit=` → `{episodes,count}`

## Decisions (see ADR-008 for rationale)

1. Narrow `Store` interface, not concrete store wiring — testable without
   Postgres; Postgres adapter is a mechanical follow-up.
2. Server applies `store.IsOnlineAtPtr` itself over unfiltered lists —
   reads stay correct when the DB sweeper lags; alias (not copy) of
   `store.OfflineAfter` so drift is impossible.
3. Real JWT (plan §1.9 allow-list), stdlib mux only — no router dep, no
   throwaway test-seam auth.
4. Boundary validation mirrors the Postgres CHECKs so bad payloads 400
   instead of 500.

## Verification (2026-09-17, go1.27.0)

- `go build ./internal/server/ ./internal/store/` → exit 0
- `go vet ./internal/server/` → exit 0, no findings
- `gofmt -l internal/server/` → clean
- `go test -count=1 ./internal/server/` → **15/15 PASS**:

```text
TestLoginSuccessAndClaims, TestLoginBadCredentials401,
TestAuthMiddleware401 (missing/garbage/wrong-scheme/wrong-secret),
TestTokenExpiry401, TestOpenPathsSkipAuth, TestWorkspaceLifecycle
(register→heartbeat→active), TestOfflineAfter90sSilence
(0s online / 90s-boundary online / 91s offline / heartbeat revives),
TestProjectResolveValidation, TestHeartbeatUnknownWorkspace404,
TestMemoryRoundtrip, TestMemoryValidation (4 subcases),
TestEpisodeRoundtrip, TestEpisodeValidation,
TestSearchRequiresProjectID, TestUnknownFieldsRejected,
TestNewPanicsWithoutSecret
```

> ⚠️ `go build ./...` (whole repo) currently fails in
> `internal/daemon` — `processor.go:646: firstLine redeclared`
> (`gitops.go:116` holds the other declaration). That package is owned by
> another agent and was not touched per this issue's ownership rule; the
> failure is pre-existing relative to this change. This issue's scope
> (`internal/server/`, `internal/store/`) builds clean.

## Follow-ups (not this issue)

- Postgres-backed `Store` adapter (`ProjectStore`/`WorkspaceStore`/memory
  + episode SQL, `users` lookup with password hashing); wire
  `cmd/server/main.go` entrypoint.
- Vector ranking inside `SearchMemory` via `store.BuildSearchSQL` (currently
  text/tag/level/key filtering).
- Sweeper calling `MarkStaleOffline` ~every 30s (server reads are already
  correct without it; flags would go stale otherwise).
- Daemon agent: fix the `firstLine` redeclaration breaking `go build ./...`.
- Rate-limiting/login brute-force protection and refresh-token rotation
  (v1 mints 24h access tokens only).
