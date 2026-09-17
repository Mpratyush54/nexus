# ADR-032-interceptor-wiring

- **ADR ID:** ADR-032-interceptor-wiring
- **Date:** 2026-09-17
- **Author:** issue-32 subagent
- **Issue:** #32 Interceptor exists but no handler calls it — zero Layer-1 events in prod, payloads unscreened
- **Status:** Accepted

## Context

Issue #4 shipped a complete passive `Interceptor` (`internal/daemon/interceptor.go`,
ADR-004): `OnFileRead` / `OnFileWrite` / `OnCommand` / `OnGitDiff` / `OnGitCommit`
with a nil-safe, panic-isolated `Emit`. But no HTTP handler ever called it, so
production emitted zero Layer-1 events (`FILE_READ`, `FILE_MODIFIED`,
`COMMAND_EXECUTED`, `GIT_DIFF_VIEWED`, `GIT_COMMITTED`) — the Memory Processor's
bug-arc detection (`DetectEpisode`) had no tool-event input. Additionally, event
payloads were never screened against `internal/scan` NeverPatterns, so file
heads, diffs, and command output could carry secret bytes to the store.
Constraints: only `interceptor.go` (+ test) may gain logic; `daemon.go`,
`fileops.go`, `gitops.go`, `commands.go` accept additive-only call lines
(no restructuring, no Register/Heartbeat changes); only two new docs.

## Options Considered

1. **Redact-at-emit via `scan.NeverPatterns` + additive handler wiring + HEAD-change
   detector (chosen).** `RedactSecrets` in `interceptor.go` replaces every
   NeverPattern match with `[REDACTED]` inside the pure payload builders;
   handlers gain one call line each; `Daemon` owns the `*Interceptor`
   (nil-sink default, `SetEventSink` injection) plus a `lastHEAD` detector
   feeding `OnGitCommit`. One screening site covers all five emitters,
   present and future.
2. **Drop events containing secrets.** Rejected: it blinds Layer 1 exactly when
   the most sensitive operations happen, and fail-closed refusal is already the
   sandbox's job (`ErrSecretHit` → 403 in `fileops.go`). The event stream must
   stay complete; redaction preserves the fact of the operation without the
   secret bytes.
3. **Screen in each handler before emitting.** Rejected: five duplicated
   call-site checks that every future emitter must remember; a single emit-side
   screen cannot be bypassed by a new caller.
4. **Async queue inside the interceptor.** Rejected per ADR-004: ordering
   guarantees weaken, tests need timing, and backpressure belongs to the
   injected server client, not the observer. Emission stays synchronous with
   nil/​panic guards.
5. **Hook commit detection into the heartbeat loop.** Rejected: the issue
   constraint forbids touching Register/Heartbeat logic. Polling HEAD from the
   git-status / git-diff / git-command paths gives equivalent production
   coverage (every git interaction re-checks HEAD) with zero heartbeat edits.

## Decision

In `internal/daemon/interceptor.go` (only logic-bearing change):

- `RedactedPlaceholder = "[REDACTED]"` + pure `RedactSecrets(s) (string, bool)`
  over `scan.NeverPatterns` (verified name: the scan package exposes
  `NeverPatterns`, not a helper func — screening iterates it directly).
- All five payload builders redact: `FileReadPayload` (redact-then-cap so a
  secret straddling the 200-rune boundary cannot leak a partial match),
  `FileModifiedPayload` (redact rendered diff, re-cap to 8KB),
  `CommandPayload` (redact stdout/stderr before the 4KB caps), `GitCommitPayload`
  (message/stat; hash passes through), `GitDiffPayload` (stat).
- New pure `DiffStat(diff)` summary (`"<a> added / <r> removed / <n> lines, <m>
  bytes"`, `(empty diff)` for empty) as the `GIT_DIFF_VIEWED` stat — counts
  carry no secret bytes by construction.

Additive-only wiring (no restructuring, Register/Heartbeat untouched):

- `daemon.go`: `Daemon.Interceptor *Interceptor` + `lastHEAD` fields;
  `New` constructs `NewInterceptor(nil)` and seeds `lastHEAD` best-effort;
  `mount` nil-guards the interceptor; new `SetEventSink` injector and
  `checkGitCommit` HEAD-change detector (emits `OnGitCommit` with best-effort
  `log -1` message + `show --stat` stat, both capped; silent no-op on git
  errors/empty repos; idempotent — no repeat event without a HEAD change).
- `fileops.go`: `handleFileRead` emits `OnFileRead(path, size, content)` on
  success; `handleFileWrite` snapshots `oldContent` best-effort before the
  write and emits `OnFileWrite(path, old, new)` on success.
- `gitops.go`: `handleGitDiff` emits `OnGitDiff(ref, DiffStat(out))`;
  both git handlers call `checkGitCommit()`.
- `commands.go`: `handleCommandRun` emits `OnCommand(JoinCmdline(...), exit,
  combined-as-stdout, nil)` for every executed command (unknown exit on spawn
  failure maps to -1); `git` invocations additionally call `checkGitCommit()`.

## Why (Rationale)

- **Redact-don't-drop is the only option keeping both guarantees:** the sandbox
  stays fail-closed (secret reads/writes still 403, untouched) while the event
  stream stays complete (every operation still observable). Covered by
  `TestInterceptPayloadsRedactDontDrop`: all five event types emit with the
  placeholder and zero raw-secret bytes.
- **Redact-before-cap ordering matters:** capping first can split a secret so
  the pattern no longer matches, leaking a partial token. All builders screen
  the full string before applying byte/rune caps.
- **One emit-side screen beats five handler checks** because a future sixth
  emitter inherits it for free; handler diffs stay at 1–3 lines each,
  satisfying the additive-only constraint.
- **HEAD polling outside the heartbeat** satisfies the no-Heartbeat-touch
  constraint while covering production: any `git` command, status poll, or
  diff view detects a new commit within one interaction. `New` seeds `lastHEAD`
  so daemon restarts never re-emit history.
- **Evidence:** `go build ./...` OK; `go vet ./internal/daemon/` clean;
  `go test ./internal/daemon/ -count=1` full package PASS including 7 new
  tests (`TestInterceptRedactSecrets`, `TestInterceptPayloadsRedactDontDrop`,
  `TestInterceptDiffStat`, `TestHandlerEmitsFileReadAndWrite`,
  `TestHandlerEmitsCommandAndGitDiff`, `TestCheckGitCommitEmitsOnHeadChange`,
  `TestSetEventSinkWiresEmission`) — see `docs/issues/ISSUE-32.md`.

## Consequences

- Production now emits all five Layer-1 event types with secret-screened
  payloads; the Processor's episode arc has tool-event input.
- The real server client is injected later with one line:
  `daemon.SetEventSink(func(t string, p map[string]any) { client.Enqueue(t, p) })`.
- Known limitation: `GIT_COMMITTED` message/stat come from best-effort
  `git log/show` subprocesses; if they fail, hash still emits with empty
  message/stat rather than failing the caller.

## Alternatives Rejected

See Options 2–5 above: drop-on-secret (blinds Layer 1), handler-side screening
(duplicated, bypassable), async interceptor queue (nondeterministic order,
wrong layer per ADR-004), heartbeat-hooked commit detection (violates the
do-not-touch-Heartbeat constraint).
