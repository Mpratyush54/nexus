# ISSUE-43 — Wire `daemon install/uninstall/status` CLI hooks

- **Status:** Done
- **Branch:** `fix/audit-gofmt-49`
- **Scope:** `main.go` (`daemon` subcommand dispatch + usage text) and
  `docs/` (`ADR-043-platform-cli-hook.md`, this file). Untouched per
  constraints: `internal/platform/` (hooks consumed as-is).
- **Refs:** `docs/decisions/ADR-043-platform-cli-hook.md`;
  `docs/decisions/ADR-024-daemon-platform-service.md` (hook contract);
  `internal/platform/platform.go`
  (`Install`/`Uninstall`/`ServiceStatus`/`DefaultDaemonArgs`).

## Problem

`nexus daemon install/uninstall` unreachable: `main.go` served only
`projects`/`status`, so the `internal/platform/` hooks from #24 had no CLI
entry point.

## Decisions

- Added `case "daemon": err = cmdDaemon(os.Args[2:])` plus
  `cmdDaemon(args []string) error` / `cmdDaemonUsage()` in the existing
  `cmdProjects`/`cmdStatus` style (thin wrapper; errors bubble to `main`'s
  single `error:` exit path).
- Mapping (real `internal/platform` signatures, cf. ADR-024 snippet):
  - `daemon install` → `platform.Install("", platform.DefaultDaemonArgs())`
  - `daemon uninstall` → `platform.Uninstall()`
  - `daemon status` → `platform.ServiceStatus()` + print
    `daemon service: <status>`
  - bare/unknown subcommand → daemon usage + error (no crash).
- (Deviates from the issue text where it quotes `Uninstall("")` /
  `ServiceStatus("")`: those functions take no arguments; using the quoted
  forms would not compile.)
- Updated top-level `usage()` to list the three daemon subcommands as
  implemented; dropped the `mem daemon [--port PORT]` Planned line.

## Verification

Run 2026-09-17 on Windows host (`fix/audit-gofmt-49`):

- `go build ./...` → exit 0
- `go vet .` → exit 0
- `go run . daemon status` → exit 0, prints `daemon service: ...`
  (e.g. `not-installed` on a clean host); no crash, no usage error.

## Follow-ups

- `daemon run` / `--port` runtime, health-gated supervision, log rotation
  → future daemon/platform issues (out of scope here).
