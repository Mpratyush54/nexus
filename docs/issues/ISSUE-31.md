# ISSUE-31 — [Phase 1] Daemon ↔ Server Contract Repair

- **Issue:** #31 — Daemon Register/Heartbeat contract mismatch
- **Status:** Done (implementation + tests + docs; uncommitted per task)
- **Scope constraint:** ONLY `internal/daemon/daemon.go`,
  `internal/daemon/daemon_test.go`, `docs/decisions/ADR-031-daemon-server-contract.md`,
  `docs/issues/ISSUE-31.md`. Did NOT touch `internal/server/`,
  `internal/store/`, `internal/project`, `deploy/`, or any other package.
- **Refs:** `implementation-plan.md` §§1.2–1.3; `internal/server/routes.go`
  (`registerRequest`, `heartbeatRequest`); `docs/issues/ISSUE-3.md`;
  `docs/issues/ISSUE-8.md`; `docs/decisions/ADR-031-daemon-server-contract.md`.

## Problem

Daemon `Register` sent `{machine_id, path, branch, commit, daemon_url}`
but the server requires `{project_id, user_id, machine_id, path, branch,
commit_sha, daemon_url}`; heartbeat sent `{machine_id, path}` but the
server wants `{workspace_id, branch, commit_sha, is_dirty}` (both with
unknown-field rejection → 400). The returned workspace ID was never
persisted, heartbeat errors were swallowed, and `DaemonURL` advertised the
listen spec (`127.0.0.1:0`) instead of the bound address.

## What was built

| File | Contents |
|---|---|
| `internal/daemon/daemon.go` | Exact server-matching `RegisterRequest`/`HeartbeatRequest` (`commit_sha`, no legacy fields); `resolveRequest` + `resolveProjectID` (POST `/projects/resolve` first, fingerprint via `runGit` origin/root-commit daemon-side, no `internal/store` import); `effectiveUserID` (`Daemon.UserID` → `CENTRAL_USER_ID`/`NEXUS_USER_ID`/`USER_ID`); `WorkspacePath`/`LoadWorkspaceID`/`SaveWorkspaceID` (0600) + `WorkspaceID` field (reloaded in `New`, cached+persisted in `setWorkspaceID`); `DaemonURL()` from bound `srv.Addr`; `postJSON` helper; `Register` (resolve → register → persist) and `HeartbeatOnce` (`{workspace_id,...}`, errors when unregistered); `heartbeatBackoff` (30s doubling, 5m cap) + logging `StartHeartbeatLoop` |
| `internal/daemon/daemon_test.go` | Strict `DisallowUnknownFields` fake mirroring server validation; `TestRegisterHeartbeatAgainstServer` (resolve-first, `project_id` from resolve, `user_id`, bound-addr `daemon_url`, memory+file 0600 persistence, heartbeat `{workspace_id}` with no legacy keys); negatives `TestRegisterRequiresUserID`, `TestHeartbeatWithoutRegisterFails`, `TestHeartbeatRejectsLegacyShape` (legacy `{machine_id,path}` → 400, `commmit_sha` typo → 400); `TestRegisterResolvesUserIDFromEnv`, `TestHeartbeatSendsPersistedIDAfterRestart`, `TestDaemonURLFromBoundAddr`, `TestHeartbeatBackoff`, `TestWorkspaceIDRoundTrip0600` |
| `docs/decisions/ADR-031-daemon-server-contract.md` | Why-mandatory ADR (options, rationale, consequences) |
| `docs/issues/ISSUE-31.md` | This file |

## Decisions (see ADR-031 for rationale)

1. Daemon-side fingerprint (two `runGit` one-liners), never import
   `internal/store`/`internal/project` — keeps the daemon stdlib-only.
2. `user_id` from explicit field first, then env — `New` keeps its
   3-arg signature so `deploy/daemon-bootstrap` still builds untouched.
3. Workspace ID in memory + `daemon.workspace` (0600) next to the token.
4. Heartbeat loop logs + exponential backoff instead of swallowing errors.
5. `DaemonURL` from the bound listener address, not the listen spec.

## Verification (2026-09-17, go1.27.0, windows/amd64)

- `go build ./...` → exit 0
- `go vet ./internal/daemon/` → exit 0, no findings
- `go test ./internal/daemon/ -run TestRegister -count=1 -v` → 4/4 PASS
  (`TestRegisterHeartbeatLocalNoop`, `TestRegisterHeartbeatAgainstServer`,
  `TestRegisterRequiresUserID`, `TestRegisterResolvesUserIDFromEnv`)
- `go test ./internal/daemon/ -run TestHeartbeat -count=1 -v` → 4/4 PASS
  (`TestHeartbeatWithoutRegisterFails`, `TestHeartbeatRejectsLegacyShape`,
  `TestHeartbeatSendsPersistedIDAfterRestart`, `TestHeartbeatBackoff`)
- `go test ./internal/daemon/ -count=1` → ok (full package green)

## Follow-ups (not this issue)

- `deploy/daemon-bootstrap/main.go`: export `CENTRAL_USER_ID` (and
  optionally wire `d.UserID`) so production registration has a user.
- Server auth on daemon endpoints (JWT for `/projects/resolve`,
  `/workspaces/*`) — currently open in tests via httptest.
- Token/workspace rotation + audit logging (issue #19 security track).
