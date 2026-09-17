# ADR-003 — Workspace Daemon Core & File Sandbox

- **ADR ID:** ADR-003-daemon-core-sandbox
- **Date:** 2026-09-17
- **Author:** issue-3 subagent
- **Issue:** #3 [Phase 1] Workspace Daemon Core & File Sandbox (`internal/daemon/`)
- **Status:** Accepted

## Context

Phase 1 needs the workspace daemon core per `implementation-plan.md` §1.3:
HTTP server, bearer-token auth, sandboxed file read/write, git status/diff,
allowlisted command execution, plus registration and a 30s heartbeat loop
against the central server. Constraints: work only in `daemon.go`,
`fileops.go`, `gitops.go`, `commands.go` (+ tests) and `docs/`; stdlib only;
cross-platform (`filepath`, no hardcoded `D:\` paths); reuse
`internal/scan.NeverPatterns` for secret rejection. Sibling agents own
`interceptor.go`, `watcher.go`, `harvester.go`, `processor.go`.

## Options Considered

1. **Stdlib `net/http` + `os/exec` (argv, no shell), lexical sandbox
   (`filepath.Clean` + `HasPrefix`)** — zero new dependencies, matches the
   plan text exactly, portable.
2. **Third-party router (chi/mux) + shell-based git/commands** — nicer
   routing, but adds deps against the zero-dep `go.mod` and shells invite
   injection.
3. **Symlink-resolving sandbox (`EvalSymlinks` on every path)** — stronger,
   but racy (TOCTOU) without openat-style APIs Go lacks on Windows, and the
   plan prescribes Clean + HasPrefix.

## Decision

Option 1. `internal/daemon/` exposes:

- `daemon.go` — `Daemon` struct; 32-byte `crypto/rand` token (hex, 64 chars)
  at `<root>/.central-memory/daemon.token` mode `0600`; constant-time Bearer
  middleware (401); routes `POST /file/read`, `POST /file/write`,
  `GET /git/status`, `GET /git/diff`, `POST /command/run`, open `GET
  /healthz`; `Register()` + `HeartbeatOnce()` + `StartHeartbeatLoop()`
  (30s ticker, first beat immediate; no-op when `ServerURL == ""`).
- `fileops.go` — `ResolveInSandbox` (Clean + root+separator HasPrefix;
  extra rooted-path guard, see below); `scan.NeverPatterns` fail-closed on
  path and content; 1MB read cap (413); traversal/secret → 403.
- `gitops.go` — `git status --porcelain`, `git diff [--no-color [ref]]`
  with strict ref validation (`^[A-Za-z0-9][A-Za-z0-9/_.~^-]*$`, ≤128
  chars, no leading `-`); git never runs under a shell.
- `commands.go` — exact allowlist: `git` (any argv), `go test`, `npm
  test`, `pytest`, `cargo test`; `os/exec` argv-only, Dir pinned to root,
  60s timeout (504), 32KB output cap; non-allowlisted → 400.

## Why (Rationale)

- **Plan fidelity:** every behavior maps to the §1.3 table (endpoints,
  30s heartbeat fields, 0600 token, 1MB cap, 60s timeout, exact allowlist).
  Acceptance criteria verified by tests: traversal → 403
  (`TestResolveInSandboxAdversarial`, `TestFileHTTPStatuses` including
  `../../etc/passwd`, absolute escapes, sibling-prefix attack), no token →
  401 (`TestAuthNoToken401`, `TestAuthWrongToken401`), non-allowlisted →
  400 (`TestCommandRunHTTPAllowlist`, `TestIsAllowedCommand`).
- **Stdlib-only keeps `go.mod` untouched** (no dependency review needed;
  sibling agents are adding pgx/fsnotify concurrently — zero conflict risk).
- **Cross-platform:** a Windows-specific escape was found by test —
  `/etc/passwd` is *not* `IsAbs` on Windows (rooted, no volume), so it
  would have been confined rather than rejected. Added an explicit
  rooted-path rejection so behavior is identical on all OSes. The 0600 mode
  is passed through on Unix; on Windows Go ignores mode bits (ACLs apply),
  so the perm assertion is Unix-only by design.
- **Evidence:** isolated-module verify (repo tree currently broken by
  sibling agents' in-flight files — see Consequences):
  `go build ./...` exit 0, `go vet ./internal/daemon/` exit 0,
  `go test ./internal/daemon/ -count=1` — 17/17 PASS.

## Consequences

- `go build ./...` **in the repo tree fails at time of writing** due to
  other agents' files (`interceptor.go` vs `harvester.go`: `EventSink`
  redeclared; `internal/context/builder.go`: `Project` redeclared) plus a
  failing `TestInterceptDiffLines`. None are in issue-#3 scope; not touched
  per constraints. Orchestrator must reconcile. Clean evidence above comes
  from an isolated module containing only issue-#3 files + `internal/scan`.
- Follow-ups (out of scope): symlink-escape hardening + `daemon.token`
  rotation (→ issue #19 security); fsnotify watcher, harvester, processor
  owned by sibling issues; server-side 90s-offline marking (→ issue #8).

## Alternatives Rejected

- Option 2: dependency + shell-injection cost for no acceptance benefit.
- Option 3: false strength (TOCTOU on Windows) and plan deviation;
  deferred to #19 with a proper threat model.
