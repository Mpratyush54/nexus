# ISSUE-99 — Interceptor hookup audit (STALE finding, regression test only)

- **Issue:** #99 — audit claimed `Daemon` lacks the interceptor field/init and
  `LogFileRead`/`LogFileWrite`/`LogCommand` hooks (issue #32 scope).
- **Status:** Done (tests only; no production change — finding verified STALE).
- **Scope constraint:** ONLY `internal/daemon/interceptor_test.go` (append-only:
  one test + one sink type), `docs/decisions/ADR-099-interceptor-hookup-regression.md`,
  this file. Did NOT touch `internal/daemon/daemon.go`, `GateBeforeToolCall`
  (tracked in #80, out of scope), or any other package. No git operations.

## Verification (finding is STALE)

- `Daemon.Interceptor *Interceptor` field exists (`daemon.go:68`); `NewDaemon`
  constructs `NewInterceptor(0, nil)` (`daemon.go:114`) and `SetEventSink`
  attaches a downstream sink.
- `handleFileRead` → `LogFileRead`, `handleFileWrite` → `LogFileModified`,
  `handleCommandRun` → `LogCommand` — all present with nil guards.

## What was built

- `TestIssue99InterceptorHookupRegression` (+ `issue99Sink` recording
  `ToolEventEmitter`): asserts `Interceptor` non-nil after `NewDaemon`,
  attaches the sink via `SetEventSink`, drives `POST /file/write`,
  `/file/read`, `/command/run` over the mux with auth, and asserts each
  emits `FILE_MODIFIED` / `FILE_READ` / `COMMAND_EXECUTED` to the sink in
  order. Plain `TempDir` (no git repo → no stray `GIT_COMMITTED`), channel
  buffer 16, 5s watchdog per event — fast and deterministic.
- No-dupe check: existing `TestHandlerEmits*` read the internal queue, none
  pins the `SetEventSink` end-to-end path for all three endpoints.

## Decisions

- See `docs/decisions/ADR-099-interceptor-hookup-regression.md` (regression
  test over reimplementation; sink-channel over queue reads).

## Verification

- `gofmt -w` touched files — clean.
- `go build ./...` — OK.
- `go vet ./internal/daemon/` — clean.
- New test + full `go test -count=1 ./internal/daemon/` — PASS
  (one transient `TestMatchesCandidateIdentity` flake on first full run;
  passes in isolation and on re-run; harvester-owned, untouched by this change).
