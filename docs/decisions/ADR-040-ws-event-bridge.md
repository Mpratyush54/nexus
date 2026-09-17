# ADR-040 — WS Event Bridge (Subscribe → Hub) + Bootstrap Wiring

- **ADR ID:** ADR-040-ws-event-bridge
- **Date:** 2026-09-17
- **Author:** subagent (issue #40)
- **Issue:** #40 Wire `store.Subscribe` → WS hub; enable `/ws` + web routes in the shipped container
- **Status:** Accepted

## Context

Two halves of live updates existed with nothing between them, and the
shipped container served neither:

1. `internal/store/events.go` streams committed rows via `Subscribe(ctx,
   pool, projectFilter)` (LISTEN/NOTIFY wake-ups + `GetEventByID`
   hydration, per ADR-009) — but no caller existed.
2. `internal/server/ws.go` fans out via `Hub.PublishEvent` /
   `PublishMemoryUpdate` / `PublishEpisodeUpdate` (+ `EnableWS()` for `GET
   /ws`) — but `deploy/server-bootstrap/main.go` never called `EnableWS()`,
   never mounted `RegisterWebRoutes`, and never bridged the store stream.
3. The bootstrap served data routes through a `stubStore` that fails closed
   (HTTP 500 "pending") on every data route.

Constraints: touch ONLY `internal/server/wsbridge.go` (+ test), the
bootstrap entrypoint, and docs — no edits to `server.go`, `routes.go`,
`ws.go`, `web.go`, or anything under `internal/store/*` (parallel-agent
ownership). Zero new dependencies.

## Options Considered

1. **Poll `ListEvents` on a ticker and diff by id** — no LISTEN
   connection, trivially testable. Rejected: reintroduces the polling lag
   NOTIFY exists to kill, and id-diffing replays history the hub already
   fanned out (duplicate delivery on every tick unless high-water marks
   are persisted somewhere).
2. **Push from inside `EventStore.AppendEvent`** — exact delivery, no
   LISTEN. Rejected: requires the store to import (or be passed) the hub —
   a layering inversion (store → server) forbidden by the ownership rule,
   and it misses rows written by any other process (migrations, daemons).
3. **Subscribe → hydrate → Publish bridge goroutine (chosen)** — the
   store stays the source of truth, the hub stays transport-agnostic, and
   the bridge is a thin pure-mapping layer between two public seams.

## Decision

- NEW `internal/server/wsbridge.go`: `BridgeEvents(ctx, pool, hub)` holds
  one `Subscribe(ctx, pool, "")` stream at a time, hydrates each
  notification via `EventStore.GetEventByID` on the same pool, and fans
  out through the type-matching `Publish*` method, resubscribing with
  1s→30s exponential backoff (reset on first delivery).
- Mapping: `MEMORY_{PROPOSED,CONFIRMED,REJECTED}` →
  `memory_update:{proposed,confirmed,rejected}`;
  `EPISODE_{OPENED,UPDATED,RESOLVED}` →
  `episode_update:{opened,updated,resolved}`; everything else → generic
  `event` frame with an explicit snake_case envelope. `MEMORY_SUPERSEDED`
  has no §3.2 action and deliberately falls back to generic rather than
  inventing protocol.
- Bootstrap (`deploy/server-bootstrap/main.go`): calls `srv.EnableWS()`,
  mounts `srv.RegisterWebRoutes(mux)` outside auth (per web.go's
  preferred mounting), wires `server.NewPostgresStore(db)` from issue #37
  **(present in-tree — stub removed)**, and starts the bridge goroutine on
  a dedicated 2-conn pool (see below).
- Testability seams: `eventPublisher` (Hub surface; fake records calls),
  `eventFetcher` (`GetEventByID`; fake serves canned rows), `subscribe`
  func param + `subscribeEvents` var (channel stubs, no Postgres).

## Why (Rationale)

- The bridge consumes only public seams (`Subscribe` signature,
  `EventStore`, `Publish*`), so it honors the no-edits ownership rule —
  verified: `git status` shows no modifications to `server.go`,
  `routes.go`, `ws.go`, `web.go`, or `internal/store/*`.
- Hydrate-on-wake-up (not payload-in-NOTIFY) follows ADR-009 exactly:
  NOTIFY carries `(id, project_id, event_type)` only; full rows come from
  `GetEventByID`, keeping per-message cost constant and redelivery
  idempotent by id.
- **Dedicated bridge pool (2 conns, same DSN)** instead of reusing
  `*store.DB`'s pool: `DB` keeps its pool private with no accessor, and
  adding one means editing `db.go` (another owner's file). LISTEN holds
  one connection permanently, so sharing the serving pool would also steal
  a serving conn; a 2-conn side pool (LISTEN + reconnect overlap) is
  isolated and cheap. If a pool accessor ever lands, the bridge can take
  `db` directly with a one-line change.
- **PostgresStore wired, stub removed:** issue #37's adapter is present
  in-tree (`internal/server/postgres.go`, `NewPostgresStore(db
  store.DBTX)`, compile-time `Store` proof) and `*store.DB` already
  satisfies `store.DBTX`, so the container now serves real data routes
  instead of fail-closed 500s. Known limit inherited from #37 (not
  re-litigated here): `Authenticate` stays fail-closed for known users
  until a `password_hash` column lands (see ADR-037).
- Verification: `go build ./...` exit 0; `go vet ./internal/server/
  ./deploy/...` exit 0; `go test -count=1 ./internal/server/` PASS
  (full suite incl. 11 new bridge tests, ~2.7s).

## Consequences

- The shipped container now serves `/ws` (JWT: header or `?token=`),
  the dashboard (`/`, `/app.js`, `/styles.css`), real data routes, and
  live fan-out of every committed event to subscribed clients.
- Bridge misses while disconnected are NOT replayed (ADR-009 contract);
  clients backfill via `ListEvents`. Persisting client `action` frames
  through `AppendEvent` remains a follow-up (hub still fans out only).
- Operational: one extra Postgres connection per server replica (LISTEN)
  plus reconnect overlap; backoff caps LISTEN retry at 30s against a dead
  DB; hydration/publish failures log and skip (one bad row never kills
  the stream).

## Alternatives Rejected

- Polling ticker (Option 1): latency + duplicate-delivery risk; ignores
  the NOTIFY infrastructure built for exactly this.
- Store-pushes-to-hub (Option 2): inverts the store→server layering,
  edits another owner's file, and misses out-of-process writes.
- Reusing `*store.DB`'s pool via a new accessor: edits `db.go`
  (ownership violation) for zero functional gain over the side pool.
