# ISSUE-3 — [Phase 1] Workspace Daemon Core & File Sandbox

- **Status:** Done
- **Scope:** `internal/daemon/daemon.go`, `fileops.go`, `gitops.go`,
  `commands.go` (+ `daemon_test.go`, `fileops_test.go`, `gitops_test.go`,
  `commands_test.go`) and `docs/`. Untouched per constraints:
  `interceptor.go`, `watcher.go`, `harvester.go`, `processor.go`.
- **Refs:** `implementation-plan.md` §1.3 table; `docs/decisions/ADR-003-daemon-core-sandbox.md`.

## Decisions

- Stdlib only (`net/http`, `os/exec` argv-only, `crypto/rand`) — no `go.mod` changes.
- Lexical sandbox (Clean + HasPrefix) exactly per plan + rooted-path guard for Windows parity.
- `scan.NeverPatterns` fail-closed on path and content (403).
- `/healthz` open (liveness probes); all other routes Bearer-authenticated (401).
- Register/heartbeat no-op when `ServerURL == ""` (offline/test friendly).

## Verification

Isolated module (issue-#3 files + `internal/scan` only; repo tree is broken
by siblings' in-flight files — `EventSink`/`Project` redeclarations,
`TestInterceptDiffLines` failure — all out of scope, not touched):

- `go build ./...` → exit 0
- `go vet ./internal/daemon/` → exit 0
- `go test ./internal/daemon/ -count=1 -v` → 17/17 PASS, including:
  - traversal → 403 (`../../etc/passwd`, `/etc/passwd`, `C:\Windows\…`,
    sibling-prefix, `..` variants)
  - no/wrong token → 401; non-allowlisted (`rm`, `go run`, `curl`, `sh`) → 400
  - secret content/path → 403; >1MB read → 413; malicious git `ref` → 400
  - register + heartbeat POSTs verified against `httptest` server

## Follow-ups

- #19 (security): symlink-escape hardening, token rotation, audit logging.
- #8 (server): 90s-offline marking consuming these heartbeats.
- Orchestrator: reconcile `interceptor.go`/`harvester.go` `EventSink`
  collision and `internal/context` `Project` collision from parallel agents.
