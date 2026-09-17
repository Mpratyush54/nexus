# ISSUE-24 — Cross-Platform Daemon Service Management

- **Status:** Done
- **Assignee:** ParthKhandelwal537
- **Branch:** `feat/wave-4-parth-20-22-24-26`
- **Scope:** `internal/platform/` (`platform.go`, `windows.go`,
  `darwin.go`, `linux.go`, `unsupported.go`, `platform_test.go`) and
  `docs/`. Untouched per constraints: `main.go` (CLI wiring documented
  as a hook in ADR-024 only), `internal/daemon/daemon.go` (read-only
  reference), all other packages.
- **Refs:** `implementation-plan.md` Target Structure (`internal/platform`
  + `internal/daemon/daemon.go`); `docs/decisions/ADR-024-daemon-platform-service.md`.

## Decisions

- One `Manager` interface (`Install`/`Uninstall`/`Status`) with native
  per-OS backends: Task Scheduler (`schtasks /SC ONLOGON`) on Windows,
  launchd agent (`RunAtLoad` + `KeepAlive`, `launchctl bootstrap`) on
  macOS, systemd user unit (`WantedBy=default.target`,
  `systemctl --user enable --now`) on Linux.
- Paths via `os.UserConfigDir`/`os.UserCacheDir` + `"nexus"` suffix
  (`%APPDATA%/nexus`, `~/Library/Application Support/nexus`,
  `~/.config/nexus`); no hardcoded drive letters (guarded by test).
- All unit-file/command builders are pure cross-platform funcs so tests
  verify every OS artifact on any host with zero live installs.
- Build tags: `windows.go` (`//go:build windows`), `darwin.go`
  (`//go:build darwin`), `linux.go` (`//go:build linux`),
  `unsupported.go` (`//go:build !windows && !darwin && !linux`).
- CLI hooks exposed as `platform.Install` / `platform.Uninstall` /
  `platform.ServiceStatus` + `DefaultDaemonArgs()`; `main.go` deliberately
  unedited (see ADR-024 hook snippet for the CLI owner).

## Verification

All run 2026-09-17 on Windows host (`go1.27.0 windows/amd64`):

- `go build ./...` → exit 0
- `GOOS=windows go build ./internal/platform/` → exit 0
- `GOOS=darwin go build ./internal/platform/` → exit 0
- `GOOS=linux go build ./internal/platform/` → exit 0
- `go vet ./internal/platform/` → exit 0 (plus `go vet ./...` → exit 0)
- `go test ./internal/platform/ -count=1 -v` → 16/16 PASS:
  - per-GOOS config/cache dirs via pure funcs + synthetic bases
    (`TestConfigDirForBasePerGOOS`, `TestCacheDirForBasePerGOOS`)
  - plist generation + XML escaping, systemd unit + spaced-path quoting
  - schtasks arg vectors + output parser (running/ready/missing/garbage)
  - zero hardcoded `D:\` paths in non-test sources
    (`TestNoHardcodedDrivePaths`)
  - no test performs an actual service install (only `os/exec`
    managers, never invoked by tests)

## Follow-ups

- CLI owner: wire `nexus daemon install/uninstall/status` in `main.go`
  per the ADR-024 snippet (out of this issue's ownership).
- Future platform issues: log rotation under `CacheDir`, health-gated
  supervision, upgrade/uninstall semantics.
- Security (#19): `daemon.token` rotation is orthogonal to service
  registration and stays with #19.
