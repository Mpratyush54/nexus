# ADR-014 — Web Dashboard as Static SPA with Disk-Backed Static Handler

- **ADR ID:** ADR-014-web-dashboard-static-spa
- **Date:** 2026-09-17
- **Author:** issue-#14 agent
- **Issue:** #14 Web dashboard (`web/` + `internal/server/web.go`, plan Phase 3)
- **Status:** Accepted

## Context

Issue #14 owns `web/` + exactly one new server file (`internal/server/web.go`)
plus docs. Constraints from the issue and the plan:

1. Static SPA, no build step: single `index.html` + `app.js` + `styles.css`.
2. Live activity feed over WebSocket (no full refresh), sessions+participants
   list, Memory Explorer (level × status filters, confirm/reject/promote →
   REST), Episode Inspector (search by error/file, trigger→root-cause→fix).
3. Must match existing REST + WS message shapes (plan §3.2).
4. Server static-file route ONLY in the new file — `routes.go`, `server.go`,
   `ws.go` untouchable (issues #8/#13).

## Options Considered

1. **Embed via `//go:embed` in `web.go`.**
   Pros: single binary, no asset-dir config. Cons: `go:embed` patterns
   forbid `..`, and the issue fixes `web/` at the repo root — `../../web`
   does not compile. Embedding would force moving assets under
   `internal/server/`, violating the ownership constraint. Rejected.
2. **(Chosen) Disk-backed static handler in `web.go` + exact-match allowlist.**
   `NewWebHandler(dir)` / `(s *Server).RegisterWebRoutes(mux)` serve exactly
   `/` → `index.html`, `/app.js`, `/styles.css` with pinned MIME and
   `no-store`. `GET /{$}` matches only root, so no API route is shadowed and
   there is no SPA fallback swallowing API 404s. Zero new deps (stdlib only).
3. **Full client-side routing / framework bundle.**
   Rejected: violates "no build step", adds supply-chain weight for a Phase 3
   exit-criterion demo (Alice's message visible to Bob in <1s).

## Decision

- `web/index.html`, `web/app.js`, `web/styles.css`: dependency-free SPA.
  - WS path `WS_PATH = "/ws"` is a **proposal for #13** (no `ws.go` exists
    yet — verified by glob: `internal/server/` contains only
    `server.go`, `routes.go`, `server_test.go`). Message shapes follow plan
    §3.2 verbatim both directions.
  - REST calls use only implemented endpoints with exact wire shapes:
    `POST /auth/login`, `GET /memory/search` (`project_id,q,tags,key,level,
    limit` → `{items,count}`; lowercase levels), `GET /episodes/search`
    (`project_id,q,error_pattern,file,status,episode_type,limit` →
    `{episodes,count}`), `GET /workspaces/{id}/active` → `{workspaces,
    count}`, `GET /healthz`. Status filtering on memories is client-side —
    `handleSearchMemory` accepts no `status` param.
  - Lifecycle buttons call proposed `POST /memory/{id}/confirm|reject|promote`
    (no such endpoint in #8's REST) and surface the 404 as a pending-endpoint
    notice instead of faking success — honest degradation, see follow-ups.
  - Sessions panel renders `workspaces/active` + live WS `presence` roster;
    no `GET /sessions` endpoint exists server-side yet (store #12 exists,
    server wiring does not).
- `internal/server/web.go`: `WebDir` var, `NewWebHandler`, method
  `RegisterWebRoutes` (exact `GET /{$}`, `GET /app.js`, `GET /styles.css`),
  `serveWebFile` with allowlist + pinned MIME + `no-store` + `nosniff`.

## Why (Rationale)

- **Exit-criterion mapping (plan §3.2 + Phase 3 §"Exit Criterion"):** WS
  `subscribe`/`event`/`presence`/`memory_update`/`episode_update` shapes are
  transcribed verbatim into `app.js` constants and the header comment, so
  when #13's hub lands with the same shapes the feed works with zero
  dashboard changes; `memory_update` triggers a targeted list re-fetch, never
  `location.reload()` — the "no full refresh" requirement by construction.
- **Ownership compliance:** `git status` shows only `web/*`,
  `internal/server/web.go`, `docs/` touched; `routes.go`/`server.go`
  untouched (verified by diff, see ISSUE-14).
- **Evidence:** `go build ./...` exit 0; `go vet ./internal/server/` exit 0;
  `gofmt -l` clean on owned files; JS self-review checklist in ISSUE-14
  (WS path + message types + REST params cross-checked against
  `routes.go`/`server.go`/plan §3.2).

## Consequences

- Deployments must mount `NewWebHandler` **outside** `withAuth` (browsers
  cannot send Bearer on navigation); alternatively extend `openPaths` in
  `server.go` — another owner's file, left as a follow-up.
- WS auth is undecided: the SPA passes `?token=` opportunistically; #13
  decides query-vs-subprotocol — dashboard tolerates both (works
  unauthenticated against a permissive hub).
- No browser in this environment — live rendering verified by code review
  only (checklist in ISSUE-14); a human must open the dashboard once the hub
  lands.

## Alternatives Rejected

- `go:embed` (option 1): does not compile with `..` patterns; moving assets
  breaks the `web/` ownership rule.
- Framework SPA (option 3): build step + deps for zero exit-criterion gain.
- Faking lifecycle success client-side: rejected — buttons report the real
  server answer so the missing endpoints stay visible as follow-ups.

## Status update (2026-09-18, issue #151)

- `internal/server/ws.go` NOW EXISTS and registers `/ws`; the proposal (no ws.go exists yet) note in Decision above is historical. Lifecycle endpoints (confirm/reject/promote) also exist now (routes_extra.go + promote.go).
