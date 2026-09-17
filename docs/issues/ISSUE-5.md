# ISSUE-5 — Transcript Harvester for Silent Discussion Capture

- **Status:** Done
- **Assignee:** wave-1-issue-5 (harvester subagent)
- **Scope:** `internal/daemon/harvester.go`, `internal/daemon/harvester_test.go`,
  `docs/` only. No other daemon files touched.
- **Plan refs:** `implementation-plan.md` §1.3 harvester (sources table,
  behaviors 1–6, end-of-session analysis), §2.2 Layer 2.

## What was built

`internal/daemon/harvester.go` (`package daemon`) — Layer 2 backbone:

| Piece | Behavior |
|---|---|
| `TranscriptSources` | 5 source rows (claude/opencode JSONL; cursor/copilot/antigravity SQLite), dirs mirroring `adapters/registry.go` |
| `ConversationFiles` | Extension filter + `adapters.ClassifyPath` fail-closed reuse |
| `ParseJSONLTurn` | Pure parser: Claude/OpenCode/generic shapes, `{speaker,content,timestamp}`; tool-noise → `ErrSkippedEntry` |
| `TailTurns` + `OffsetTracker` | Byte-offset resume, truncation reset, partial-line holdback |
| `IdleDetector` + `CheckIdle` | Injectable clock; 5m quiet → one `SESSION_TRANSCRIPT_COMPLETE` (re-arms on resume) |
| `WorkspaceMatchesFile` | Containment → `ProjectOf`/`ForPath` → embedded cwd/`workspace.json` → basename heuristic |
| `Harvester.Start` | fsnotify (JSONL) + 30s SQLite WAL poll + 1m idle sweep; read-only, nil-sink safe |
| `EventSink` | Declared once here, shared with interceptor/watcher (same signature) |

## Decisions

- See `docs/decisions/ADR-005-transcript-harvester.md` (tail-from-EOF on
  start, no backfill; SQLite liveness-only until a driver is approved;
  `fsnotify` dep; single shared `EventSink`).
- Two bugs found by the new tests and fixed: `parts`-fallback variable
  shadowing (outer `ok` never set), and JSON-escaped `\\` in `cwd` scan.

## Verification

- `go build ./internal/daemon/ ./adapters/... .` — **OK**
- `go vet ./internal/daemon/` — **clean**
- `go test ./internal/daemon/ -run TestHarvest -v` — **all 7 PASS**
  (`TestHarvestParseJSONLTurn` 11 subcases, `TestHarvestTailOffsetResume`,
  `TestHarvestIdleDetector`, `TestHarvestIdleCompleteEmits`,
  `TestHarvestWorkspaceMatching`, `TestHarvestTranscriptSources`,
  `TestHarvestConversationFilesClassifyFilter`)
- `go test ./internal/daemon/ -v` (full package incl. #4 interceptor + #4
  watcher tests) — **PASS**
- `gofmt` applied to both new files.
- `go build ./...` still fails **only** in `internal/context/builder.go`
  (issue #6's file, out of scope — untouched).

## Follow-ups

1. SQLite row parsing needs a SQL driver dep (new ADR) — poller currently
   tracks liveness only.
2. Hoist `EventSink` to shared `internal/daemon/events.go` once Wave 1
   lands (noted in both harvester and interceptor).
3. Historical backfill flag belongs to the Phase 1b importer (issue #25).
4. `internal/context` build break owned by issue #6.
