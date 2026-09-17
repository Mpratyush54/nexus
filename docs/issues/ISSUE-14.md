# ISSUE-14 — Web Dashboard (Static SPA + Static-File Route)

- **Issue:** #14 — Web dashboard `web/` (plan Phase 3, §3.2 protocol)
- **Status:** Done (implementation + docs; awaiting merge; browser check pending — no browser in this env)
- **Assignee:** issue-14 agent
- **Scope constraint:** ONLY `web/` + `internal/server/web.go` + `docs/`.
  Did NOT touch `internal/server/routes.go`, `server.go`, `server_test.go`,
  or any other package.
- **Plan refs:** `implementation-plan.md` Phase 3 exit criterion ("Alice sends
  a message … Bob sees it in <1s via WebSocket … bug episode … in real
  time") + §3.2 WebSocket protocol; read `internal/server/*.go` first for
  the exact REST shapes reused here.

## What was built

| File | Contents |
|---|---|
| `web/index.html` | Static SPA shell: topbar (login `POST /auth/login`, WS status dot), toolbar (project/session inputs, Connect, Health), 4 cards (feed, sessions+participants, Memory Explorer, Episode Inspector); loads `/styles.css` + `/app.js`, no build step |
| `web/app.js` | Vanilla JS: `api()` (Bearer + `{error}` envelope), `WS_PATH="/ws"` (proposed, #13 owns `ws.go`), §3.2 message handling (`event`/`presence`/`memory_update`/`episode_update` → prepend-only feed, targeted re-fetch, presence roster), `GET /workspaces/{id}/active` roster, memory search (`?level=` server-side, status client-side — no status param exists), lifecycle buttons → `POST /memory/{id}/confirm\|reject\|promote` with honest 404 notice, episode search (`q`/`error_pattern`/`file`/`status`) + trigger→root-cause→fix detail |
| `web/styles.css` | Dependency-free dark theme, responsive grid, feed/presence/item/detail styles |
| `internal/server/web.go` | `WebDir` var, `NewWebHandler(dir)`, `(s *Server).RegisterWebRoutes(mux)` (`GET /{$}` + `GET /app.js` + `GET /styles.css` exact — no catch-all, no API shadowing), `serveWebFile` (exact allowlist, pinned MIME, `no-store`, `nosniff`, 404 otherwise); stdlib only |
| `docs/decisions/ADR-014-web-dashboard-static-spa.md` | Why-mandatory ADR (embed-vs-handler, verbatim-shape rationale, auth-mount consequence) |
| `docs/issues/ISSUE-14.md` | This file |

## Decisions (see ADR-014 for rationale)

1. Disk-backed static handler, not `go:embed` (`..` patterns don't compile;
   `web/` must stay at root) — the issue allows either.
2. WS path `/ws` is a proposal: **no `ws.go` exists yet** (`internal/server/`
   = `server.go` + `routes.go` + `server_test.go`); §3.2 shapes transcribed
   verbatim so the SPA works unchanged when #13's hub lands.
3. Only implemented REST endpoints are called with exact wire shapes; memory
   lifecycle endpoints don't exist server-side → buttons surface the real
   404 instead of faking success.
4. Static mount must live outside `withAuth` (browsers can't Bearer-navigate);
   extending `openPaths` needs `server.go` — another owner's file, follow-up.

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/server/` → exit 0
- `gofmt -l` on owned files → clean
- Ownership: diff touches only `web/*`, `internal/server/web.go`, `docs/`
  (see Follow-ups for the `git status` evidence command)
- **JS self-review checklist (no browser in this env):**
  - [x] `WS_PATH = "/ws"` single constant; subscribe sends exactly
        `{type:"subscribe",project_id,session_id?}` (§3.2)
  - [x] Inbound switch handles exactly `event` (+`event`), `presence`
        (+`user_id`,`status`), `memory_update` (+`item`,`action`),
        `episode_update` (+`episode`,`action`); outbound presence sends
        exactly `{type:"presence",status}` (§3.2)
  - [x] `POST /auth/login {username,password}` → `{token,user_id}` (server.go
        `loginRequest`/`loginResponse`)
  - [x] `GET /memory/search?project_id=&q=&level=&limit=` → `{items,count}`;
        levels lowercase (`organization|project|personal|session`); status
        filtered client-side (no server param — routes.go `handleSearchMemory`)
  - [x] `GET /episodes/search?project_id=&q=&error_pattern=&file=&status=`
        → `{episodes,count}` (routes.go `handleSearchEpisodes`)
  - [x] `GET /workspaces/{id}/active` → `{workspaces,count}` (routes.go
        `handleActiveWorkspaces`); `GET /healthz` → `{status:"ok"}`
  - [x] `index.html` references `/styles.css` + `/app.js`; `web.go` serves
        exactly those URL paths with matching MIME
  - [x] No `location.reload()` anywhere; `memory_update` → filtered re-fetch
  - [x] No new `go.mod` deps (stdlib only)

## Follow-ups (not this issue)

- #13 hub: implement `ws.go` on `/ws` (or update `WS_PATH`) with §3.2 shapes;
  decide WS auth (`?token=` proposed) — dashboard tolerates either.
- Server owner: mount `NewWebHandler` outside `withAuth` (or extend
  `openPaths` in `server.go`, untouchable here) so browsers load `/`.
- Server/store owner: `POST /memory/{id}/confirm|reject|promote` +
  `GET /sessions`-family endpoints (store #12 exists; server wiring doesn't).
- Human with a browser: open `/`, login, connect live, confirm <1s fanout +
  episode real-time update once the hub lands.
