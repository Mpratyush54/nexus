# ADR-043 — Platform CLI Hook (`daemon install/uninstall/status`)

- **ADR ID:** ADR-043-platform-cli-hook
- **Date:** 2026-09-17
- **Author:** CLI owner (fix/audit-gofmt-49)
- **Issue:** #43 `nexus daemon install/uninstall` unreachable; main.go serves only projects/status
- **Status:** Accepted

## Context

`main.go` dispatched only `projects` and `status`; the `internal/platform/`
hooks from issue #24 (`platform.Install` / `platform.Uninstall` /
`platform.ServiceStatus` + `DefaultDaemonArgs()`) had no CLI entry point,
so `nexus daemon install/uninstall` (and `daemon status`) were unreachable.
Constraints for this fix: touch only `main.go` + `docs/`; do NOT touch
`internal/platform/`; follow the existing `cmdProjects`/`cmdStatus` style;
update usage text.

## Options Considered

1. **Thin `daemon` subcommand dispatch in `main.go`** — `case "daemon":`
   → `cmdDaemon(os.Args[2:])`, switching on `install|uninstall|status`
   and delegating to the platform hooks with the ADR-024 snippet
   semantics (`Install("", DefaultDaemonArgs())`, `Uninstall()`,
   `ServiceStatus()` + print). No flag parsing, no new deps.
2. **Full flag-parsing daemon CLI (`--port`, `daemon run`, etc.)** — builds
   the planned `mem daemon [--port PORT]` runtime now, but that belongs to
   the daemon-runtime issue and expands this fix beyond #43's scope.

## Decision

Option 1. `main.go` now exposes:

- `mem daemon install` → `platform.Install("", platform.DefaultDaemonArgs())`
- `mem daemon uninstall` → `platform.Uninstall()`
- `mem daemon status` → `platform.ServiceStatus()` (prints
  `daemon service: <status>`)
- bare/unknown `daemon` subcommand → daemon usage + non-zero error
  (no crash, no fallthrough to the top-level unknown-command path)
- top-level `usage()` lists the three daemon subcommands as implemented;
  the `mem daemon [--port PORT]` runtime line is removed from Planned
  (the remaining Planned lines — memory, sessions — are untouched).

## Why (Rationale)

- **Unblocks #24's contract:** ADR-024 §Consequences defines the exact hook
  snippet and states `main.go` wiring is owned by the CLI issue; this fix
  applies that snippet verbatim, so platform and CLI agree with zero drift.
- **Minimal blast radius:** only `main.go` + `docs/` change; the
  `internal/platform/` package (with its per-OS managers and pure-builder
  tests) is untouched, so no cross-platform or reinstall risk.
- **Style consistency:** `cmdDaemon(args []string) error` mirrors
  `cmdProjects`/`cmdStatus` (thin wrapper, real work in the owning
  package, errors bubble to `main`'s single `error:` exit path).
- **Note on the issue text:** it quotes `platform.Uninstall("")` and
  `platform.ServiceStatus("")`, but the real signatures are
  `Uninstall()` and `ServiceStatus()` (no args); the fix uses the real
  signatures per `internal/platform/platform.go` and the ADR-024 snippet.
- **Evidence:** `go build ./...` exit 0; `go vet .` exit 0;
  `go run . daemon status` exits 0 and prints `daemon service: ...`
  (see `docs/issues/ISSUE-43.md`).

## Consequences

- `nexus daemon install/uninstall/status` are now reachable on all OSes;
  per-OS behavior (schtasks / launchd / systemd) is unchanged.
- Follow-ups (out of scope): `daemon run/--port` runtime, health-gated
  supervision, log rotation → future daemon/platform issues.

## Alternatives Rejected

- Option 2: premature runtime work; expands scope and duplicates the
  daemon-runtime issue's ownership.
