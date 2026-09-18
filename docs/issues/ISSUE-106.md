# ISSUE-106 — Heartbeat backoff + registration recovery (REAL)

- **Issue:** #106 — `StartHeartbeat` ran on a fixed ticker
  (`heartbeatBackoff` only fed a log line) and never re-registered, so a
  daemon starting before the server 404-looped heartbeats forever.
- **Status:** Done (implementation + tests + docs; uncommitted per task).
- **Scope constraint:** ONLY `internal/daemon/daemon.go`,
  `internal/daemon/daemon_test.go`,
  `docs/decisions/ADR-106-heartbeat-backoff-reregister.md`, this file.
  `HeartbeatOnce`, `heartbeatBackoff`, `Register` signatures unchanged
  (pinned by `daemon_test.go`). No git operations.

## Problem

1. Fixed `time.NewTicker(interval)` never slowed down on consecutive errors.
2. Unregistered daemons (or stale workspace IDs) got 404 on every
   `PUT /workspaces/:id/heartbeat` equivalent and never called `Register`.

## What was built

| File | Contents |
|---|---|
| `internal/daemon/daemon.go` | `StartHeartbeat` now beats via a `time.Timer` reset to `heartbeatDelay(interval, failures)` after every beat; `beat()` calls `Register()` when no workspace ID is known or the heartbeat error matches `isUnknownRegistration` (contains `"register first"` or `"404"` — the `postJSON` 404 surface is `"server status 404"`); register success resets failures (ID persisted by `Register`), failure increments them. New helpers `isUnknownRegistration` + `heartbeatDelay` (equals `heartbeatBackoff` at production scale; proportionally scaled for small injected intervals). |
| `internal/daemon/daemon_test.go` | `TestStartHeartbeatRegistersWhenUnregistered` (unregistered daemon registers then heartbeats, ID pinned); `TestStartHeartbeatReregistersOn404` (stale ID → 404 → re-register → successful heartbeat as `ws-fresh`); `TestHeartbeatDelayScalesWithInterval` (production-scale equality, small-base doubling, cap); `TestStartHeartbeatBacksOffOnRepeatedFailures` (500-loop: retries continue but stay ≤10 attempts in 300ms vs ~16 on a fixed 20ms ticker). All httptest-backed, ≤2s each. |
| `docs/decisions/ADR-106-heartbeat-backoff-reregister.md` | Why-mandatory ADR (options, rationale, consequences) |
| `docs/issues/ISSUE-106.md` | This file |

## Decisions (see ADR-106 for rationale)

1. Timer-reset over ticker — the wait must be recomputed from the failure
   count after every beat.
2. Error-substring re-register trigger (`"register first"`/`"404"`) over a
   lifecycle state machine — minimal change, no signature churn.
3. Scaled `heartbeatDelay` over raw `heartbeatBackoff` — keeps production
   timing identical while letting tests inject 20ms bases.

## Verification

- `gofmt -w` touched files — clean.
- `go build ./...` — OK.
- `go vet ./internal/daemon/` — clean.
- `go test -count=1 ./internal/daemon/` — PASS (all pre-existing
  `TestRegister*`/`TestHeartbeat*` green; one transient
  `TestMatchesCandidateIdentity` flake on first full run, green in isolation
  and on re-run — harvester-owned, untouched).
