# Foundation Store — Decisions (issues #1, #2, #9)

Date: 2026-09-17. Scope: `migrations/001*`, `migrations/002_events.*`,
`internal/store/*`, `go.mod`/`go.sum`. Every critical choice below carries
Context / Decision / Alternatives / Why / Consequences.

---

## 1. MemStore is kept as a first-class Store, not a throwaway stub

- **Context:** Issue #2 asks for a Postgres data-access layer, but unit tests,
  local dev, and CI must work with no database running. The repo already had a
  working `MemStore` implementing most of the `Store` interface.
- **Decision:** Keep `MemStore`, extend it to the full interface (including the
  new `Subscribe`), and add `PostgresStore` beside it. Both assert
  `var _ Store = (*…)(nil)` at compile time. `NewMemStore()` needs no config;
  only `NewPostgresStore(ctx, dsn)` dials the network.
- **Alternatives:** (a) Delete MemStore, test against real Postgres via
  testcontainers — heavy, Windows-hostile, slow. (b) Generated mocks
  (mockgen) — drift from real semantics, extra toolchain.
- **Why:** Zero-dependency tests run in <1s (`go test ./internal/store/`);
  daemon/server developers iterate without Aurora credentials; the in-memory
  broadcast doubles as the reference semantics for `Subscribe`.
- **Consequences:** Two implementations to keep in sync — mitigated by the
  shared `Store` interface, mirrored method order across files, and
  MemStore-first tests that define behavior (priority, threshold, ordering).

## 2. pgx/v5 (pgxpool) instead of database/sql

- **Context:** The store needs pooling, health checks, `LISTEN/NOTIFY`, and a
  `vector(1536)` column type that `database/sql` + lib/pq cannot encode
  natively.
- **Decision:** `github.com/jackc/pgx/v5` with `pgxpool` (MaxConns 20, MinConns
  2, MaxConnLifetime 30m, MaxConnIdleTime 5m, 60s health checks) plus
  `github.com/pgvector/pgvector-go` for vector encode/decode helpers.
- **Alternatives:** (a) `database/sql` + pgx stdlib bridge — loses native
  `WaitForNotification`, LISTEN needs fragile conn pinning. (b) `lib/pq` /
  `pgx/v4` — maintenance-mode, no vector support. (c) ORM (GORM/Bun) — hides
  the `<=>` operator and index hints we depend on.
- **Why:** pgx is the only driver with first-class `LISTEN`, pool lifecycle
  hooks, and bulk-friendly typing; pgvector-go is the canonical vector codec.
  Matches the plan's locked dependency list (§1.9).
- **Consequences:** New `go.mod` deps (pgx/v5 v5.11.0, pgvector-go v0.4.1);
  contributors need Go module access. `Subscribe` holds one dedicated
  `pgx.Conn` per subscriber — acceptable at daemon scale, must be revisited if
  fan-out ever exceeds hundreds of listeners.

## 3. IVFFlat (lists=100/50) instead of HNSW for vector indexes

- **Context:** `memory_items.embedding` and `episodes.embedding` need cosine
  similarity search from day one; 001 ships the indexes.
- **Decision:** `USING ivfflat (embedding vector_cosine_ops)` with
  `lists = 100` (memories) / `lists = 50` (episodes), exactly per plan §1.1.
- **Alternatives:** (a) HNSW — faster recall at scale but higher build memory,
  slower inserts, and overkill pre-product-market-fit. (b) No index, sequential
  scan — fine for hundreds of rows, falls off a cliff later.
- **Why:** IVFFlat build cost and write amplification are lower for the
  <100K-item regime we will live in for v1; recall is tunable later via
  `SET ivfflat.probes`. The plan explicitly defers HNSW "at scale".
- **Consequences:** Migration to HNSW later is a `CREATE INDEX CONCURRENTLY` +
  drop — no query changes (`<=>` works for both). Revisit when either table
  passes ~100K rows or p99 search exceeds budget.

## 4. BIGSERIAL (int64 sequence) for events.id, UUIDs everywhere else

- **Context:** The event store needs a total order for "give me everything
  after X" sync; all other entities use UUIDs.
- **Decision:** `events.id BIGSERIAL PRIMARY KEY`; projects, workspaces,
  memories, episodes, tasks stay `UUID DEFAULT gen_random_uuid()`.
- **Alternatives:** (a) UUIDs for events too — no natural ordering, cursor
  pagination needs `(created_at, id)` tiebreakers and still risks clock-skew
  inversions. (b) ULIDs — sortable but non-standard in Postgres, weaker tooling.
- **Why:** A monotonically increasing int64 gives free, gap-tolerant cursors
  (`WHERE id > $since ORDER BY id`), which is exactly what `ListEvents`,
  backfill-after-drop, and `forked_at_event_id` branching (Phase 5) need.
  Gaps from rolled-back inserts are harmless (cursor is "greater than", not
  "contiguous").
- **Consequences:** Event IDs are guessable/sequential — never expose them as
  capability tokens. int64 will not exhaust in practice (~9e18 rows).

## 5. Normalize-and-store canonical git URLs; priority URL > root_commit > folder

- **Context:** Project identity must converge across machines where the same
  repo is cloned via different remote spellings.
- **Decision:** `NormalizeGitURL` strips schemes (`https/http/ssh/git+ssh/git`),
  any `user@`, converts scp-like `host:path` → `host/path`, trims `.git` and
  trailing `/`, lowercases. `ResolveProject` matches on normalized URL first,
  then `root_commit`, then `folder_name`, and **persists the normalized form**.
  Also fixed while here: ssh-URL mangling (`ssh://git@…` previously produced
  `ssh///…`) and hardened `user@` handling.
- **Alternatives:** (a) Store raw remote — `git@…` vs `https://…` clones fork
  phantom projects. (b) Hash-only identity — loses human-readable debugging.
- **Why:** Matches plan §1.2 and the locked "Remote URL → root commit → folder"
  priority; normalization is what makes the URL tier actually catch aliases.
- **Consequences:** Stored `canonical_url` values are lowercase normalized
  hosts/paths (e.g. `github.com/mpratyush54/nexus`) — display code must use
  `display_name`, never the raw URL. Folder fallback stays ambiguous
  (first-registered wins in Postgres, map-order in MemStore); acceptable for a
  last-resort tier.

## 6. 90-second heartbeat/offline threshold, shared as `OfflineThreshold`

- **Context:** Plan §1.3: daemon heartbeats every 30s; server marks offline
  after 90s silence. The threshold was a magic `90 * time.Second` literal in
  MemStore and absent from the SQL path.
- **Decision:** Export `const OfflineThreshold = 90 * time.Second`; MemStore
  and `PostgresStore.GetActiveWorkspace` (`last_seen > now() - threshold`,
  most-recent-first) both use it. Heartbeat sets `is_online = true` +
  `last_seen = now()` on every beat.
- **Alternatives:** (a) DB-side interval literal — duplicates the constant in
  SQL, drifts. (b) Shorter threshold (30–60s) — flaps on GC pauses/sleep.
- **Why:** 3 missed 30s beats tolerates one slow tick without flapping;
  a single Go constant keeps both backends identical and unit-testable
  (tests backdate `LastSeen` past the threshold and expect `ErrNotFound`).
- **Consequences:** Presence lags reality by up to 90s — fine for v1
  ("online if heartbeat < 90s", plan §3.4). Changing the beat interval later
  must revisit this constant jointly.

## 7. Atomic sequenced in-memory IDs (bug found by the new tests)

- **Context:** MemStore minted IDs as `time.Now().Format("…​.000000")`
  (microsecond clock). The new priority test created projects in a tight loop:
  two IDs collided and the second project **overwrote the first in the map**.
- **Decision:** `newID(prefix)` = `prefix + unixnano + atomic seq`
  (`sync/atomic.Int64`), applied to all four MemStore ID sites.
- **Alternatives:** (a) `crypto/rand` hex — fine but heavier for tests.
  (b) Per-store mutex counter alone — collides across two MemStore instances.
- **Why:** Time + process-wide atomic counter is unique under concurrency and
  across instances, dependency-free, and keeps the human-readable `proj_/ws_`
  prefixes. The failing test now passes and guards the regression.
- **Consequences:** IDs are not UUIDs — MemStore IDs must never leak into the
  Postgres `::uuid` paths. Production rows always use DB-generated UUIDs.

## 8. Subscribe = non-blocking broadcast + ListEvents backfill (both backends)

- **Context:** Issue #9 needs real-time fan-out; slow consumers must not stall
  appenders, and dropped notifications must be recoverable.
- **Decision:** `Subscribe(ctx, projectID) (<-chan *Event, func(), error)`:
  buffered chan (64), non-blocking send (drop on full), `cancel()` unregisters
  and closes. MemStore broadcasts in-process under the store mutex; Postgres
  `LISTEN`s on `events`, parses the notify header, re-fetches the full row,
  and filters by project. `ctx` cancellation also unsubscribes.
- **Alternatives:** (a) Blocking send — one wedged dashboard stalls all
  appends. (b) Unbounded buffer — memory leak under a dead consumer.
  (c) Notify payload carries the full event — 8000-byte NOTIFY limit, fragile.
- **Why:** "Drop + backfill via ListEvents" is the standard Postgres-bus
  contract: notifications are hints, the table is truth. Re-fetching keeps
  payloads unlimited and subscribers always see committed rows.
- **Consequences:** Consumers must tolerate gaps and re-sync with
  `ListEvents(sinceID)` on reconnect — documented on the method. At-least-once
  duplicates are possible across resubscribe; handlers must be idempotent on
  `event.id`.

## 9. Vector I/O via text cast + pgvector-go codecs (no pgx type registration)

- **Context:** `pgvector-go` v0.4.1 ships pgx support as a *separate module*
  (`pgvector-go/pgx`); pulling it in adds a second versioned dependency and an
  `AfterConnect` registration hook.
- **Decision:** Pass embeddings as `pgvector.NewVector(v).String()` into
  `$N::vector` parameters and read them back via `embedding::text` +
  `Vector.Parse`. pgvector-go is still the codec (no hand-rolled formatting).
- **Alternatives:** (a) Add the `pgx` submodule + `RegisterTypes` — native
  binary codec, marginally faster, but more deps and hook wiring.
- **Why:** Text-cast round-trips are exact for float32, need no connection
  hooks, and keep `go.mod` to two new modules. The 1536-dim text overhead is
  noise next to embedding-model latency.
- **Consequences:** If vector throughput ever dominates profiles, switch to the
  native codec (confined to `encodeEmbedding`/`parseEmbedding` + pool
  `AfterConnect`) — call sites already go through those helpers.

## 10. Idempotent migrations; down files drop what up created

- **Context:** #1 acceptance: schema applies cleanly to PG16+pgvector; down
  migration leaves no orphans. #9 adds FKs to tables 001 created.
- **Decision:** All statements use `IF NOT EXISTS` / guarded `DO` blocks
  (FK adds check `pg_constraint` first); `001.down` drops tables in reverse
  dependency order **plus the two extensions** (fixed: the original down left
  `vector`/`pgcrypto` behind); `002.down` drops trigger → function → FKs →
  table, in that order.
- **Alternatives:** (a) Non-idempotent migrations + external runner state —
  half-applied runs need manual repair. (b) Leave extensions installed —
  fails the "no orphans" criterion and pollutes shared dev clusters.
- **Why:** `RunMigrations` is a dumb lexical executor with no state table yet;
  idempotency makes retry-after-failure safe. Dropping extensions keeps
  `up(001)+up(002) / down(002)+down(001)` a clean round-trip.
- **Consequences:** `IF NOT EXISTS` hides concurrent-create races but also
  hides *type* mismatches on re-run (e.g. changed column type won't alter) —
  acceptable pre-v1; introduce a versioned runner (golang-migrate style) before
  any production data exists.
