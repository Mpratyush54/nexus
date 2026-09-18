# Dashboard — Phase 3 Decisions (static SPA, vanilla WS client)

Date: 2026-09-17
Scope: `web/` (`index.html`, `app.js`, `style.css`, `README.md`) for nexus issue #14 / implementation-plan.md §Phase 3 (sessions + real-time).
Status: implemented. No Go files touched.

## Context

Phase 3 exit criterion: "Alice sends a message in a session. Bob sees it in <1s
via WebSocket. A bug episode opened by Alice's agent shows up in Bob's dashboard
in real time." The dashboard is the human-visible proof of that, covering plan
§3.2 (WS protocol), §3.3 (session vs project scoping), §3.4 (presence + activity
feed), plus the issue's Memory Explorer and Episode Inspector.

Verified before building:

- `internal/server/ws.go` **does not exist yet** — the server ships Phase 1.8
  REST only (`server.go`, `routes.go`, `auth.go`). The WS message shapes below
  therefore come from the plan (§3.2), not from code, and the client is written
  against that contract so the future hub just works.
- Existing REST shapes honored: `GET /memory/search` and `GET /episodes/search`
  return `{"items":[…],"count":n}`; auth is `Authorization: Bearer <token>`
  (JWT stub, see `2026-09-17-central-server.md`); memory content rule is 20–2000
  chars; JSON field names match `internal/store/models.go`.

## WS shape used (plan §3.2, verbatim)

```jsonc
// Client → Server
{"type": "subscribe", "project_id": "...", "session_id": "..."}
{"type": "action", "event_type": "MESSAGE_SENT", "payload": {...}}
{"type": "presence", "status": "typing"}
// Server → Client
{"type": "event", "event": {/* full event row */}}
{"type": "presence", "user_id": "...", "status": "online|typing|idle|offline"}
{"type": "memory_update", "item": {/* memory item */}, "action": "proposed|confirmed|rejected"}
{"type": "episode_update", "episode": {/* episode */}, "action": "opened|updated|resolved"}
```

`session_id` is treated as opaque: the `sessions` migration (§3.1) has not
landed, so the picker accepts any string and forwards it in `subscribe`.
Nothing breaks when real session rows appear.

## Decision 1 — static files, no build step

Why:

- The dashboard is a thin view over REST + WS; there is no bundling problem to
  solve (three files, zero imports). A build step would add Node toolchain
  requirements to a repo whose `go.mod` is deliberately dependency-free.
- Static files are servable by anything (`npx serve`, `python -m http.server`,
  one-line Go `FileServer`) and reviewable as plain diffs.
- Keeps the "do NOT touch Go files" constraint trivially satisfiable — there is
  no codegen or embed step coupling `web/` to the server.

Consequence: if the dashboard ever needs charts, routing, or offline cache,
revisit a bundler — only with a measured need.

## Decision 2 — vanilla JS, no npm dependencies

Why:

- Every requirement (fetch, WebSocket, DOM patching) is covered by platform
  APIs; a framework buys nothing at this size and wouldimport supply-chain risk
  into a security-sensitive memory product.
- Zero-install onboarding: open the served page, log in, subscribe. No
  `npm install`, no version drift between contributors.
- Small attack surface for XSS review: one `esc()` helper, no template engine.

## Decision 3 — graceful degradation while `ws.go` is missing

Why:

- The hub is the highest-risk Phase 3 dependency. A dashboard that whitescreens
  without WS proves nothing; one that works over REST today and lights up live
  tomorrow de-risks the demo.
- Implemented as: WS connect with exponential-backoff retry (1s→30s); REST
  remains fully usable while disconnected; a 5s poll of the searchable
  collections (`/memory/search`, `/episodes/search`) runs only while the socket
  is down. There is deliberately no `GET /events` polling — no such REST route
  exists, and inventing one from the client would fake a contract the server
  never promised.

## Decision 4 — optimistic memory actions (confirm/reject/promote)

Why (plan §2.8 confirmation flow):

- The server exposes `ConfirmMemory` in the store interface but **no REST route**
  for it yet (routes stop at `POST /memory` + `GET /memory/search`). Blocking
  the issue's "confirm proposed memories directly from the UI" criterion on a
  missing route would stall Phase 3 on Phase 2 work.
- Buttons therefore `POST /memory/{id}/confirm|reject|promote` best-effort
  (the natural §2.8 shape for whoever implements the route), and on 404/405
  keep optimistic local state + emit a WS `action` frame so the future hub can
  reconcile. Failures are shown inline on the card, never swallowed.

## Decision 5 — append-only feed rendering, presence derived like the server

Why:

- The <1s criterion is a rendering budget as much as a network one: each `event`
  frame prepends one capped DOM node (200 max), `memory_update`/`episode_update`
  replace a single card, `presence` updates one dot. No full-page refresh path
  exists in the code.
- Presence staleness uses the same 90s `OfflineThreshold` as `server.go` /
  `MemStore`, so the UI's green→offline flip agrees with the server's
  `/workspaces/{id}/active` 404 boundary.

## Verification

- `web/` contains exactly `index.html`, `app.js`, `style.css`, `README.md` —
  no Go files modified (confirm with `git status --porcelain`).
- Manual pass (requires server on `:8080`): login → resolve project →
  subscribe → `presence`/`event` frames render <1s; memory confirm/reject shows
  optimistic state with inline fallback note when the route 404s; search fans
  out to both search endpoints; WS-down mode still browses via REST.
- No automated tests: static assets with no runner; covered by the manual pass
  above. A headless WS round-trip test belongs with the `ws.go` server change,
  not here.

## Status update (2026-09-18, issue #151)

- `internal/server/ws.go` NOW EXISTS (full WebSocket hub + routes); the
  "does not exist yet" note in Context above is historical. The client
  shapes it was written against match the landed protocol.
