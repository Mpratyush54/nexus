# ISSUE-4 — [Phase 1] Passive Interceptor & Instruction File Watcher

- **Status:** Done
- **Scope:** `internal/daemon/interceptor.go`, `internal/daemon/watcher.go`
  (+ `interceptor_test.go`, `watcher_test.go`) and `docs/` only.
  `daemon.go`, `fileops.go`, `gitops.go`, `commands.go`, `harvester.go`
  untouched (parallel Wave-1 owners). `go.mod`/`go.sum` untouched
  (`fsnotify v1.9.0` was already required).
- **Plan ref:** `implementation-plan.md` §§1.3 (interceptor table, watcher
  list), 2.1 (event types). Full rationale: `docs/decisions/
  ADR-004-passive-interceptor-instruction-watcher.md`.

## What was built

- `internal/daemon/interceptor.go` — Layer 1 passive observer:
  `Interceptor{Sink}` + `NewInterceptor` with `OnFileRead` (`FILE_READ`
  {path,size,head: first 200 chars}), `OnFileWrite` (`FILE_MODIFIED`
  {path,size,diff}), `OnCommand` (`COMMAND_EXECUTED` {cmdline,exit_code,
  stdout,stderr capped 4KB each + truncated flags}), `OnGitDiff`
  (`GIT_DIFF_VIEWED`), `OnGitCommit` (`GIT_COMMITTED`). `Emit` is
  nil-receiver/nil-sink safe and `recover()`-guarded, so file mods emit
  without client awareness and a broken sink can never fail a tool call.
  Pure helpers: `CapBytes`/`CapString` (UTF-8-safe), `HeadChars`,
  `JoinCmdline`, `DiffLines` (LCS, CRLF-normalized, 8KB render cap,
  oversized-input summary fallback).
- `internal/daemon/watcher.go` — Layer 3 fsnotify watcher:
  `InstructionFiles` (`CLAUDE.md`, `.cursorrules`,
  `.github/copilot-instructions.md`, `.windsurfrules` → `watched_files`
  `file_type` values), pure `FileTypeForPath` / `HashBytes` / `HashString`
  (SHA256 = `last_hash`) / `DetectInstructionChange`, and `Watcher`
  (fsnotify + 250ms debounced flush + deterministic `PollOnce` fallback;
  lazy `.github` parent watching; creation + deletion events with
  `deleted` flag; 1MB content-retention cap). Emits
  `INSTRUCTION_FILE_CHANGED` `{path,file_type,old_hash,new_hash,diff,
  size,deleted}`.
- Coordination note: `EventSink func(eventType string, payload map[string]any)`
  is declared once in `harvester.go` (identical signature, landed first)
  and reused here — redeclaring breaks the build, touching `harvester.go`
  violates ownership. Hoisting it to a shared file is a logged follow-up.
- `docs/decisions/ADR-004-passive-interceptor-instruction-watcher.md` —
  full ADR with mandatory Why section. This file (`docs/issues/ISSUE-4.md`).

## Verification

- `go build` (daemon package + dependents): **clean**.
- `go vet ./internal/daemon/`: **clean**.
- `go test ./internal/daemon/ -run
  'TestIntercept|TestWatch|TestHash|TestSink'`: **18/18 pass** (includes
  live-fsnotify `TestWatchRunEmitsOnWrite`, ~0.6s).
- Full `go test ./internal/daemon/ -count=1`: **passes** (run twice).
  One transient full-run failure was observed (`TestHarvestParseJSONLTurn`,
  `TestHarvestWorkspaceMatching` — issue #5's tests, untouched here);
  harvester-only runs pass with these files present and the failures
  reproduce neither with nor without them deterministically — attributed
  to the harvester agent editing `harvester.go` mid-run (mtime/size
  changed between reads), not to this change (no shared code paths).
- `go build ./...` fails in `internal/context/builder.go` (issue #6 WIP,
  `Project redeclared`) — out of scope, left for its owner.
- Bug caught by tests during development: `DiffLines` compared raw
  strings before CRLF normalization, so a pure line-ending rewrite
  rendered a context-only diff; fixed to normalize-before-compare.

## Follow-ups

- Hoist `EventSink` to a shared daemon file with the harvester owner.
- Wire `OnFileRead`/`OnFileWrite`/`OnCommand`/`OnGitDiff` calls into the
  HTTP handlers (issue #3) and `OnGitCommit` into heartbeat HEAD-change
  detection; persist `last_hash` server-side (`watched_files`).
- `internal/context` build break (issue #6) blocks repo-wide `go build
  ./...`; unrelated to this issue.
