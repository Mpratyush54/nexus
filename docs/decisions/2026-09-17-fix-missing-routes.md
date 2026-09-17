# Fix Missing Routes (Code-Review #8) — Decisions

Date: 2026-09-17. Scope: `internal/server/routes.go` (one-line hook),
`internal/server/routes_extra.go` (new), `internal/server/routes_extra_test.go`
(new). `internal/store/db.go` and `internal/server/ws.go` untouched.

## Context

`registerRoutes()` in `internal/server/routes.go` mounted only the Phase 1.8
surface (auth, projects, workspaces, memory create/search, episode
create/search). The shipped clients already call a wider surface, so every
call below 404s (or 405s) today:

- CLI sessions (`cmd/nexus/session.go`): `GET /sessions?project_id=`,
  `POST /sessions`, `POST /sessions/{id}/join`.
- CLI branches (`cmd/nexus/branch.go`): `GET /branches?project_id=`,
  `POST /branches`, `POST /branches/{name}/checkout`,
  `GET /branches/diff?project_id=&target=`, `POST /branches/merge`.
- CLI memory (`cmd/nexus/memory.go`) + dashboard (`web/app.js`
  `memoryAction()`): `POST /memory/{id}/confirm`, `POST /memory/{id}/reject`
  (dashboard also calls `POST /memory/{id}/promote`, still unimplemented —
  its call site is best-effort with optimistic fallback, so it is deferred).
- Episodes: `POST /episodes/{id}/resolve` (backed by the existing
  `Store.ResolveEpisode`; the CLI has no resolve subcommand yet, the dashboard
  will grow one).

The Store methods already exist (`internal/store/sessions.go`,
`branches.go`, `store.go`); only HTTP handlers + route bindings were missing.

## Decision

Add all eleven routes with handlers in a new file
`internal/server/routes_extra.go`, wired via a single
`s.registerExtraRoutes()` line appended to `registerRoutes()`. Path shapes
follow the clients exactly (see Context); where callers disagree about
payload detail, the handler accepts the union and defaults from the JWT
subject (`X-Auth-Subject` stashed by `requireAuth`):

- `POST /sessions` takes the CLI's `{"title","project_id"}` (+ optional
  `created_by`, defaults to auth subject) and returns the session (201).
- `POST /sessions/{id}/join` takes the CLI's `{}` (+ optional
  `user_id`/`agent_id`/`role`, user defaults to auth subject, role defaults
  to `MEMBER` via `normalizeRole`) and returns the participant (200).
- `POST /branches` takes the CLI's `{"name","project_id","from"}` (+ optional
  `visibility`/`owner_id`); blank `from` means main (resolved via
  `EnsureMainBranch`), otherwise `from` is a sibling branch name (201,
  409 on duplicate).
- `POST /branches/{name}/checkout` accepts `{name}` as a branch ID first,
  then as a name scoped by `?project_id=`; it is resolve-only (200) — the
  server keeps no per-client "current branch" state.
- `POST /memory/{id}/confirm` delegates to `Store.ConfirmMemory` and returns
  the updated item (200).
- `POST /memory/{id}/reject`: no `RejectMemory` exists on `store.Store`, so
  the handler probes for an optional
  `RejectMemory(ctx,id,rejectedBy) error` interface (forward-compat) and
  otherwise flips the fetched item to `REJECTED` (200).
- `POST /episodes/{id}/resolve` takes optional
  `{"resolution","verification"}` (+ optional `resolved_by`, defaults to auth
  subject), delegates to `Store.ResolveEpisode`, returns the episode (200).
- `GET /branches/diff` and `POST /branches/merge` resolve both branch
  endpoints (404 on unknown) and return an explicit stub shape with a `note`
  field and empty change/conflict lists — never fabricated data.

## Alternatives

- **Edit `routes.go` in place for everything:** rejected. `routes.go` is the
  Phase 1.8 hot file; a separate `routes_extra.go` keeps the review diff to
  one hook line and avoids merge clashes.
- **Add `RejectMemory` / `Diff` / `Merge` to the Store interface + implement
  in `store.go`:** rejected for this fix. The task constrains edits to
  `internal/server/*`; changing the interface would force touching
  `MemStore`, `PostgresStore`, and every mock. Reject is covered by the
  fallback below; diff/merge need content enumeration that does not exist.
- **Return 501 for diff/merge until the store layer lands:** rejected. The
  clients treat 404/405/501-adjacent failures as "not implemented" and the
  review asked for the routes to exist; a 200 with an explicit `note`
  keeps the contract stable while being honest about the stub.
- **Reject via `ConfirmMemory`-style new method on MemStore only:**
  rejected — same interface-touch problem, plus a MemStore-only method would
  silently diverge from Postgres behavior.

## Why

- **Client-shape-first:** the CLI parses `{"items","count"}` lists and posts
  fixed payloads (`{}` for join/checkout/confirm/reject,
  `{"source","target","project_id"}` for merge). Matching those shapes
  exactly removes the 404 class without touching any shipped client.
- **Auth + error conventions preserved:** all new routes go through
  `requireAuth` (401 envelope) and reuse `writeJSON`/`writeError`/`decodeJSON`,
  so `project_id`-missing is 400 (like `/memory/search`), unknown IDs are
  404, duplicates are 409, and strict bodies reject unknown fields — the same
  contract Phase 1.8 clients already handle.
- **Reject fallback is sound for tests, labeled for production:**
  `MemStore.GetMemoryItem` returns the live map pointer, so the status flip
  persists (asserted in `TestMemoryConfirmReject`); `PostgresStore` returns a
  scanned copy, so the doc + code comment mark it as needing a real
  `RejectMemory`. The interface probe means that method slots in with zero
  handler changes.
- **Diff/merge honesty:** `internal/branches` (issue #18) computes diff/merge
  from snapshots, but no Store method enumerates branch contents, so the HTTP
  layer cannot build snapshots. Empty lists + `note` document the seam
  instead of inventing conflicts/merges.

## Consequences

- `go build ./...` and `go test ./internal/server/` stay green; new file
  `routes_extra_test.go` covers sessions create/list/join, branches
  list/fork/checkout/diff/merge, memory confirm/reject (including reject
  persistence on MemStore), and episode resolve, plus 400/401/404/409 paths.
- Follow-ups owed (not in this fix): real `RejectMemory` on the Store
  interface (+ Postgres SQL) to make reject durable in production;
  branch-content enumeration so diff can call `internal/branches.Diff` and
  merge can apply `internal/branches.MergeResult` in the store layer;
  `POST /memory/{id}/promote` for the dashboard (currently optimistic-only);
  a `nexus episode resolve` CLI subcommand to exercise the new route.
- `GET /branches` auto-creates `main` via `EnsureMainBranch` (after a
  `GetProject` 404 check), so fresh projects list one branch — a deliberate
  read-with-write documented here; `GET /sessions` does not auto-create
  anything.
