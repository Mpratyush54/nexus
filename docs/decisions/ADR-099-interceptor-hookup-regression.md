# ADR-099 — Interceptor Hookup Regression Test (no reimplementation)

- **ADR ID:** ADR-099-interceptor-hookup-regression
- **Date:** 2026-09-17
- **Issue:** #99 audit: Layer 1 Passive Tool Interceptor allegedly never hooked
  into Daemon file/command handlers (`internal/daemon/`)
- **Status:** Accepted

## Context

Issue #99's acceptance criteria ask for the `interceptor` field, its init,
and `LogFileRead`/`LogFileWrite`/`LogCommand` hooks — but all of that
already exists via issue #32 (`Daemon.Interceptor`, `NewDaemon` init,
`SetEventSink`, nil-guarded hook calls in `handleFileRead`/`handleFileWrite`/
`handleCommandRun`). Reimplementing would churn working code and risk
regressions. `GateBeforeToolCall` belongs to #80 and is out of scope.

## Options Considered

1. **Single end-to-end regression test (chosen)** — pin the existing hookup:
   recording sink via `SetEventSink`, three authed mux calls, one emission
   each, plus a non-nil `Interceptor` assertion. Any future removal fails
   loudly; zero production churn.
2. **Reimplement the hooks** — rejected: duplicates #32, risks behavior drift
   for no gain.
3. **Unit-test each hook in isolation** — rejected: existing
   `TestHandlerEmits*` already cover the queue path; the gap is the
   sink-wired end-to-end path.

## Decision

Append `TestIssue99InterceptorHookupRegression` (+ minimal `issue99Sink`
channel emitter) to `internal/daemon/interceptor_test.go`. Sink-channel
reads (not internal-queue reads) so the test pins the `SetEventSink`
wiring; plain `TempDir` avoids stray `GIT_COMMITTED` events; no new
imports, no production edits.

## Consequences

- Hook removal breaks the build signal immediately.
- Test skips when `git` is absent from `PATH` (command leg needs `git version`).
- Real server-client sink injection remains a follow-up (see ISSUE-32).
