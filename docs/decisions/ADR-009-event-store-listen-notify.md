# ADR-009 — Event Store: Append + LISTEN/NOTIFY + Replay

- **ADR ID:** ADR-009-event-store-listen-notify
- **Date:** 2026-09-17
- **Author:** issue-#9 agent
- **Issue:** #9 Event store (`migrations/002_events` + `internal/store/events.go`)
- **Status:** Accepted

## Context

Phase 2 needs the append-only event log that every extraction layer writes
to and the Memory Processor (issue #10) consumes, per
`implementation-plan.md` §§2.1–2.2, while three constraints collide:

1. **Parallel ownership** — `internal/store/db.go`, `projects.go`,
   `workspaces.go` (issues #1/#2) and `memory.go` (issue #6) are owned by
   other agents and must not be touched, yet the event store needs the
   `DBTX`/`Rows` seams, the `episode_events` FK completion, and a pool for
   `LISTEN`.
2. **Real-time without new infra** — the plan mandates Postgres
   `LISTEN/NOTIFY` (no Kafka until throughput pressure), so `Subscribe`
   needs a dedicated connection per subscriber with context-cancel cleanup.
3. **Testability** — constants, payload marshaling, replay-SQL, and channel
   parsing must be pure and unit-testable without a database; live behavior
   stays behind `TEST_POSTGRES_DSN`-gated tests.

## Options Considered

1. **Expose the pool from `*DB` (accessor or exported field) and subscribe
   through it.** Pros: one pool, one owner. Cons: requires editing `db.go`,
   which is forbidden by the ownership constraint; also entangles the pool
   owner's API with a Phase-2 concern.
2. **(Chosen) Self-contained `events.go`: `EventStore` on the existing
   `DBTX` seam + standalone `Subscribe(ctx, *pgxpool.Pool, filter)`.**
   Append/replay go through `DBTX` (no file touched — `*DB` already
   satisfies it); `Subscribe` takes the pool directly, so the caller (server
   WS hub, processor) passes the pool it already owns. No new dependency:
   `pgxpool` ships inside the plan-§1.9-allow-listed `pgx/v5` module.
3. **Polling subscription (`ListEvents` on a ticker) instead of
   `LISTEN`.** Pros: no dedicated connection, trivially testable. Cons:
   latency is poll-interval-bound (Phase 3 needs <1s fan-out), and idle
   polling burns Aurora Serverless capacity that scales-to-zero is supposed
   to save. Rejected; polling remains only as the replay-backfill path.

## Decision

- `migrations/002_events.up.sql`: plan §2.1 transcribed exactly — `events`
  (`BIGSERIAL id`, project/session/user/agent/workspace/episode ids,
  `event_type TEXT`, `payload JSONB NOT NULL`, `created_at`), indexes
  `(project_id, created_at)` / `(project_id, event_type)` / `episode_id`,
  `ALTER episode_events ADD CONSTRAINT fk_episode_events_event FOREIGN KEY
  (event_id) REFERENCES events(id)` (completing 001's deferred FK;
  `session_id`/`agent_id` stay plain UUID — parents land in 003/004),
  `notify_event()` trigger + `pg_notify('events')` with
  `{id, project_id, event_type}`. `002_events.down.sql` reverses in
  dependency order (trigger → function → FK → indexes → table).
- `internal/store/events.go`: 19 `Event*` constants plus 4 reused from
  `episodes.go` (issue #11 landed first declaring `EventFileRead`,
  `EventFileModified`, `EventCommandExecuted`, `EventGitCommitted` for arc
  detection — redeclaring them here broke `go build`, so this file reuses
  them; the registry still covers every §2.1 type) + `ValidEventTypes`/`IsValidEventType` (exact match); `Event`,
  `AppendEventParams`, `EventFilter` (project/session/episode, `ANY`
  type match, inclusive `Since`/`Until`, clamped limit 100/1000),
  `EventNotification`; pure `MarshalEventPayload` (nil → `{}`, bytes/Raw
  passthrough validated, strings JSON-quoted), `ParseEventNotification`,
  `BuildListEventsSQL` (`ORDER BY id ASC` — append order, never
  `created_at`); `EventStore{db DBTX}` with `AppendEvent` (validate-then-
  single-`INSERT ... RETURNING`, no mutex — the pool parallelizes),
  `ListEvents` (replay), `GetEventByID` (NOTIFY hydration, wrapped
  `ErrNotFound`); package-level `Subscribe` (acquire → `LISTEN` →
  goroutine `WaitForNotification` → parse → project filter → buffered
  channel; `UNLISTEN` + release + close on context cancel).
- Contract: notifications are wake-ups, not delivery — subscribers MUST
  replay via `ListEvents` on (re)connect (`GetEventByID` hydrates single
  rows). Malformed payloads are skipped; a broken connection ends the
  stream (resubscribe + replay).

## Why (Rationale)

- **Seam reuse over file edits:** `EventStore` compiles against the
  `DBTX`/`Rows` interfaces from issues #2/#6 with zero changes to their
  files, and `Subscribe` sidesteps `*DB`'s private pool by taking
  `*pgxpool.Pool` — the exact split the ownership constraint forces, and
  it keeps pool lifecycle (sizing, close) with the process owner instead
  of fragmenting it per store. Verified by `go build ./...` + `go vet`
  passing on the merged tree with parallel agents' files (incl. 003)
  present.
- **Append order is the only safe replay order:** `created_at` ties
  (same-transaction inserts, clock skew) make time-ordering
  non-deterministic; `BIGSERIAL id` is the commit order, so `ORDER BY id
  ASC` is the replay contract and `Since`/`Until` are convenience bounds
  only.
- **`payload::TEXT` at scan time:** avoids driver-level JSON/UUID decoding
  entirely (plain `string`/`int64`/`time.Time` scans), so `events.go`
  cannot break on pgx type-mapping subtleties; validity is enforced at
  append (`MarshalEventPayload`) and asserted on read (`json.Valid` in
  live tests).
- **Strict type validation at append:** the table has no CHECK on
  `event_type` (plan-exact, forward-compatible storage), so the Go
  registry is the typo firewall — `AppendEvent` rejects unknowns before
  any round-trip, and `ValidEventTypes` length is pinned to 23 by test so
  registry drift fails loudly.
- **Evidence:** `go build ./...` 0, `go vet ./internal/store/` 0,
  `gofmt` clean, `go test ./internal/store/ -run TestEvent` all PASS
  (units) with live tests SKIP without a DSN; SQL self-review (balanced
  parens, trigger `RETURNS trigger`/`RETURN NEW`/`FOR EACH ROW`
  syntax) recorded in ISSUE-9.

## Consequences

- Issue #10 (processor) consumes `ListEvents` + `Subscribe`; Phase-3 hub
  owns one `Subscribe` per node and hydrates via `GetEventByID`.
- A sweeper/cron owns nothing here — unlike presence, the log needs no
  background repair (replay is idempotent by id).
- `DBTX` still has no transaction support: event append + memory-write
  atomicity stays a follow-up (owned by the `DBTX` agents).

## Alternatives Rejected

- Pool accessor on `*DB` (option 1): forbidden file touch for zero
  functional gain — callers already hold the pool.
- Polling subscription (option 3): misses the <1s fan-out goal and burns
  serverless capacity; kept only as replay-backfill.
- Native UUID/JSON scan types: heavier driver coupling for zero Phase-2
  gain; text-cast scans are lossless here.
