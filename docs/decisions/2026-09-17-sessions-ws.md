# Sessions + WebSocket Hub — Phase 3 Decisions

Date: 2026-09-17
Scope: `migrations/003_sessions.*`, `internal/store/sessions.go`,
`internal/server/ws.go` for nexus issues #12 (Session Layer & Scoping) and
#13 (WebSocket Hub, Fan-Out & Presence) / implementation-plan.md §3.
Status: implemented against `MemStore` (+ `PostgresStore` SQL paths, no live
DB in CI); hub is stdlib-only and covered by in-memory fan-out tests.

## Decision 1 — SQL design (003_sessions)

**Surrogate PK on `session_participants`, not `PRIMARY KEY(session_id, user_id)`.**
The plan's sketch (`PRIMARY KEY (session_id, COALESCE(user_id, …))`) is not
valid Postgres: `gen_random_uuid()` is volatile and cannot appear in a PK /
unique expression, and NULLs are distinct in unique constraints anyway, so a
natural key cannot dedupe agent-only rows (`user_id IS NULL`). Instead:
`id UUID PRIMARY KEY` + partial unique index
`uq_participants_session_user (session_id, user_id) WHERE user_id IS NOT NULL`
(human re-join reuses the row via `ON CONFLICT … WHERE user_id IS NOT NULL DO
UPDATE SET left_at = NULL`; agent-only rows are re-used by a SELECT-then-reuse
in `JoinSession`). `CHECK (user_id IS NOT NULL OR agent_id IS NOT NULL)`
preserves the plan's intent because the `agents` table only lands in
migration 004.

**`ON DELETE SET NULL` for the scoping FKs** (`memory_items.session_id`,
`tasks.session_id`). Deleting a session must never cascade-delete memories or
tasks — session end is a lifecycle event, not data deletion. Ending a session
only flips `is_active`/`ended_at` and stamps participants' `left_at`.
(`episodes.session_id` / `events.session_id` stay bare UUIDs, as in 001/002 —
wiring them is deferred so this migration touches exactly what issue #12
specifies.)

**Partial indexes** (`idx_sessions_active`, `idx_participants_active`,
`idx_memory_session`, `idx_tasks_session`) keep the hot queries — active
session lookup, presence listing, session memory views — index-only on the
rows that matter.

## Decision 2 — stdlib-only hub, no `nhooyr.io/websocket` / `golang.org/x/net`

Why:
- The task forbids `golang.org/x/net`; `nhooyr.io/websocket` is not in
  `go.mod` and adding a dependency for a text-frame fan-out is
  disproportionate. The RFC 6455 subset we need (masked client text frames,
  unmasked server frames, ping/pong/close, `Sec-WebSocket-Accept`) is ~120
  lines over `net/http` Hijack.
- The `Hub` itself is transport-agnostic (clients are `Send chan []byte`
  endpoints), so every routing/presence/backpressure test runs in-memory with
  zero sockets; the socket pumps (`wsReadLoop`/`wsWriteLoop`) are thin
  adapters. A future migration to a WS library only replaces `upgradeToWebSocket`
  + frame helpers — `Hub`, the protocol envelope, and all tests survive.
- Auth reuses the existing JWT stub (`Authorization: Bearer`, with `?token=`
  fallback for browser `WebSocket` clients that cannot set headers).

Non-goals: binary frames, extensions (permessage-deflate), and fragmented
control frames are rejected; inbound payloads are capped at 1 MiB.

**Backpressure:** per-client buffer of 64, non-blocking broadcast. A slow
connection's message is shed and counted (`Hub.Dropped`) instead of stalling
the hub — same drop-and-backfill contract as `MemStore.Subscribe` /
`PostgresStore.Subscribe` (catch-up via `ListEvents`).

## Decision 3 — presence via heartbeat, not socket state alone

Why:
- Socket open ≠ user present: a laptop asleep holds no TCP, a tab in the
  background holds TCP but no attention. The repo already standardizes on a
  90 s heartbeat window (`OfflineThreshold` in store + server, daemon 30 s
  beats), so presence reuses it: every inbound WS frame refreshes
  `lastSeen`; `SweepOffline(90s)` marks silent clients `offline` and fans out
  the transition.
- `typing` is a leased state (`TypingTTL` 6 s, refreshed by each keystroke
  presence message) that demotes to `online` on expiry, so indicators can
  never stick after a disconnect. `idle` is client-reported (no activity for
  5 min per plan §3.4); the server never infers it from timing alone.
- Join/leave is explicit (`JoinSession`/`LeaveSession` stamp `left_at`) while
  presence is derived — the participant row is membership history, the hub is
  liveness. `EndSession` closes both at once.

## Scoping rules (enforced in `internal/store/sessions.go`)

- `CreateSessionMemory` forces `level='session'` + requires `session_id`.
- `NewSessionInherits` (pure predicate): only `CONFIRMED` project/org items
  with no `session_id` enter a new session — sibling session data is
  invisible by construction.
- `ListSessionVisibleMemories`: own session (`PROPOSED`+`CONFIRMED`) ∪
  inherited, with session keys shadowing project keys (`applySessionOverride`).
- `PromotionCandidates` groups session-level keys by `COUNT(DISTINCT
  session_id)` (default threshold 3, plan §2.7); `PromoteSessionMemory` flips
  one item to `level='project'`, clears `session_id`, marks `CONFIRMED`.

## Pre-existing breakage (not touched)

`go build ./...` fails in `internal/materializer` (`oldPost`/`newPost`
unused) and `cmd/nexus` (`runEpisode`/`runDoctor` undefined) — both predate
this change (untracked worktree files, HEAD `a0d26e1`). `internal/store` and
`internal/server` build clean with `go vet` and `go test` green.
