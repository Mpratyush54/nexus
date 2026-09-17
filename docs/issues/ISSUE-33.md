# ISSUE-33 — SQLite extractor seam (Cursor/Copilot/Antigravity emit no CONVERSATION_TURN)

- **Status:** Done
- **Scope:** `internal/daemon/harvester.go`, `internal/daemon/harvester_test.go`,
  `docs/decisions/ADR-033-sqlite-extractor-seam.md`, this file only. No other
  daemon files touched. `go.mod`/`go.sum` untouched (stdlib `log` only).
- **Plan refs:** `implementation-plan.md` §1.3 harvester (sources table,
  behaviors 1–6), §2.2 Layer 2. Prior art: `docs/issues/ISSUE-5.md`,
  `docs/decisions/ADR-005-transcript-harvester.md`.

## Problem

Sqlite-format sources (Cursor/Copilot/Antigravity) only refreshed idle
timers; they never emitted `CONVERSATION_TURN`, because row parsing needs a
SQL driver and no SQL driver dep is approved. The skip was also silent, and
the file header overstated backbone coverage for those agents.

## What was built

`internal/daemon/harvester.go` (`package daemon`, stdlib + `fsnotify` only):

| Piece | Behavior |
|---|---|
| `SQLiteExtractor` | New seam: `ExtractNewRows(dbPath string, since time.Time) ([]Turn, error)`; `since` = session last activity (zero when unseen) |
| `TranscriptSource.Extractor` | Extractor slot on sqlite source rows; default nil = liveness-only |
| Registry + options | `Harvester.extractors` (agent-keyed) via `WithSQLiteExtractor` / `WithSQLiteExtractors` (nil removes); `Sources()` wires sqlite rows; `WithLogger` for the explicit reason |
| `processSQLiteFile` | Nil extractor: touch + log `liveness-only (no SQL driver dep, see ADR-033)`; extractor present: extract, tag `Path`, emit via shared `emitTurns` (same payload shape, 8000-rune cap); errors logged, liveness kept, loop never fails |
| `pollSQLite` | Changed files now route through `processSQLiteFile` (was touch-only), so the 30s WAL poll emits once an extractor exists |
| Comments | Header + `processFile` corrected: JSONL emits turns; sqlite is liveness-only unless an extractor is registered |
| `harvester_test.go` | `fakeSQLiteExtractor` (since-honoring fake) + 2 new tests (below); JSONL path untouched |

## Decisions

- See `docs/decisions/ADR-033-sqlite-extractor-seam.md` (seam-over-driver:
  CGO `mattn/go-sqlite3` breaks cross-compilation, pure-Go
  `modernc.org/sqlite` is an unapproved heavy tree, hand-parsing WAL bytes
  is fragile — all deferred; nil default preserves current behavior).
- One failure found by the new tests and fixed: the poller re-processed a
  `processFile`-seen sqlite DB (no snapshot outside `Discover`), so a
  since-ignoring fake double-emitted — the fake now honors the `since`
  contract (non-zero `since >= epoch` returns nothing), matching what a
  real driver-backed implementation must do.

## Verification

- `go build ./...` — **OK**
- `go vet ./internal/daemon/` — **clean**
- `go test ./internal/daemon/ -run TestHarvest -v` — **all 9 PASS**
  (7 prior: `TestHarvestParseJSONLTurn` 11 subcases,
  `TestHarvestTailOffsetResume`, `TestHarvestIdleDetector`,
  `TestHarvestIdleCompleteEmits`, `TestHarvestWorkspaceMatching`,
  `TestHarvestTranscriptSources`,
  `TestHarvestConversationFilesClassifyFilter`; 2 new:
  `TestHarvestSQLiteNilExtractorLiveness` — zero turns, liveness refreshed,
  reason logged, registry defaults nil;
  `TestHarvestSQLiteFakeExtractorEmitsTurns` — 2 turns via `processFile`, 2
  more via `pollSQLite`, registry wiring cursor-only)
- `go test ./internal/daemon/ -count=1` (full package incl. interceptor,
  watcher, processor tests) — **PASS**

## Follow-ups

1. Approve a SQL driver dep (new ADR) and implement a driver-backed
   `SQLiteExtractor` per Cursor/Copilot/Antigravity schema; register at
   daemon startup.
2. Consider per-DB extraction cursors if `since`-filtering proves too
   coarse under clock skew.
