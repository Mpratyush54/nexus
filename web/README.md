# Web Dashboard (Phase 3 — nexus issue #14)

Static single-page app: live agent activity feed, presence, memory explorer,
episode inspector. Vanilla HTML/CSS/JS, **no npm, no bundler, no build step**.

## Files

| File | Purpose |
|---|---|
| `index.html` | Layout: connection, project/session picker, presence, feed, memory, episodes, search |
| `app.js` | All logic: REST calls, WebSocket client, rendering |
| `style.css` | Dark theme, responsive — the ONE live stylesheet |

(`styles.css` was a dead orphan — different class names from the live DOM —
and is deleted. If you see it referenced anywhere, that reference is stale.)

## Prerequisites

A running Central Server (`internal/server/`). The WebSocket hub
(`internal/server/ws.go`) speaks the protocol below; without WS the dashboard
remains usable over REST (with 5s polling fallback).

## Serve (pick one — not implemented in-repo, by design)

```sh
# Option A: any static server (Node)
npx serve web/ -l 3000
# then open http://localhost:3000

# Option B: Python
python -m http.server 3000 --directory web/

# Option C: Go one-liner (no new files — paste into a scratch main or `go run`)
# go run -exec ... :
#   http.ListenAndServe(":3000", http.FileServer(http.Dir("./web")))
```

## Connection defaults (deploy compat, issue #130)

API base and WS URL default to **same-origin** derived from
`window.location` (`https:` → `wss:`, WS path `/ws`), so the dashboard works
behind the ALB/HTTPS without hand-editing. Stored overrides in
`localStorage` (`cm.apiBase`, `cm.wsUrl`) always win — set them once for
cross-host dev. `node --check web/app.js` gates syntax in CI.

## Use

1. §1: API base + WS URL are prefilled (same-origin) → **Login** (`POST /auth/login`, token stored in `localStorage`; XSS-readable by design for a static SPA — treat the host as trusted, prefer short server TTLs).
2. §2: enter `folder_name` (or canonical URL) → **Resolve project**, or paste a
   known `project_id`. Enter a `session_id` → **Subscribe** sends `{"type":"subscribe","project_id":…}`; the hub replies `subscribed` (shown) or `error` (surfaced in the feed).
3. §3–§4: presence dots + live feed arrive as `presence` / `event` frames
   (<1s, append-only DOM, capped at 200 nodes, never a full refresh).
4. §5: memory list via `GET /memory/search`; **Confirm / Reject / Promote**
   buttons try `POST /memory/{id}/confirm|reject|promote` (live in
   `routes_extra.go` + `promote.go`) and always apply optimistic local state +
   a WS `action` hint.
5. §6: episode cards via `GET /episodes/search`; live `episode_update` frames
   prepend/update in place. Cards expand trigger → investigation → root cause →
   fix → verification.
6. §7: one query box fans out to `/memory/search` + `/episodes/search` in parallel.

Auth: REST sends `Authorization: Bearer`; the WS socket appends `?token=`
automatically (browsers cannot set WS headers — `ws.go` accepts it). Any HTTP
401 or auth `error` frame clears the token and prompts re-login.

## Protocol reference (ws.go)

```jsonc
// Client → Server
{"type": "subscribe", "project_id": "...", "session_id": "..."}
{"type": "action", "event_type": "MESSAGE_SENT", "payload": {...}}
{"type": "presence", "status": "typing"}
// Server → Client
{"type": "event", "event": {/* store.Event */}}
{"type": "presence", "user_id": "...", "status": "online|typing|idle|offline"}
{"type": "memory_update", "item": {/* store.MemoryItem */}, "action": "proposed|confirmed|rejected"}
{"type": "episode_update", "episode": {/* store.Episode */}, "action": "opened|updated|resolved"}
{"type": "subscribed", "project_id": "...", "session_id": "..."}
{"type": "error", "message": "..."}
```

## Acceptance mapping (issue #14)

- Real-time updates without full-page refresh → §4 feed + §§5–6 in-place
  card updates; `presence`/`memory_update`/`episode_update`/`subscribed`/`error` handlers mutate
  single DOM nodes only.
- Confirm proposed memories from the UI → §5 buttons (server endpoints live —
  see `docs/decisions/2026-09-17-dashboard.md`).
- Assigned to @ParthKhandelwal537 → no code action; noted for tracking.
