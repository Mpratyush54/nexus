# Decision: Transcript Harvester (Layer 2) — tail+poll, no SQLite driver, 5min idle window

Date: 2026-09-17
Scope: `internal/daemon/harvester.go` (GitHub Mpratyush54/nexus issue #5,
implementation-plan.md Phase 1.3 harvester + Phase 2.2 Layer 2 backbone)

## Context

The Transcript Harvester silently captures research conversations, architecture
debates, and decisions from native agent session files (Claude JSONL, Cursor /
VS Code / Antigravity SQLite-vscdb, OpenCode JSONL) with zero user/agent
interruption. It emits `CONVERSATION_TURN` per meaningful turn and a
`SESSION_TRANSCRIPT_COMPLETE` batch when a session ends, feeding the Memory
Processor's deep-analysis pass.

## Decisions

### 1. Byte-offset tailing + 30s polling (stdlib only, no fsnotify)

- Tailing tracks the last-read byte offset per file, so restarts and polls
  never re-emit. Truncation (log rotation) resets the offset to zero.
- Only bytes through the last full line are consumed; torn writes (no trailing
  newline) are held back for the next poll instead of failing JSON parse.
- Polling (not fsnotify) because: (a) `go.mod` is dependency-free and the plan
  mandates stdlib-only for this file; (b) fsnotify does not fire reliably on
  SQLite WAL writes anyway, so SQLite sources need polling regardless — one
  mechanism for both formats is simpler; (c) polling is Windows-friendly and
  has no handle-leak failure modes. This mirrors the Layer 3 watcher rationale
  in `2026-09-17-interceptor-watcher.md`.
- Trade-off accepted: up to 30s detection latency on new turns (matches the
  plan's SQLite poll cadence; harmless for a background memory pipeline).

### 2. No SQLite driver — track mtime/size, emit a completion hint

- Cursor / Antigravity / Copilot transcripts live in SQLite/vscdb stores. Adding
  a SQLite driver (cgo `mattn/go-sqlite3` or a pure-Go port) would break the
  zero-dependency, cross-platform daemon for Phase 1 value: we cannot parse
  rows, only observe change.
- So `TrackSQLite` records `{size, mtime}` per file. A change marks session
  activity (deferring the idle trigger); the later `SESSION_TRANSCRIPT_COMPLETE`
  event carries `detail: "sqlite/vscdb source: rows not parsed (no driver); …"`,
  telling the Memory Processor that full extraction must happen out-of-band.
- Revisit when: a pure-Go, no-cgo SQLite reader is vendored, or agents expose
  transcripts in JSONL (then those sources migrate to `FormatJSONL` tailing).

### 3. 5-minute idle window for end-of-session deep analysis

- Per plan §2.2: no new content for 5+ minutes fires exactly one
  `SESSION_TRANSCRIPT_COMPLETE` carrying the full turn batch. New activity
  re-arms the trigger, so long sessions produce one completion per idle gap,
  never duplicates, never premature batches (verified by injectable-clock
  tests at 4min = silent, 6min = fires, refire suppressed, re-arm works).
- 5 minutes balances "user just paused to think" (no spurious deep-analysis
  LLM calls on the user's key) against "session actually ended" (memories
  land promptly). Shorter windows burn LLM budget on partial context; longer
  windows delay cross-device convergence (Bob waiting on Alice's decisions).

### 4. Reuse `adapters` package (registry + classification)

- `ResolveSources()` cross-checks agent names against `adapters.Registry()`,
  so adding/removing an agent adapter automatically scopes harvesting; the
  hardcoded dir list is the fallback because the registry exposes backup
  roots, not raw transcript dirs.
- `ScanAndTail` skips any path `adapters.ClassifyPath` marks NEVER, so
  credentials/secrets are never opened even if they sit beside transcripts.
- Workspace matching (`MatchesWorkspace`: path must contain the workspace
  folder name) prevents cross-project leakage between checkouts.

### 5. Noise filtering boundary with Layer 1

- The parser skips `tool_use` / `tool_result` / `system` / `thinking` record
  types, tool/function roles, Claude-style content blocks that are not text,
  blanks, and unparsable lines. Tool activity is Layer 1's (interceptor)
  territory; Layer 2 keeps dialogue only, so the event stream has no double
  coverage and the Memory Processor sees clean turns.

## Coordination note (parallel agents, same tree)

- The shared daemon envelope (`EventType` / `Event` / `EventEmitter`) is owned
  by `harvester.go`; `interceptor.go` already renamed its own types to
  `ToolEvent*` to avoid collision.
- `EventInstructionFileChanged` is declared in `harvester.go` ONLY as a
  temporary alias so the package builds while `watcher.go`'s refactor lands;
  its canonical home is `watcher.go` — move it there and delete the alias.
- `go.mod` / `go.sum` gained `pgx/v5` + `pgvector-go` (required by
  `internal/store`, not by the harvester, which stays stdlib-only apart from
  the in-repo `adapters` import).

## Verification

- `harvester_test.go`: tail offset (append picked up exactly once), noise
  filtering (incl. mixed text+tool_use content blocks), idle detection with
  injectable clock, workspace matching, SQLite mtime/size tracking + hint,
  torn-write holdback. All 6 pass (`go test`, isolated module since sibling
  daemon/store files are mid-refactor by other agents — see note above).
- `gofmt` clean. Full-repo `go build ./...` currently blocked only by other
  agents' in-flight files (`interceptor_test.go` old names, `store` gaps);
  no errors originate from `harvester.go` / `harvester_test.go`.
