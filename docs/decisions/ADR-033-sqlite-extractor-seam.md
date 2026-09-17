# ADR-033-sqlite-extractor-seam

- **ADR ID:** ADR-033-sqlite-extractor-seam
- **Date:** 2026-09-17
- **Author:** issue-33 subagent
- **Issue:** #33 SQLite sources emit no CONVERSATION_TURN (Cursor/Copilot/Antigravity liveness-only)
- **Status:** Accepted

## Context

The harvester (`internal/daemon/harvester.go`, ADR-005) tails five source
rows, but only the two JSONL rows (Claude, OpenCode) emit
`CONVERSATION_TURN`. The three sqlite-format rows (Cursor, Copilot,
Antigravity `.db`/`.vscdb`/`.sqlite`) only refresh idle timers: row parsing
needs a SQL driver, and no SQL driver dep is approved. The file header still
described Layer 2 as the emitter of turns for all agent files, overstating
backbone coverage for sqlite agents. Constraints: stdlib + `fsnotify` only
(plan §1.9), read-only access, zero UX disruption, JSONL path untouched, no
`go.mod` changes.

## Options Considered

1. **Extractor seam with nil-default (chosen).** Define
   `SQLiteExtractor { ExtractNewRows(dbPath string, since time.Time)
   ([]Turn, error) }` in `harvester.go`; add an `Extractor` slot to
   `TranscriptSource` and an agent-keyed registry on `Harvester`
   (`WithSQLiteExtractor` / `WithSQLiteExtractors`). Default nil =
   liveness-only with an explicit logged reason; a registered extractor
   emits `CONVERSATION_TURN` per row via both `processFile` and the 30s
   `pollSQLite` path. Stdlib only, no new deps.
2. **Add a SQL driver now (`mattn/go-sqlite3`).** CGO: breaks
   cross-compilation for the daemon's target boxes and violates the
   stdlib-only budget without an approved dep decision. Rejected.
3. **Add a pure-Go driver (`modernc.org/sqlite`).** No CGO, but a heavy new
   dependency tree for three source rows, still unapproved; premature until
   sqlite-turn volume justifies it. Rejected (deferred to a follow-up ADR).
4. **Hand-parse SQLite/WAL bytes without a driver.** Fragile against format
   and WAL-checkpoint internals; read-only safety cannot be guaranteed.
   Rejected.

## Decision

Implemented the seam in `internal/daemon/harvester.go` (`package daemon`,
no new imports beyond stdlib `log`):

- `SQLiteExtractor` interface — `(dbPath, since) -> ([]Turn, error)`;
  `since` is the session's last activity (zero when unseen) so
  implementations return only newer rows.
- `TranscriptSource.Extractor` slot — populated by `Harvester.Sources()`
  from the registry for sqlite rows only (JSONL rows never use it);
  default nil.
- `Harvester` registry (`extractors map[string]SQLiteExtractor`) with
  `WithSQLiteExtractor(agent, ex)` / `WithSQLiteExtractors(map)` options
  (nil removes) and `WithLogger(*log.Logger)` for the explicit
  liveness-only reason.
- `processSQLiteFile` — nil extractor: `touch` + log
  `liveness-only (no SQL driver dep, see ADR-033)`; extractor present:
  extract, tag `Path`, emit via the shared `emitTurns` helper (same
  `CONVERSATION_TURN` payload shape, 8000-rune cap); extraction errors are
  logged and keep liveness without failing the watch loop.
- `pollSQLite` routes changed files through `processSQLiteFile` (was
  touch-only), so the 30s WAL poll also emits once an extractor exists.
- Header and `processFile` comments corrected: JSONL emits turns;
  sqlite is liveness-only unless an extractor is registered (no longer
  "backbone emits for all agents").

## Why (Rationale)

This is the only option meeting every acceptance criterion at once: the
reported gap is closed structurally (sqlite rows *can* now emit turns)
without approving a driver dep or touching the JSONL path; the nil default
preserves current production behavior exactly (liveness + logged reason
instead of silent skip); both ingestion paths (`processFile` for
fsnotify/discovery events, `pollSQLite` for WAL churn) share one handler so
they cannot diverge; and verification is green —
`TestHarvestSQLiteNilExtractorLiveness` (zero turns, liveness refreshed,
reason logged, registry defaults nil) and
`TestHarvestSQLiteFakeExtractorEmitsTurns` (2 turns via `processFile`, 2
more via `pollSQLite`, registry wiring cursor-only) plus all 7 prior
harvester tests pass, `go build ./...` OK, `go vet ./internal/daemon/`
clean (see `docs/issues/ISSUE-33.md`).

## Consequences

- No `go.mod`/`go.sum` changes (stdlib `log` only).
- SQLite `CONVERSATION_TURN` content still absent in production until a
  driver-backed `SQLiteExtractor` is supplied (follow-up: approve driver,
  implement extractor per Cursor/Copilot/Antigravity schema, register at
  daemon startup).
- `since`-contract: implementations must filter by timestamp; callers pass
  last activity, so clock skew can re-deliver rows (offset tracking stays
  JSONL-only by design).

## Alternatives Rejected

See Options 2–4 above: CGO driver (toolchain/dep budget), pure-Go driver
(unapproved heavy tree, premature), hand-parsing SQLite bytes (fragile,
unsafe).
