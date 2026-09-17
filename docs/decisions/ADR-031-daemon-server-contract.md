# ADR-031 — Daemon ↔ Server Contract Repair

- **ADR ID:** ADR-031-daemon-server-contract
- **Date:** 2026-09-17
- **Author:** issue-31 subagent
- **Issue:** #31 Daemon Register/Heartbeat contract mismatch (`internal/daemon/`)
- **Status:** Accepted

## Context

The daemon (`internal/daemon/daemon.go`, issue #3) and the central API
(`internal/server/routes.go`, issue #8) disagreed on the wire contract:

- Register sent `{machine_id, path, branch, commit, daemon_url}` but the
  server requires `{project_id, user_id, machine_id, path, branch,
  commit_sha, daemon_url}` (400 otherwise) and rejects unknown fields.
- Heartbeat sent `{machine_id, path, ...}` but the server requires
  `{workspace_id, branch, commit_sha, is_dirty}`.
- The returned workspace ID was never persisted, so every restart
  re-registered and heartbeats could not survive restarts.
- Heartbeat errors were swallowed (`_ = d.HeartbeatOnce`), hiding outages.
- `DaemonURL` advertised the listen spec (`d.Addr`, e.g. `127.0.0.1:0`)
  instead of the bound listener address, which is never dialable.

Constraints: only `internal/daemon/daemon.go` + `internal/daemon/daemon_test.go`
may change; daemon stays stdlib-only, so it must NOT import
`internal/store` (or `internal/project`); cross-platform (`filepath`, 0600
modesUnix-enforced); `ServerURL == ""` stays a local-only no-op.

## Options Considered

1. **Daemon resolves project/user and persists workspace ID (chosen)** —
   daemon derives `(origin, root_commit)` via git (same concepts as
   `project.Fingerprint`), POSTs `/projects/resolve` first, resolves
   `user_id` from `Daemon.UserID` or `CENTRAL_USER_ID`/`NEXUS_USER_ID`/
   `USER_ID`, sends the exact server field names, persists the returned
   workspace ID in memory + `<root>/.central-memory/daemon.workspace`
   (0600), heartbeats `{workspace_id,...}`, logs errors with exponential
   backoff, and derives `DaemonURL` from the bound address.
2. **Change the server to accept the daemon's old shapes** — rejected: the
   server mirrors the Postgres CHECKs/NOT NULLs (plan §§1.1–1.3) and its
   validation is covered by 15 tests; weakening it would push NULLs into
   `workspaces.project_id/user_id` and fork the contract.
3. **Import `internal/store` / `internal/project` in the daemon** —
   rejected: violates the stdlib-only daemon constraint and creates an
   import cycle risk (store already imports project); the two git one-liners
   are trivial to replicate daemon-side.

## Decision

Option 1, implemented entirely in `internal/daemon/daemon.go`:

- `RegisterRequest` → `{project_id, user_id, machine_id, path, branch,
  commit_sha, is_dirty, daemon_url}`; `HeartbeatRequest` → `{workspace_id,
  branch, commit_sha, is_dirty}` (no unknown fields).
- `Register` resolves `user_id` (field → env), fingerprints the repo
  (`git remote get-url origin`, `git rev-list --max-parents=0 HEAD`,
  best-effort), POSTs `/projects/resolve` → `project_id`, POSTs
  `/workspaces/register`, persists the returned ID via `setWorkspaceID`
  (memory + file, 0600).
- `HeartbeatOnce` sends `{workspace_id,...}`; errors when unregistered.
- `StartHeartbeatLoop` logs every failure (`log.Printf`) and backs off
  exponentially (`heartbeatBackoff`: 30s doubling, 5m cap, reset on success).
- `DaemonURL()` reads the bound address (`srv.Addr` set by `Start`) with
  fallback to the listen spec; `New` reloads the persisted workspace ID.

## Why (Rationale)

- **Contract fidelity:** payload structs are field-for-field identical to
  `registerRequest`/`heartbeatRequest` in `internal/server/routes.go`,
  including `commit_sha` (not `commit`) and no `machine_id`/`path` on
  heartbeat; the strict fake in `daemon_test.go` enforces
  `DisallowUnknownFields` + required-field checks mirroring the server, plus
  negative cases (missing `user_id`, unregistered heartbeat, legacy
  `{machine_id,path}` → 400, `commmit_sha` typo → 400).
- **No new deps / no store import:** only stdlib (`log` added); fingerprint
  reuses the package's own `runGit`, never `internal/store`.
- **Local-first preserved:** `ServerURL == ""` still short-circuits before
  any resolution or dial.
- **Evidence:** `go build ./...` exit 0, `go vet ./internal/daemon/` exit 0,
  `go test ./internal/daemon/ -run 'TestRegister|TestHeartbeat'` green and
  full package green (see ISSUE-31.md).

## Consequences

- New file `<root>/.central-memory/daemon.workspace` (0600) alongside the
  token; operators must preserve it across restarts (else re-register).
- `Daemon.UserID` / `CENTRAL_USER_ID` (aliases `NEXUS_USER_ID`, `USER_ID`)
  is now required for registration; `deploy/daemon-bootstrap` should export
  it (follow-up, out of scope — bootstrap was not touched).
- Heartbeat failures now appear in logs with retry delays instead of silent
  drops; alerting can grep `daemon: heartbeat:`.

## Alternatives Rejected

- Option 2: server weakening breaks DB invariants and its test suite.
- Option 3: store/project imports break daemon layering for two shell lines.
