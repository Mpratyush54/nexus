# ISSUE-32 — Wire interceptor into handlers + screen payloads for secrets

- **Status:** Done
- **Scope:** `internal/daemon/interceptor.go`, `internal/daemon/interceptor_test.go`
  (logic + tests), additive-only call lines in `internal/daemon/daemon.go`,
  `internal/daemon/fileops.go`, `internal/daemon/gitops.go`,
  `internal/daemon/commands.go`, plus
  `docs/decisions/ADR-032-interceptor-wiring.md` and this file. No other files
  touched. `Register`/`Heartbeat` logic untouched. `go.mod`/`go.sum` untouched.
- **Plan refs:** `implementation-plan.md` §1.3 interceptor (Layer 1 event table),
  §2.1 event store. Prior art: `docs/issues/ISSUE-4.md`,
  `docs/decisions/ADR-004-passive-interceptor-instruction-watcher.md`.

## Problem

The interceptor existed but no handler called it — production emitted zero
Layer-1 events (`FILE_READ`, `FILE_MODIFIED`, `COMMAND_EXECUTED`,
`GIT_DIFF_VIEWED`, `GIT_COMMITTED`). Payloads were also unscreened for
secrets (`internal/scan` NeverPatterns never consulted on the emit path).

## What was built

`internal/daemon/interceptor.go` (`package daemon`, one new import:
`central-memory/internal/scan`):

| Piece | Behavior |
|---|---|
| `RedactedPlaceholder` | `"[REDACTED]"` replacement marker |
| `RedactSecrets(s) (string, bool)` | Pure: replaces every `scan.NeverPatterns` match (verified name — the scan package exposes `NeverPatterns`, no helper func); reports hit; clean input unchanged |
| Payload builders | `FileReadPayload` (redact-then-200-rune-cap), `FileModifiedPayload` (redact diff, re-cap 8KB), `CommandPayload` (redact stdout/stderr before 4KB caps), `GitCommitPayload` (message/stat; hash untouched), `GitDiffPayload` (stat) — events redacted, never dropped |
| `DiffStat(diff)` | Pure `GIT_DIFF_VIEWED` stat: `"<a> added / <r> removed / <n> lines, <m> bytes"` (`+++`/`---` headers excluded from counts); `"(empty diff)"` for empty |

Additive-only wiring (call lines + fields/methods, no restructuring):

| File | Lines added |
|---|---|
| `daemon.go` | `Interceptor *Interceptor` + `lastHEAD` fields; `New` constructs `NewInterceptor(nil)` and seeds `lastHEAD`; `mount` nil-guard; `SetEventSink` injector; `checkGitCommit` HEAD-change detector (emits `OnGitCommit` with best-effort capped message/stat; silent no-op on git errors; idempotent) |
| `fileops.go` | `handleFileRead` → `OnFileRead` on success; `handleFileWrite` snapshots `oldContent` pre-write, → `OnFileWrite` on success |
| `gitops.go` | `handleGitDiff` → `OnGitDiff(ref, DiffStat(out))`; both git handlers → `checkGitCommit()` |
| `commands.go` | `handleCommandRun` → `OnCommand(JoinCmdline, exit, combined, nil)` for every execution (spawn failure maps to exit -1); `git` runs → `checkGitCommit()` |
| `interceptor_test.go` | 7 new tests (below); all prior tests untouched and passing |

## Decisions

- See `docs/decisions/ADR-032-interceptor-wiring.md` (redact-at-emit over
  drop-on-secret and handler-side screening; redact-before-cap ordering so
  split secrets cannot leak partials; HEAD polling from git paths instead of
  the heartbeat loop per the do-not-touch-Heartbeat constraint).
- One subtlety the new tests pin: timeout/spawn failures have no real exit
  code (`RunCommand` returns 0 with an error), so the emitted event uses -1
  rather than a misleading 0.

## Verification

- `go build ./...` — **OK**
- `go vet ./internal/daemon/` — **clean**
- `go test ./internal/daemon/ -run 'TestInterceptRedactSecrets|TestInterceptPayloadsRedactDontDrop|TestInterceptDiffStat|TestHandlerEmits|TestCheckGitCommit|TestSetEventSink' -v` — **all 7 PASS**
  (`TestInterceptRedactSecrets` — all 5 NeverPattern families redacted, clean
  passthrough; `TestInterceptPayloadsRedactDontDrop` — all 5 event types emit
  with placeholder, zero raw bytes; `TestInterceptDiffStat` — counts + empty;
  `TestHandlerEmitsFileReadAndWrite` — `/file/write` → `FILE_MODIFIED`,
  `/file/read` → `FILE_READ` with content head;
  `TestHandlerEmitsCommandAndGitDiff` — `/git/diff` → `GIT_DIFF_VIEWED`,
  `/command/run git version` → `COMMAND_EXECUTED`;
  `TestCheckGitCommitEmitsOnHeadChange` — silent when seeded, emits new hash
  after a commit, idempotent on repeat;
  `TestSetEventSinkWiresEmission` — injection on/off, nil-receiver safe)
- `go test ./internal/daemon/ -count=1` (full package incl. harvester,
  watcher, processor suites) — **PASS**

## Follow-ups

1. Inject the real server client at daemon startup via `SetEventSink`
   (non-blocking queue + background sender per the ADR-004 contract).
2. Consider seeding `lastHEAD` persistence if daemon restarts must distinguish
   "commit during downtime" (currently the first post-restart check emits it —
   arguably correct, but worth a conscious decision).
