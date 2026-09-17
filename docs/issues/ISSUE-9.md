# ISSUE-9 — Event Store (Append + LISTEN/NOTIFY + Replay)

- **Issue:** #9 — [Phase 2] Event Store (`migrations/002_events.up.sql` +
  `internal/store/events.go`, plan §§2.1–2.2)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `migrations/002_*`,
  `internal/store/events.go` (+ `events_test.go`), `docs/`. Did NOT touch
  `internal/store/db.go`, `projects.go`, `workspaces.go`, `memory.go`.

## What was built

| File | Contents |
|---|---|
| `migrations/002_events.up.sql` | Plan §2.1 transcribed exactly: `events` (`BIGSERIAL id`, project/session/user/agent/workspace/episode ids, `event_type TEXT`, `payload JSONB NOT NULL`, `created_at`), indexes `(project_id, created_at)` / `(project_id, event_type)` / `episode_id`, `ALTER episode_events ADD CONSTRAINT fk_episode_events_event FOREIGN KEY (event_id) REFERENCES events(id)`, `notify_event()` + `pg_notify('events', {id, project_id, event_type})` trigger |
| `migrations/002_events.down.sql` | Reverse-order rollback (trigger → function → FK → indexes → table; preserves 001's `episode_events.event_id` column) |
| `internal/store/events.go` | 19 `Event*` constants + 4 reused from `episodes.go` (#11 landed first; redeclaration broke the build — reused, not redeclared; registry still covers all §2.1 types); `Event`/`AppendEventParams`/`EventFilter`/`EventNotification`; pure `MarshalEventPayload`/`ParseEventNotification`/`BuildListEventsSQL`; `EventStore` (`AppendEvent` single-INSERT, mutex-free via pool; `ListEvents` id-ordered replay; `GetEventByID`); package-level `Subscribe` (dedicated pooled conn, `LISTEN`, ctx-cancel `UNLISTEN`+release+close, project filter, skip-malformed) |
| `internal/store/events_test.go` | 12 tests: constants registry (23, unique, exact-match), payload marshal (nil→`{}`, passthrough, string quoting, errors), notification parse (valid + 7 malformed + extra-field tolerance), deliver filter, replay-SQL bare/full/placeholders, limit clamp, append validation (5 rejections, zero DB touch) + success SQL shape, 8×25 concurrent appends, fake replay order + query error, `GetByID` not-found, nil-pool subscribe, 2 DSN-gated live tests (append→list→filter→hydrate; subscribe→notify→filter→cancel-close) |
| `docs/decisions/ADR-009-event-store-listen-notify.md` | Why-mandatory ADR (seam reuse, id-ordered replay, text-cast scans, strict append validation) |

## Decisions (see ADR-009 for rationale)

1. `EventStore` on existing `DBTX`; `Subscribe` takes `*pgxpool.Pool`
   directly (`*DB.pool` is private, `db.go` untouchable) — no new deps.
2. Replay ordered by `id ASC` (commit order), never `created_at`.
3. `payload::TEXT` server-side cast → plain scans, no driver JSON mapping.
4. Strict `event_type` validation in Go (table intentionally CHECK-free).
5. Notifications are wake-ups: resubscribe + `ListEvents` replay on
   (re)connect; hydrate via `GetEventByID`.

## Verification (2026-09-17, go1.27.0)

- SQL self-review: parens balanced (`(` == `)` counts equal in both
  files), trigger has `RETURNS trigger` / `RETURN NEW` / `FOR EACH ROW
  EXECUTE FUNCTION`, FK references existing 001 tables/column.
- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test -count=1 ./internal/store/ -run TestEvent` → **12 tests:
  10 PASS, 2 SKIP** (`TestEventLiveAppendListReplay`,
  `TestEventLiveSubscribeNotify` — no `TEST_POSTGRES_DSN` in this
  environment; they compile and run where Postgres + pgvector is available)
- `gofmt -l` on owned files → clean
- Full `go test -count=1 ./internal/store/` → **69 PASS, 6 SKIP, 0 FAIL**
  (no regressions in other owners' suites); `go test ./...` whole-repo →
  all packages ok

## Follow-ups (not this issue)

- #10 processor: consume `ListEvents` + `Subscribe` (promotion counters,
  episode auto-detection over the §2.3 signal sequence).
- Phase-3 hub: one `Subscribe` per node, fan-out over WebSocket, presence
  from lifecycle events.
- `DBTX` transactions for event-append + memory-write atomicity (needs
  the `db.go` owners).
- Optional future `CHECK (event_type IN (...))` once the type set
  stabilizes; live-DB run of the two gated tests + `go test -race` on a
  64-bit-gcc machine.
