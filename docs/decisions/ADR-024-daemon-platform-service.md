# ADR-024 — Cross-Platform Daemon Service Management

- **ADR ID:** ADR-024-daemon-platform-service
- **Date:** 2026-09-17
- **Author:** ParthKhandelwal537
- **Issue:** #24 Cross-Platform Daemon Service Management (`internal/platform/`)
- **Status:** Accepted

## Context

`implementation-plan.md` Target Structure requires an OS-abstraction
package (`internal/platform/`: `platform.go`, `windows.go`, `darwin.go`,
`linux.go`) so the workspace daemon auto-starts on login on all three
supported operating systems. Constraints for this issue: work only in
`internal/platform/` + `docs/`; correct `//go:build` tags; paths via
`os.UserConfigDir` / `os.UserCacheDir` (`~/.config/nexus`,
`%APPDATA%/nexus`, `~/Library/Application Support/nexus`); expose
`Install`/`Uninstall` funcs as `nexus daemon install/uninstall` hooks
**without editing `main.go`** (owned by the CLI issue); zero hardcoded
`D:\` paths; stdlib only.

## Options Considered

1. **Native per-OS autostart behind one `Manager` interface** —
   Task Scheduler (`schtasks /SC ONLOGON`) on Windows, launchd agent
   (`~/Library/LaunchAgents` + `launchctl bootstrap`) on macOS, systemd
   user unit (`~/.config/systemd/user` + `systemctl --user enable --now`)
   on Linux. Stdlib `os/exec` only.
2. **Third-party service library (e.g. kardianos/service)** — one API for
   all OSes, but adds a non-plan dependency to `go.mod` against the
   locked §1.9 list and hides the per-OS artifacts reviewers must audit.
3. **Single mechanism everywhere (e.g. Run-key / cron @reboot)** —
   portable-ish but second-class on each OS (no KeepAlive/Restart
   supervision, weaker login-session integration).

## Decision

Option 1. `internal/platform/` exposes:

- `platform.go` (no build tag, shared) — `AppName`/`ServiceName`;
  `LaunchdLabel()` (`com.nexus.daemon`), `SystemdUnitName()`
  (`nexus.service`), `WindowsTaskName()` (`NexusDaemon`); `Status`
  enum (`running`/`stopped`/`not-installed`/`unknown`); `Manager`
  interface (`Install`/`Uninstall`/`Status`); package-level
  `Install`/`Uninstall`/`ServiceStatus` delegating to
  `currentManager()`; `ConfigDir()`/`CacheDir()` via
  `os.UserConfigDir`/`os.UserCacheDir` + pure `ConfigDirForBase` /
  `CacheDirForBase`; pure builders `LaunchdPlist`, `SystemdUnit`,
  `WindowsTaskCommand`, `Schtasks{Create,Delete,Query}Args`,
  `ParseSchtasksStatus`; `DefaultDaemonArgs()` (`["daemon"]`).
- `windows.go` (`//go:build windows`) — `schtasks /Create /SC ONLOGON`,
  `/Delete`, `/Query /FO LIST` mapping (`Running`→running,
  `Ready`→stopped, missing-task→not-installed, idempotent uninstall).
- `darwin.go` (`//go:build darwin`) — plist write (`0644`) +
  `launchctl bootout` (best-effort) → `bootstrap gui/<uid>` → `enable`;
  `Status` via `launchctl print` (`state = running`).
- `linux.go` (`//go:build linux`) — unit write (`0644`) +
  `systemctl --user daemon-reload` → `enable --now`; `Status` via
  `is-active` (missing unit → not-installed).
- `unsupported.go` (`//go:build !windows && !darwin && !linux`) —
  erroring `Manager` so exotic `GOOS` builds never break `go build ./...`.
- `platform_test.go` — pure-func tests only (synthetic per-GOOS bases,
  plist/systemd/task builders, schtasks output parser, drive-letter
  guard); **no test installs anything**.

## Why (Rationale)

- **Plan fidelity:** every acceptance criterion maps to code —
  auto-start on login is `ONLOGON` / `RunAtLoad+KeepAlive` /
  `WantedBy=default.target`; paths resolve through `os.UserConfigDir`
  (which yields `%APPDATA%`, `~/Library/Application Support`,
  `~/.config` per OS) with zero drive-letter literals (guarded by
  `TestNoHardcodedDrivePaths` over all non-test sources).
- **Testability without side effects:** all per-OS artifacts are pure
  builders in the shared file, so `go test` verifies plist XML, systemd
  units and schtasks vectors on every host; the `os/exec` managers are
  never exercised by tests. Per-GOOS config-dir logic is covered via
  synthetic bases for all three OSes on any host.
- **Stdlib-only keeps `go.mod` untouched** (no dependency review;
  no conflict with sibling wave-4 agents).
- **Evidence:** `go build ./...` exit 0; `GOOS=windows|darwin|linux
  go build ./internal/platform/` all exit 0; `go vet ./...` exit 0;
  `go test ./internal/platform/ -count=1` — 16/16 PASS (see
  `docs/issues/ISSUE-24.md`).

## Consequences

- **CLI hook (main.go NOT touched — owned by the CLI issue).** The
  orchestrator/CLI owner wires the exposed hooks as follows:

  ```go
  import "central-memory/internal/platform"

  case "daemon":
      if len(os.Args) > 2 {
          switch os.Args[2] {
          case "install":
              err = platform.Install("", platform.DefaultDaemonArgs())
          case "uninstall":
              err = platform.Uninstall()
          case "status":
              var st platform.Status
              st, err = platform.ServiceStatus()
              if err == nil {
                  fmt.Println("daemon service:", st)
              }
          }
      }
  ```

  Until wired, `platform.Install/Uninstall/ServiceStatus` are callable
  library functions with identical semantics.
- Follow-ups (out of scope): health-check-gated supervision, log-file
  rotation under `CacheDir`, uninstall-on-upgrade semantics → future
  platform issues; `daemon.token` rotation → issue #19 (security).

## Alternatives Rejected

- Option 2: dependency cost for no acceptance benefit; plan §1.9 locks
  the dependency list and this package needs nothing beyond stdlib.
- Option 3: loses supervised restart (launchd `KeepAlive`, systemd
  `Restart=always`) and first-class login integration the plan requires.
