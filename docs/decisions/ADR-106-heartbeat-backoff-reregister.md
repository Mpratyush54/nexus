# ADR-106 — Heartbeat Backoff Enforcement + Registration Recovery

- **ADR ID:** ADR-106-heartbeat-backoff-reregister
- **Date:** 2026-09-17
- **Issue:** #106 audit: fixed-ticker heartbeat ignores backoff; unregistered
  daemons 404-loop forever (`internal/daemon/`)
- **Status:** Accepted

## Context

`StartHeartbeat` computed `heartbeatBackoff(failures)` only for a log line
while a fixed `time.NewTicker(interval)` kept firing at full rate against a
down server. Worse, a daemon that never registered (server down at startup)
— or whose workspace ID went stale — received 404 on every heartbeat and
never retried `Register`, a permanent failure with no recovery path.
Signatures of `HeartbeatOnce`, `heartbeatBackoff`, and `Register` are pinned
by existing tests and must not change.

## Options Considered

1. **Timer-reset + register-on-unknown-registration (chosen)** — replace the
   ticker with a `time.Timer` reset to the backoff delay after every beat;
   when no workspace ID is known, or a heartbeat error contains
   `"register first"`/`"404"`, call `Register()` (success resets failures
   and persists the ID; failure increments them). Minimal diff, no signature
   churn, no new goroutines.
2. **Full lifecycle state machine** (`UNREGISTERED → REGISTERING →
   HEARTBEATING → RECONNECTING`, as the issue suggests) — rejected: heavier
   machinery for the same observable behavior; the register-vs-beat branch
   inside `beat()` already encodes those states.
3. **Raw `heartbeatBackoff` as the timer delay** — rejected for tests only:
   at production scale it is exactly right, but tests could not exercise it
   without 30s+ sleeps. `heartbeatDelay` scales proportionally for small
   injected bases and is identical at `HeartbeatInterval`.

## Decision

Implement option 1 with `isUnknownRegistration` (substring match — `postJSON`
404s surface as `"server status 404"`) and `heartbeatDelay` helpers in
`internal/daemon/daemon.go`; cover with four httptest-backed tests using
20ms injected intervals (register-when-unregistered, re-register-on-404,
delay scaling incl. cap, bounded-attempts backoff proof).

## Consequences

- Failure storms back off (30s → 5m cap in production) instead of hot-looping.
- Daemons starting before the server self-heal once it appears; stale IDs
  re-register transparently.
- 404-substring matching could false-positive on exotic messages containing
  "404" — acceptable: re-registering is idempotent and self-correcting.
- `Register` with no server URL stays a no-op, so offline use is unaffected.
