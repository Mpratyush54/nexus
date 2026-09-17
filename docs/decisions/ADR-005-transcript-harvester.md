# ADR-005-transcript-harvester

- **ADR ID:** ADR-005-transcript-harvester
- **Date:** 2026-09-17
- **Author:** wave-1-issue-5 (harvester subagent)
- **Issue:** #5 Transcript Harvester for Silent Discussion Capture (`internal/daemon/harvester.go`)
- **Status:** Accepted

## Context

The 4-layer passive extraction pipeline (plan §2.2) designates transcript
harvesting as the **backbone**: 90%+ of decisions live in conversation, and
the coverage matrix shows pure discussion, explanations, corrections, and
research are caught *only* by Layer 2. Constraints: zero UX disruption (no
MCP sampling — it needs user approval, burns context, and interrupts flow),
read-only access to agent files, reuse of `adapters/registry.go` paths and
`ClassifyPath`/`ProjectOf`, stdlib + `fsnotify` only (plan §1.9), and
pure/testable parsers with an injectable clock.

## Options Considered

1. **fsnotify for JSONL + 30s poll for SQLite (chosen).** fsnotify gives
   near-instant turn events for append-only JSONL (Claude, OpenCode);
   SQLite WAL writes don't reliably raise fsnotify events, so
   `workspaceStorage` `.db`/`.vscdb` files are size+mtime polled every 30s
   (plan §1.3 behavior 6), including `-wal`/`-shm` siblings where churn shows.
2. **Poll everything on a fixed interval.** Simpler and uniform, but adds up
   to 30s latency to the backbone layer and wastes IO on idle boxes. Rejected.
3. **Start-up backfill of full history.** Tempting for completeness, but
   floods the event stream on every daemon restart and duplicates Phase 1b's
   importer (issue #25) job. `Discover()` seeds offsets at EOF and tails only
   new content; history import stays with the migration importer.
4. **Parse SQLite rows now.** Impossible under stdlib+fsnotify-only (no SQL
   driver); the poller therefore tracks liveness (keeps the idle detector
   honest) and row parsing is a follow-up once a driver dep is approved.

## Decision

Implemented `internal/daemon/harvester.go` (`package daemon`):

- `TranscriptSources(home, appData)` — the plan §1.3 table, mirroring
  `adapters/registry.go` (`agentDirs` for claude/opencode/cursor,
  `absRoots`/APPDATA for VS Code `workspaceStorage`, Antigravity, Copilot).
- `ParseJSONLTurn([]byte)` — pure, tolerant of Claude/OpenCode/generic
  shapes, one envelope unwrap, text-part concatenation; tool-only, empty,
  and system/tool-speaker lines return `ErrSkippedEntry` (Layer 1 owns tool
  noise).
- `TailTurns` + `OffsetTracker` — byte-offset tailing; truncation resets to
  0; trailing partial lines are held back until newline-terminated.
- `IdleDetector` (injectable `Clock`) + `CheckIdle` — 5m quiet →
  one `SESSION_TRANSCRIPT_COMPLETE` per session (re-arms on resume, so a
  reopened session can complete again); turn counts included.
- `WorkspaceMatchesFile` — containment → `adapters.ProjectOf` vs
  `project.ForPath` (conflicting leaf = hard no) → embedded `cwd` /
  sibling `workspace.json` folder (JSON-escape aware) → basename heuristic.
- `Harvester.Start(ctx)` — fsnotify on source dirs, 30s SQLite poll,
  1-minute idle sweeps; read-only, nil-sink safe. `EventSink` is declared
  once here (`func(eventType string, payload map[string]any)`) and shared
  with interceptor/watcher (follow-up: hoist to `events.go`).
- Turn content capped at 8000 runes; timestamps RFC3339 or `""`.

## Why (Rationale)

This option wins because it is the only one meeting every acceptance
criterion simultaneously: fsnotify+poll covers all five source rows with zero
user/agent awareness (behaviors 1–6); offset tracking guarantees no re-read
(`TestHarvestTailOffsetResume`); the 5-minute idle→complete path is
deterministic via injected clock (`TestHarvestIdleDetector`,
`TestHarvestIdleCompleteEmits`); workspace attribution reuses adapter logic
instead of reinventing it (`TestHarvestWorkspaceMatching`,
`TestHarvestConversationFilesClassifyFilter`); and verification is green:
`go vet ./internal/daemon/` clean, `go test ./internal/daemon/` — all
harvest/intercept/watch tests PASS (see `docs/issues/ISSUE-5.md`).

## Consequences

- New dep `github.com/fsnotify/fsnotify v1.9.0` (+ `golang.org/x/sys`) —
  pre-approved by plan §1.9 / ADR-000-go-deps. `go.mod`/`go.sum` updated.
- SQLite row content not yet extracted (liveness only) — needs driver dep.
- `go build ./...` still fails in `internal/context/builder.go` (issue #6's
  file, untouched per scope); daemon/adapters/repo-root all build.

## Alternatives Rejected

See Options 2–4 above: poll-only (latency), backfill-on-start (event flood,
duplicates #25), SQLite parsing now (violates the dep budget).
