// Package platform abstracts OS-specific daemon service management.
//
// Issue #24 (Cross-Platform Daemon Service Management) owns this package.
// It provides auto-start-on-login for the nexus workspace daemon on all
// three supported operating systems behind a single Manager interface:
//
//   - Windows: Task Scheduler task (schtasks /SC ONLOGON)
//   - macOS:   launchd user agent (~/Library/LaunchAgents + launchctl)
//   - Linux:   systemd user unit (~/.config/systemd/user + systemctl --user)
//
// Design rules for this package:
//
//   - No hardcoded drive letters or user paths anywhere. All app
//     directories derive from os.UserConfigDir / os.UserCacheDir /
//     os.UserHomeDir at runtime, with the application suffix appended by
//     the pure helpers ConfigDirForBase / CacheDirForBase (unit-tested
//     with synthetic per-GOOS bases; no host filesystem dependency).
//   - All unit-file / command-line generation is done by pure cross-
//     platform builder functions (LaunchdPlist, SystemdUnit,
//     WindowsTaskCommand, SchtasksCreateArgs) so `go test` verifies the
//     per-OS artifacts on every host without installing anything.
//   - The thin per-OS managers (windows.go, darwin.go, linux.go,
//     unsupported.go) only perform filesystem writes + os/exec calls and
//     are never exercised by unit tests (no actual service install).
//   - CLI hooks (`nexus daemon install` / `nexus daemon uninstall`) are
//     exposed as package-level Install / Uninstall / Status functions.
//     main.go is owned by another issue and MUST NOT be edited here; the
//     exact hook snippet is documented in ADR-024 instead.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppName is the application directory suffix appended to the OS config
// and cache base directories (e.g. %APPDATA%/nexus, ~/.config/nexus,
// ~/Library/Application Support/nexus).
const AppName = "nexus"

// ServiceName is the canonical daemon service name used where the
// platform accepts a free-form name (systemd unit stem).
const ServiceName = "nexus"

// LaunchdLabel returns the launchd service label (reverse-DNS).
func LaunchdLabel() string { return "com." + AppName + ".daemon" }

// SystemdUnitName returns the systemd user unit file name.
func SystemdUnitName() string { return ServiceName + ".service" }

// WindowsTaskName returns the Task Scheduler task name.
func WindowsTaskName() string { return "NexusDaemon" }

// Status describes the daemon service state.
type Status string

const (
	// StatusUnknown means the state could not be determined.
	StatusUnknown Status = "unknown"
	// StatusRunning means the service is installed and running.
	StatusRunning Status = "running"
	// StatusStopped means the service is installed but not running.
	StatusStopped Status = "stopped"
	// StatusNotInstalled means no service definition was found.
	StatusNotInstalled Status = "not-installed"
)

// Manager installs, removes and inspects the daemon auto-start service.
// Implementations live in the per-OS files (windows.go, darwin.go,
// linux.go, unsupported.go) behind build tags.
type Manager interface {
	// Install writes the service definition for exePath (plus args) and
	// enables auto-start on login. An empty exePath resolves to the
	// current executable via resolveExe.
	Install(exePath string, args []string) error
	// Uninstall disables auto-start and removes the service definition.
	// Uninstalling a service that is not installed MUST succeed (nil).
	Uninstall() error
	// Status reports the current service state without mutating anything.
	Status() (Status, error)
}

// Current returns the Manager for the OS this binary was built for.
func Current() Manager { return currentManager() }

// Install registers auto-start-on-login for the daemon using the
// current-OS manager. It is the `nexus daemon install` hook implementation
// (see ADR-024 for the main.go wiring owned by the CLI issue).
func Install(exePath string, args []string) error {
	return currentManager().Install(exePath, args)
}

// Uninstall removes the daemon auto-start service (`nexus daemon uninstall`).
func Uninstall() error { return currentManager().Uninstall() }

// ServiceStatus reports the daemon service state (`nexus daemon status`).
func ServiceStatus() (Status, error) { return currentManager().Status() }

// DefaultDaemonArgs are the arguments appended to the daemon executable
// when no explicit args are given: the daemon subcommand from main.go's
// planned CLI (`nexus daemon [--port PORT]`).
func DefaultDaemonArgs() []string { return []string{"daemon"} }

// ---------------------------------------------------------------------------
// App directories (no hardcoded paths)
// ---------------------------------------------------------------------------

// ConfigDir returns the per-user configuration directory for nexus:
//
//   - Windows: %APPDATA%/nexus      (via os.UserConfigDir)
//   - macOS:   ~/Library/Application Support/nexus
//   - Linux:   ~/.config/nexus (or $XDG_CONFIG_HOME/nexus)
//
// It creates nothing; callers create it as needed.
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("platform: user config dir: %w", err)
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("platform: user config dir is empty")
	}
	return ConfigDirForBase(base), nil
}

// CacheDir returns the per-user cache directory for nexus
// (%LOCALAPPDATA%/nexus, ~/Library/Caches/nexus, ~/.cache/nexus).
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("platform: user cache dir: %w", err)
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("platform: user cache dir is empty")
	}
	return CacheDirForBase(base), nil
}

// ConfigDirForBase is the pure, GOOS-independent half of ConfigDir: it
// appends the application suffix to an OS-provided base directory. Tests
// feed it synthetic per-GOOS bases (e.g. %APPDATA% on windows,
// ~/Library/Application Support on darwin, ~/.config on linux) so the
// per-GOOS path logic is verified on every host.
func ConfigDirForBase(base string) string {
	return filepath.Join(base, AppName)
}

// CacheDirForBase is the pure, GOOS-independent half of CacheDir.
func CacheDirForBase(base string) string {
	return filepath.Join(base, AppName)
}

// ConfigFilePath returns <configDir>/config.json (pure path join).
func ConfigFilePath(configDir string) string {
	return filepath.Join(configDir, "config.json")
}

// ---------------------------------------------------------------------------
// Executable resolution (no hardcoded paths)
// ---------------------------------------------------------------------------

// resolveExe returns exePath cleaned to an absolute path, or the current
// process executable when exePath is blank.
func resolveExe(exePath string) (string, error) {
	if strings.TrimSpace(exePath) == "" {
		self, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("platform: locate executable: %w", err)
		}
		return filepath.Clean(self), nil
	}
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return "", fmt.Errorf("platform: resolve executable path: %w", err)
	}
	return filepath.Clean(abs), nil
}

// ---------------------------------------------------------------------------
// Pure service-definition builders (cross-platform, unit-tested)
// ---------------------------------------------------------------------------

// xmlEscape escapes a string for embedding in launchd plist XML.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// quoteArg quotes a single command-line argument when it contains
// whitespace or double quotes (used for schtasks /TR and systemd
// ExecStart, neither of which runs through a shell).
func quoteArg(a string) string {
	if a == "" {
		return `""`
	}
	if !strings.ContainsAny(a, " \t\"") {
		return a
	}
	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// commandLine joins an executable path and args into a shell-free command
// line with quoting applied per argument.
func commandLine(exePath string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(exePath))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// WindowsTaskCommand builds the schtasks /TR command string that launches
// the daemon (pure; the schtasks invocation itself lives in windows.go).
func WindowsTaskCommand(exePath string, args []string) string {
	return commandLine(exePath, args)
}

// SchtasksCreateArgs builds the `schtasks /Create` argument vector for a
// logon-triggered task (pure; executed by windows.go).
func SchtasksCreateArgs(taskName, command string) []string {
	return []string{
		"/Create",
		"/TN", taskName,
		"/TR", command,
		"/SC", "ONLOGON",
		"/F",
	}
}

// SchtasksDeleteArgs builds the `schtasks /Delete` argument vector (pure).
func SchtasksDeleteArgs(taskName string) []string {
	return []string{"/Delete", "/TN", taskName, "/F"}
}

// SchtasksQueryArgs builds the `schtasks /Query` argument vector used by
// Status (pure).
func SchtasksQueryArgs(taskName string) []string {
	return []string{"/Query", "/TN", taskName, "/FO", "LIST", "/V"}
}

// ParseSchtasksStatus maps `schtasks /Query /FO LIST` output to a Status.
// Missing-task output (matches "could not be found" / "cannot find")
// maps to StatusNotInstalled with ok=true. Unrecognized output returns
// ok=false so the caller can surface StatusUnknown.
func ParseSchtasksStatus(output string) (Status, bool) {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "could not be found") ||
		strings.Contains(lower, "cannot find") ||
		strings.Contains(lower, "the system cannot find") {
		return StatusNotInstalled, true
	}
	for _, line := range strings.Split(output, "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "status") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "running":
			return StatusRunning, true
		case "ready", "disabled":
			return StatusStopped, true
		}
	}
	return StatusUnknown, false
}

// LaunchdPlist renders the launchd agent plist that starts the daemon at
// login and keeps it alive (pure; written to
// ~/Library/LaunchAgents/<label>.plist by darwin.go).
func LaunchdPlist(label, exePath string, args []string) string {
	programArgs := make([]string, 0, len(args)+1)
	programArgs = append(programArgs, "    <string>"+xmlEscape(exePath)+"</string>")
	for _, a := range args {
		programArgs = append(programArgs, "    <string>"+xmlEscape(a)+"</string>")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n" +
		`<plist version="1.0">` + "\n" +
		`<dict>` + "\n" +
		`  <key>Label</key>` + "\n" +
		`  <string>` + xmlEscape(label) + `</string>` + "\n" +
		`  <key>ProgramArguments</key>` + "\n" +
		`  <array>` + "\n" +
		strings.Join(programArgs, "\n") + "\n" +
		`  </array>` + "\n" +
		`  <key>RunAtLoad</key>` + "\n" +
		`  <true/>` + "\n" +
		`  <key>KeepAlive</key>` + "\n" +
		`  <true/>` + "\n" +
		`</dict>` + "\n" +
		`</plist>` + "\n"
}

// SystemdUnit renders the systemd --user unit that starts the daemon at
// login (pure; written to ~/.config/systemd/user/nexus.service by
// linux.go and enabled via `systemctl --user enable --now`).
func SystemdUnit(description, exePath string, args []string) string {
	if strings.TrimSpace(description) == "" {
		description = "nexus workspace daemon"
	}
	return "[Unit]\n" +
		"Description=" + description + "\n" +
		"After=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + commandLine(exePath, args) + "\n" +
		"Restart=always\n" +
		"RestartSec=5\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}
