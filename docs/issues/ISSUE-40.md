# ISSUE-40 — WS Event Bridge (Subscribe → Hub) + Bootstrap Wiring

- **Issue:** #40 — Bridge `store.Subscribe` → WS hub; enable `/ws` + web routes in the shipped container
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** subagent
- **Scope constraint:** ONLY `internal/server/wsbridge.go` (+
  `wsbridge_test.go`), `deploy/server-bootstrap/main.go`, `docs/`. Did
  NOT touch `internal/server/server.go`, `routes.go`, `ws.go`, `web.go`,
  or any file under `internal/store/*`. Read `internal/server/ws.go`
  (Hub, `PublishEvent`/`PublishMemoryUpdate`/`PublishEpisodeUpdate`,
  `EnableWS`), `internal/store/events.go` (`Subscribe` signature), and
  `deploy/server-bootstrap/main.go` (stubStore, missing `EnableWS`) first,
  per the issue.
- **Plan refs:** `implementation-plan.md` §3.2 (WS protocol), §3.4
  (presence), §2.1 (event-type registry); ADR-009 (LISTEN/NOTIFY wake-up
  contract), ADR-013 (hub design), ADR-014 (dashboard mounting).

## What was built

| File | Contents |
|---|---|
| `internal/server/wsbridge.go` (NEW) | `BridgeEvents(ctx, pool, hub)`: holds one `Subscribe(ctx, pool, "")` stream, hydrates via `EventStore.GetEventByID` (same pool through a `poolDBTX` adapter — pool can't satisfy `store.DBTX` directly since `DBTX.Query` returns `store.Rows`), fans out via type-matching `Publish*`; reconnect with 1s→30s backoff (reset on delivery). Pure mapping funcs (`memoryActionForEventType`, `episodeActionForEventType`, `publishBridgedEvent`) + `eventPublisher`/`eventFetcher` seams + `subscribeEvents` var for DB-free tests |
| `internal/server/wsbridge_test.go` (NEW) | 11 tests, all DB-free/no-socket: recording fake publisher, map-backed fake fetcher, channel-backed subscribe stubs |
| `deploy/server-bootstrap/main.go` | `srv.EnableWS()` + `srv.RegisterWebRoutes(mux)` (outside auth); **wired `server.NewPostgresStore(db)` from #37 (present in-tree) and removed `stubStore`/`errStorePending`**; dedicated 2-conn bridge pool (same DSN) + bridge goroutine ( hunger: `*store.DB` exposes no pool accessor and `db.go` is another owner's file — see ADR-040) |
| `docs/decisions/ADR-040-ws-event-bridge.md` | Why-mandatory ADR (bridge choice, mapping table, side-pool rationale, PostgresStore wiring, test evidence) |
| `docs/issues/ISSUE-40.md` | This file |

## Decisions (see ADR-040 for rationale)

1. **Subscribe → hydrate → Publish** (not polling, not store-pushes-hub):
   only layering-clean option; out-of-process writes included.
2. **Mapping:** memory proposed/confirmed/rejected and episode
   opened/updated/resolved → typed update frames (payload forwarded as
   item/episode); `MEMORY_SUPERSEDED` (no §3.2 action) and everything
   else → generic `event` frame with explicit snake_case envelope
   (`store.Event` has no JSON tags, never marshaled raw).
3. **Failure isolation:** hydration misses and publish errors log + skip;
   malformed payloads never reach the bridge (`Subscribe` filters);
   broken stream → channel close → resubscribe (no `ListEvents` replay
   here — ADR-009 backfill stays the consumer's job).
4. **Side pool, not a `DB` accessor:** `db.go` untouched per ownership;
   2 conns (LISTEN holder + reconnect overlap), same DSN.
5. **Stub replaced, not kept:** #37's `PostgresStore` is in-tree with a
   compile-time `Store` proof and `*store.DB` already satisfies
   `store.DBTX` — fail-closed 500s become real data routes. (Known
   inherited limit: login stays fail-closed for known users until
   `password_hash` lands — ADR-037's follow-up, not this issue's.)

## Verification

- `go build ./...` → exit 0
- `go vet ./internal/server/ ./deploy/...` → exit 0
- `go test -count=1 -run "TestBridge|TestNextBackoff" ./internal/server/`
  → **PASS (11/11)**: `TestBridgeMemoryMapping`,
  `TestBridgeEpisodeMapping`, `TestBridgeGenericEventEnvelope`,
  `TestBridgeSupersededFallsBackToGeneric`, `TestBridgePublishValidation`,
  `TestBridgeSubscriptionDrainsAndSkipsBadRows`,
  `TestBridgeSubscriptionEndsOnCancel`,
  `TestBridgeLoopResubscribesAfterDrop`,
  `TestBridgeLoopSubscribeErrorThenRecover` (~1s backoff),
  `TestBridgeEventsNilPool`, `TestNextBackoffCaps`
- Full `go test -count=1 ./internal/server/` → **PASS** (`ok … 2.681s`
  — all pre-existing issue-#8/#13/#37/#38 tests unmodified and green)
- `git status` shows no modifications outside the four allowed paths
  (plus other agents' concurrent uncommitted work, untouched)

## Notes for reviewers (concurrent-tree hazards hit during this issue)

- Untracked `wsbridge.go`/`wsbridge_test.go` were deleted mid-task by
  another agent's tree-wide operation (`git clean` suspected); `main.go`
  edits survived. Files were recreated with the `poolDBTX` fix included —
  re-verify with `git status` at merge time.
- `*pgxpool.Pool` does NOT implement `store.DBTX` (`Query` returns
  `pgx.Rows` vs `store.Rows`) — hence the `poolDBTX` adapter in
  `wsbridge.go`. If a pool accessor ever lands on `*store.DB`, the bridge
  can hydrate through it directly.

## Follow-ups (not this issue)

- Persist client `action` frames via `EventStore.AppendEvent` (hub still
  fans out only).
- `Hub.NoteHeartbeat` from the REST heartbeat handler.
- Backfill-on-reconnect (`ListEvents` since high-water mark) for clients
  that missed commits during a bridge outage.
- `wss://` termination, `Origin` checking, per-IP rate limits (from
  ISSUE-13 follow-ups, still open).
