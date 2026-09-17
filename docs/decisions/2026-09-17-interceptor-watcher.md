# Decision: Passive Interceptor & Instruction File Watcher (2026-09-17)

Issue: [nexus #4](https://api.github.com/repos/Mpratyush54/nexus/issues/4) — Phase 1.3 interceptor + watcher, Phase 2.2 Layers 1/3.
Files: `internal/daemon/interceptor.go`, `internal/daemon/watcher.go` (+ tests).

## Context

The daemon must capture knowledge passively: every tool call flowing through it
(file read/write, command runs, git ops) plus manual edits to instruction files
(`CLAUDE.md`, `.cursorrules`, `.github/copilot-instructions.md`, `.windsurfrules`).
Constraints from the task: stdlib only, no edits to `daemon.go`/`fileops.go`
(owned by another agent), `go build ./internal/daemon/` must keep passing,
and `go.mod` declares zero dependencies.

## Decision 1: Polling + SHA256 instead of fsnotify

The plan (§1.3/§2.2) names fsnotify, and §1.9 lists
`github.com/fsnotify/fsnotify` as a future dependency. We deliberately ship
**stdlib polling** (`time.Ticker` + `os.Stat`/`os.ReadFile` + SHA256 compare):

1. **Zero deps.** `go.mod` has no requirements; adding fsnotify for one watcher
   would force a `go.sum`, vendoring, and cross-platform CI matrix for marginal
   gain. Polling needs only `os`, `crypto/sha256`, `time`.
2. **Correct on more filesystems.** fsnotify misses SQLite WAL writes, network
   mounts, and some Windows editor atomic-save patterns — the plan itself
   already concedes polling for SQLite (§1.3 harvester). Instruction files are
   edited by humans at minute/hour cadence, so a 5s poll loses nothing.
3. **Deterministic and testable.** `CheckOnce()` is a pure poll step: tests
   drive baseline → modify → event without sleeping or temp-dir watchers.
4. **Hash, not mtime.** Editors rewrite identical content (formatters, line
   endings); SHA256 comparison means no spurious `INSTRUCTION_FILE_CHANGED`.
   First sight baselines silently so daemon startup never storms the event
   stream; deletions are forgotten without an event (a delete is not knowledge).

Migration path: if sub-second latency ever matters, swap `Start()`'s ticker
for an fsnotify loop behind the same `EventEmitter` interface — payload shape
is unchanged, so the Memory Processor is unaffected.

## Decision 2: Async non-blocking emit via buffered channel

`LogAction` sits on the agent's tool-call hot path. Blocking it on a slow
server POST would stall user-visible file/command operations — violating the
"agent has no idea" requirement. So:

- Each `LogAction` builds the `Event` and does a `select { case q <- ev:
  default: dropped++ }` — **never blocks**, even with a full buffer.
- A background goroutine drains to the wired `EventEmitter` (server POST in
  production, `ChanEmitter`/fake in tests). `ChanEmitter` itself is
  non-blocking with its own drop counter, so backpressure sheds load at two
  levels instead of propagating.
- Drop counters (`Interceptor.Dropped()`, `ChanEmitter.Dropped()`) make loss
  observable; daemon health reporting can alert on sustained drops rather than
  silently degrading.

Alternative rejected: synchronous emit with retries — simpler delivery
semantics but couples agent latency to network health. Event loss under extreme
backpressure is acceptable (extraction is best-effort; Layer 2 harvesting is
the backbone safety net).

## Decision 3: Payload diff/output caps

- `COMMAND_EXECUTED` stdout/stderr capped at **4KB** (issue #4 requirement).
- `FILE_MODIFIED` / watcher / git diffs capped at **8KB**; `FILE_READ`
  preview at **200 chars** (plan §1.3: "first 200 chars"); commit messages at
  2KB.
- Caps truncate on UTF-8 rune boundaries and append a `[truncated]` marker
  plus a `*_truncated` boolean, so the Memory Processor can distinguish
  "short file" from "capped file".
- Diffs are line-oriented (`- `/`+ ` with common prefix/suffix elision),
  stdlib only — no external diff library. Common lines are elided so a
  one-line rule change in a large `CLAUDE.md` yields a small event.

Without caps, one vendored lockfile write or `go test -v` dump would dominate
the `events` table and every downstream prompt.

## Decision 4: Local ToolEvent/ToolEventEmitter types, local WatchedFile struct

- `ToolEvent`, `ToolEventType`, and `ToolEventEmitter` are defined **locally**
  in the daemon package: `daemon.go`/`fileops.go` are owned by another agent,
  and a shared import would create edit contention (and potential import cycles
  once the daemon wires server callbacks). Coordination happens via channels
  and file existence checks, never cross-file edits.
- The `Tool` prefix (rather than plain `Event`) is forced by a real collision:
  Layer 2's `harvester.go`, written concurrently by another agent, already owns
  the `Event` identifier and `DefaultPollInterval` in package `daemon`. Our
  watcher therefore uses `WatcherPollInterval`. No foreign file was touched to
  resolve this — our symbols were renamed instead.
- `WatchedFile` is a **local struct**, not `store.WatchedFile`: the task allows
  either ("reuse from internal/store if importable else local struct"), and
  `internal/store` is currently not importable from a stdlib-only package —
  sibling agents added `db.go`/`memory.go` requiring `pgx`/`pgvector`, which are
  absent from `go.mod`. Field names/types mirror `store.WatchedFile` exactly so
  mapping to the `watched_files` row is a plain copy at integration time.

## Consequences

- `go build ./internal/daemon/` passes with stdlib only; no `go.sum` changes.
- Unit tests cover truncation, 4KB command cap, diff caps, non-blocking
  behavior under a full queue, and hash change detection (baseline-silent,
  modify-emits, missing-skipped, nested copilot path, diff cap).
- Follow-ups (not this task): wire `Log*` helpers into `fileops.go`/
  `commands.go`/`gitops.go` call sites (other agent's files), and connect the
  emitter to the server event POST.
- Known pre-existing failures outside this task's scope: `daemon_test.go` /
  `fileops_test.go` (other agents' files) fail on Windows sandbox/symlink
  privilege cases; `internal/store` does not build (missing `pgx`/`pgvector`
  modules). All 23 interceptor/watcher tests pass; `go build` and `go vet`
  are clean for the daemon package's non-test compilation.
