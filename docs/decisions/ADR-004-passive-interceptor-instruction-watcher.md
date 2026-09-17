# ADR-004 — Passive Interceptor & Instruction File Watcher

- **ADR ID:** ADR-004-passive-interceptor-instruction-watcher
- **Date:** 2026-09-17
- **Author:** issue-4 subagent
- **Issue:** #4 [Phase 1] Passive Interceptor & Instruction File Watcher (`internal/daemon/`)
- **Status:** Accepted

## Context

Phase 1 needs Layer 1 (tool interception) and Layer 3 (instruction file
watching) of the passive extraction pipeline (`implementation-plan.md`
§§1.3, 2.2) so that file reads/writes, command runs, and git commits are
recorded as events, and hand-edits to `CLAUDE.md` / `.cursorrules` /
`.github/copilot-instructions.md` / `.windsurfrules` surface as
`INSTRUCTION_FILE_CHANGED` hash-diff events — all without the agent or the
user being aware. Constraints: work only in `internal/daemon/
interceptor.go`, `watcher.go` (+ tests) and `docs/`; never touch
`daemon.go`, `fileops.go`, `gitops.go`, `commands.go`, `harvester.go`
(parallel Wave-1 owners); only plan-§1.9 deps allowed; diff/hash/cap logic
must be pure and unit-testable without filesystem or network.

## Options Considered

1. **Func-type `EventSink` + synchronous `Interceptor.Emit` with nil/​panic
   guards (chosen)** — pros: zero-cost abstraction the daemon core can later
   inject a real server client into (`NewInterceptor(client.Enqueue)`);
   synchronous emission keeps event order deterministic and tests trivial;
   nil-sink no-op covers local-only mode; `recover()` guarantees a broken
   sink can never fail the tool call it observes. Cons: a blocking sink
   would add latency — pushed to the injected client (documented contract:
   the real client must queue + send in background).
2. **Async interceptor with internal drop-on-full queue** — pros: latency
   isolation inside this package. Cons: ordering guarantees weaken, tests
   need timing, and queue sizing/backpressure policy belongs to the daemon
   core + server client (issue #3/#8), not to a passive observer. Rejected:
   keep Layer 1 dumb and deterministic; async lives in the injected sink.
3. **Raw before/after blobs in `FILE_MODIFIED` instead of a rendered diff**
   — pros: lossless. Cons: event rows balloon (whole files per keystroke
   save); the Memory Processor wants the *change*, and `watched_files`
   semantics are hash-compare-then-diff. Rejected.
4. **fsnotify-only watcher without `PollOnce`** — pros: smaller API. Cons:
   fsnotify misses SQLite-style writes, unwatched parent dirs (`.github`
   created later), and overflow gaps; tests would be timing-flaky.
   Rejected: `PollOnce` is both the deterministic test hook and the gap
   fallback.
5. **Polling-only watcher (no fsnotify dep)** — pros: zero new imports.
   Cons: contradicts the plan (§1.3 prescribes fsnotify; §1.9 allows the
   dep) and wastes wakeups. Rejected: `fsnotify v1.9.0` was already in
   `go.mod`/`go.sum` (module cache present), so no new dependency was
   introduced.

## Decision

Ship `internal/daemon/interceptor.go`:

- Event constants `FILE_READ`, `FILE_MODIFIED`, `COMMAND_EXECUTED`,
  `GIT_DIFF_VIEWED`, `GIT_COMMITTED`, `INSTRUCTION_FILE_CHANGED` (plan §2.1
  names, shared by the watcher).
- `Interceptor{Sink}` + `NewInterceptor` + `OnFileRead` / `OnFileWrite` /
  `OnCommand` / `OnGitDiff` / `OnGitCommit`; `Emit` is nil-receiver,
  nil-sink, and panic-safe.
- Pure builders: `FileReadPayload` (head = first 200 runes),
  `FileModifiedPayload` (LCS line diff, 8KB render cap),
  `CommandPayload` (stdout/stderr 4KB each + `*_truncated` flags),
  `GitCommitPayload`, `GitDiffPayload`, plus pure `CapBytes`/`CapString`
  (UTF-8-safe), `HeadChars`, `JoinCmdline`, `DiffLines` (LCS with prefix/
  suffix trim, CRLF-normalized, oversized-input summary fallback).

Ship `internal/daemon/watcher.go`:

- `InstructionFiles` watch list transcribing plan §1.3 exactly, with
  `FileTypeForPath` (case-insensitive, slash-normalized) mapping to the
  `watched_files.file_type` values from migration 001.
- Pure `HashBytes`/`HashString` (SHA256 hex = `last_hash`) and pure
  `DetectInstructionChange` (hash-compare + diff against retained content).
- `Watcher` (fsnotify + 250ms debounced flush + `PollOnce` fallback):
  snapshots hashes at startup, watches root + existing watched parents,
  lazily adds created parents (`.github`), emits
  `INSTRUCTION_FILE_CHANGED` `{path, file_type, old_hash, new_hash, diff,
  size, deleted}` on every verified change including creation and deletion;
  1MB content-retention cap with hash-notice degradation beyond it.

## Why (Rationale)

- **Synchronous + guarded Emit wins because passivity has two enemies —
  latency and failure — and both are handled at the right layer:**
  `recover()` + nil-safety provably isolate failures (covered by
  `TestSinkNilSafe`/`TestSinkPanicIsolated`: a panicking sink never reaches
  the caller); latency is a property of the *network client*, which does
  not exist yet, so baking a queue into the observer would be speculative
  complexity. The func-type sink keeps the injection one line for issue #3.
- **Single shared `EventSink` (declared in `harvester.go`, reused here)
  because Go forbids duplicate top-level definitions** — the harvester
  (Wave 1 #5) landed first with the byte-identical signature this issue
  specified, and its own comment anticipates hoisting to a shared file.
  Redeclaring would break `go build`; editing `harvester.go` would violate
  directory ownership. Reuse + documented hoist follow-up is the only
  constraint-satisfying option. Verified: `go vet ./internal/daemon/`
  clean, package builds.
- **4KB/200-char/8KB caps transcribe the plan, not taste:** 4KB
  stdout/stderr and 200-char read preview are literal plan §1.3 values;
  the 8KB diff cap and 2000-line LCS guard bound event rows (a full-file
  rewrite must not become a multi-MB `payload` JSONB). Degradation is
  explicit (`... [truncated]`, `(diff omitted: …)`) so the Processor can
  distinguish "no change" from "change too large".
- **LCS line diff over raw blobs because the consumer is an LLM prompt:**
  `- old` / `+ new` with context lines is the cheapest representation of
  *what changed* for memory extraction, and CRLF normalization prevents
  Windows line-ending rewrites from fabricating diffs (caught by test,
  fixed: normalize-before-compare in `DiffLines`).
- **fsnotify + hash + debounce + `PollOnce` because each covers the
  others' gaps:** fsnotify gives promptness; SHA256 vs last hash gives
  truth (editor temp-file churn without content change emits nothing —
  `TestWatchCheckPathIdempotent`); 250ms debounce coalesces save bursts
  into one event; `PollOnce` covers unwatched-dir creation, overflow, and
  gives tests a timing-free hook. The live fsnotify path is still covered
  by `TestWatchRunEmitsOnWrite` (10s deadline, passes in ~0.6s).
- **Evidence:** `go build` of the daemon package clean; `go vet
  ./internal/daemon/` clean; `go test ./internal/daemon/ -run
  'TestIntercept|TestWatch|TestHash|TestSink'` — 18/18 pass; full package
  suite passes repeatedly (`-count=1`, twice). `go build ./...` currently
  fails only in `internal/context/builder.go` (issue #6 WIP, untouched per
  constraints) — recorded as a follow-up, not this issue.

## Consequences

- Daemon core (issue #3) can wire interception with three lines per
  handler: read `oldContent` before `WriteFileSandboxed`, call
  `OnFileWrite` after success; same pattern for read/command/git-diff.
  File mods then emit `FILE_MODIFIED` with zero client awareness
  (acceptance criterion 1).
- `NewWatcher(root, sink)` + `Run(ctx)` + `PollOnce()` give the daemon a
  complete Layer 3; `watched_files.last_hash` persistence (server side) can
  seed `lastHash` later without API changes.
- Follow-ups: (a) hoist `EventSink` to a shared daemon file with the
  harvester owner; (b) persist `last_hash` server-side and reconcile on
  startup; (c) wire interceptor calls into the HTTP handlers; (d)
  `internal/context` build break owned by issue #6.

## Alternatives Rejected

- **Async internal queue (Option 2):** rejected — nondeterministic order,
  timing-flaky tests, wrong layer for backpressure policy.
- **Raw blobs instead of diffs (Option 3):** rejected — unbounded event
  payloads; the Processor needs changes, not snapshots.
- **fsnotify-only / polling-only watcher (Options 4/5):** rejected —
  first is gap-prone and untestable deterministically, second contradicts
  the plan and wastes resources.
- **Redeclaring `EventSink` in `interceptor.go`:** rejected — duplicate
  definition breaks the build (`EventSink redeclared`); reuse + hoist
  follow-up instead.
