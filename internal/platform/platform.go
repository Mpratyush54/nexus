// Package platform is the cross-platform OS abstraction for the nexus
// workspace daemon (Mpratyush54/nexus#24).
//
// platform.go holds everything portable: the Service interface, XDG-style
// path helpers built on os.UserConfigDir, configurable project roots, the
// launchd/systemd text renderers (pure functions so tests run on any GOOS),
// and the Install/Uninstall/Status CLI hooks (`nexus daemon
// install|uninstall|status`) that delegate to the current OS backend.
//
// OS backends live behind build tags and only add the thin exec layer:
//   - windows.go (schtasks stub)
//   - darwin.go  (launchctl manager)
//   - linux.go   (systemctl --user manager)
//
// Stdlib only. Zero hardcoded drive letters or home directories: every path
// derives from os.UserConfigDir / os.UserHomeDir (injectable via package
// vars for tests) or from NEXUS_* env overrides.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ServiceName is the shared daemon service identity across all backends.
const ServiceName = "nexus-daemon"

// LaunchdLabel is the launchd job label (darwin backend).
const LaunchdLabel = "com.nexus.daemon"

// SystemdUnitName is the systemd user unit name (linux backend).
const SystemdUnitName = "nexus-daemon.service"

// SchtasksName is the Task Scheduler task name (windows backend).
const SchtasksName = "nexus-daemon"

// ServiceStatus describes whether the daemon background service exists and
// whether it is currently running.
type ServiceStatus string

const (
	// StatusNotInstalled means no service/task/unit/plist was found.
	StatusNotInstalled ServiceStatus = "not-installed"
	// StatusRunning means the service entry exists and the daemon is up.
	StatusRunning ServiceStatus = "running"
	// StatusStopped means the service entry exists but the daemon is down.
	StatusStopped ServiceStatus = "stopped"
	// StatusUnknown means the backend could not determine the state.
	StatusUnknown ServiceStatus = "unknown"
)

// Service is the OS backend contract. Each backend implements it behind a
// build tag; Current returns the one for runtime.GOOS.
type Service interface {
	// Install writes the OS service definition (scheduled task, plist, or
	// unit file) for executable+args and enables start-on-login.
	Install(executable string, args []string) error
	// Uninstall stops the daemon and removes the OS service definition.
	Uninstall() error
	// Status reports the install/run state of the daemon service.
	Status() (ServiceStatus, error)
}

// ---------------------------------------------------------------------------
// Injectable OS hooks (tests override these; production uses os.*).
// ---------------------------------------------------------------------------

var (
	userConfigDirFunc = os.UserConfigDir
	userHomeDirFunc   = os.UserHomeDir
	userCacheDirFunc  = os.UserCacheDir
)

// ---------------------------------------------------------------------------
// Config / cache directories.
// ---------------------------------------------------------------------------

// ConfigDir returns the nexus config dir, derived from os.UserConfigDir:
//
//	Windows: %APPDATA%\nexus
//	darwin:  ~/Library/Application Support/nexus
//	linux:   ~/.config/nexus (or $XDG_CONFIG_HOME/nexus)
//
// NEXUS_CONFIG_DIR overrides the result when set (non-empty).
func ConfigDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("NEXUS_CONFIG_DIR")); override != "" {
		return override, nil
	}
	base, err := userConfigDirFunc()
	if err != nil {
		return "", fmt.Errorf("platform: config dir: %w", err)
	}
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("platform: config dir: empty base from os.UserConfigDir")
	}
	return filepath.Join(base, "nexus"), nil
}

// CacheDir returns the nexus cache dir, derived from os.UserCacheDir with a
// UserConfigDir-adjacent fallback (some CI environments lack a cache dir).
// NEXUS_CACHE_DIR overrides the result when set (non-empty).
func CacheDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("NEXUS_CACHE_DIR")); override != "" {
		return override, nil
	}
	if base, err := userCacheDirFunc(); err == nil && strings.TrimSpace(base) != "" {
		return filepath.Join(base, "nexus"), nil
	}
	// Fallback: <config-dir>/cache keeps one root instead of failing.
	cfg, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "cache"), nil
}

// configDirForGOOS is the pure, injectable mapping used by ConfigDir and by
// tests to assert per-OS behavior without switching GOOS. Empty appData/home
// fall back to the configBase (the os.UserConfigDir value for that OS).
func configDirForGOOS(goos, configBase, appData, home string) string {
	switch goos {
	case "windows":
		if strings.TrimSpace(appData) != "" {
			return filepath.Join(appData, "nexus")
		}
		return filepath.Join(configBase, "nexus")
	case "darwin":
		if strings.TrimSpace(home) != "" {
			return filepath.Join(home, "Library", "Application Support", "nexus")
		}
		return filepath.Join(configBase, "nexus")
	default: // linux and other unix-likes: honor XDG layout via configBase.
		if strings.TrimSpace(configBase) != "" {
			return filepath.Join(configBase, "nexus")
		}
		if strings.TrimSpace(home) != "" {
			return filepath.Join(home, ".config", "nexus")
		}
		return filepath.Join(".config", "nexus")
	}
}

// cacheDirForGOOS mirrors configDirForGOOS for the cache location.
func cacheDirForGOOS(goos, cacheBase, configBase, home string) string {
	if strings.TrimSpace(cacheBase) != "" {
		return filepath.Join(cacheBase, "nexus")
	}
	// Fallback keeps a single root: <config>/cache.
	return filepath.Join(configDirForGOOS(goos, configBase, "", home), "cache")
}

// ---------------------------------------------------------------------------
// Project roots.
// ---------------------------------------------------------------------------

// ProjectRoots returns the directories the daemon scans for projects. It is
// configuration, not code: NEXUS_PROJECT_ROOTS (os.PathList-separated) wins;
// otherwise the user's home directory is the single default root. Never
// returns hardcoded drive letters — callers must pass roots explicitly on
// machines where the home dir is wrong.
func ProjectRoots() []string {
	if raw := strings.TrimSpace(os.Getenv("NEXUS_PROJECT_ROOTS")); raw != "" {
		var roots []string
		for _, p := range filepath.SplitList(raw) {
			if p = strings.TrimSpace(p); p != "" {
				roots = append(roots, p)
			}
		}
		if len(roots) > 0 {
			return roots
		}
	}
	if home, err := userHomeDirFunc(); err == nil && strings.TrimSpace(home) != "" {
		return []string{home}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Service definition renderers (pure — identical output on every GOOS so
// cross-compile tests stay green).
// ---------------------------------------------------------------------------

// quoteArg renders one argv element for systemd ExecStart / schtasks /TR.
// Elements with whitespace or quotes are double-quoted with interior quotes
// and backslashes escaped.
func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"'") {
		return s
	}
	r := strings.ReplaceAll(s, `\`, `\\`)
	r = strings.ReplaceAll(r, `"`, `\"`)
	return `"` + r + `"`
}

// execCommandLine joins executable+args into a single command line for
// ExecStart= and schtasks /TR.
func execCommandLine(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(executable))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// RenderSystemdUnit renders a systemd --user unit that starts the daemon on
// login (WantedBy=default.target). Contains an ExecStart= line with the full
// command. The caller writes it to
// ~/.config/systemd/user/nexus-daemon.service. Restart policy is always
// (Issue #111 hardening: the daemon must come back even after a clean exit,
// not just on failure); logs go to the journal so output is never lost.
func RenderSystemdUnit(description, executable string, args []string) string {
	if strings.TrimSpace(description) == "" {
		description = "Nexus workspace daemon"
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", description)
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", execCommandLine(executable, args))
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5s\n")
	b.WriteString("StandardOutput=journal\n")
	b.WriteString("StandardError=journal\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// RenderLaunchdPlist renders a launchd plist that starts the daemon at login
// (RunAtLoad + KeepAlive). ProgramArguments carries executable+args verbatim
// as an array so no shell quoting is involved; the executable path is always
// present in the output. ThrottleInterval rate-limits crash loops and
// StandardOut/ErrorPath capture logs under ~/Library/Logs (Issue #111).
func RenderLaunchdPlist(label, executable string, args []string) string {
	if strings.TrimSpace(label) == "" {
		label = LaunchdLabel
	}
	logOut := "~/Library/Logs/" + label + ".log"
	logErr := "~/Library/Logs/" + label + ".err.log"
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", plistEscape(label))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	fmt.Fprintf(&b, "\t\t<string>%s</string>\n", plistEscape(executable))
	for _, a := range args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", plistEscape(a))
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>5</integer>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", plistEscape(logOut))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", plistEscape(logErr))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// plistEscape escapes the five XML special chars for plist <string> bodies.
func plistEscape(s string) string {
	r := strings.ReplaceAll(s, "&", "&amp;")
	r = strings.ReplaceAll(r, "<", "&lt;")
	r = strings.ReplaceAll(r, ">", "&gt;")
	r = strings.ReplaceAll(r, `"`, "&quot;")
	return r
}

// RenderSchtasksCreateArgs builds the `schtasks /Create` argv for an
// ONLOGON task. Kept pure so the quoting is unit-testable on any GOOS; the
// windows backend executes it. /DELAY staggers startup past logon storms;
// task output should be redirected by the daemon itself to the cache-dir log
// (see DaemonLogFile) because schtasks has no native log-file switch.
func RenderSchtasksCreateArgs(taskName, executable string, args []string) []string {
	return []string{
		"/Create",
		"/TN", taskName,
		"/TR", execCommandLine(executable, args),
		"/SC", "ONLOGON",
		"/DELAY", "0000:30",
		"/RL", "HIGHEST",
		"/F",
	}
}

// DaemonLogFile returns the daemon log path under the cache dir
// (<cache>/nexus/daemon.log, NEXUS_CACHE_DIR-aware). Backends that cannot
// capture output natively (schtasks) should have the daemon log here.
func DaemonLogFile() string {
	if dir, err := CacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "daemon.log")
	}
	return filepath.Join("nexus", "daemon.log")
}

// schtasksMissing reports whether schtasks output means "task not found".
// schtasks localizes its messages, so English plus common German/French/
// Spanish/Portuguese/Italian/Dutch tokens are matched case-insensitively
// (Issue #111). Generic "not found"/"not exist" cover phrasings like
// "cannot be found" that contain neither "cannot find" verbatim.
func schtasksMissing(output string) bool {
	lower := strings.ToLower(output)
	for _, token := range []string{
		"cannot find", "cannot be found", "not found", "not exist",
		"does not exist", "no se puede encontrar", "no existe",
		"nicht gefunden", "existiert nicht", "introuvable", "n'existe pas",
		"impossibile trovare", "non esiste", "não encontrado", "não existe",
		"0x80070002",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// parseSchtasksStatus interprets `schtasks /Query /FO LIST` output without
// depending on the OS display language (Issue #111). Missing-task output
// maps to StatusNotInstalled (nil error); a running state in any supported
// language maps to StatusRunning; any other present task maps to
// StatusStopped; unexpected query failures map to StatusUnknown + error.
func parseSchtasksStatus(output string, queryErr error) (ServiceStatus, error) {
	text := output
	lower := strings.ToLower(text)
	if queryErr != nil {
		if schtasksMissing(text) {
			return StatusNotInstalled, nil
		}
		return StatusUnknown, fmt.Errorf("platform: schtasks query: %w: %s", queryErr, strings.TrimSpace(text))
	}
	if schtasksMissing(text) {
		return StatusNotInstalled, nil
	}
	// Running states across locales: English Running, German Wird ausgeführt,
	// French En cours, Spanish En ejecución, Italian In esecuzione.
	for _, token := range []string{
		"running", "wird ausgef", "en cours", "en ejecuci", "in esecuzione",
		"em execu", "wordt uitgevoerd",
	} {
		if strings.Contains(lower, token) {
			return StatusRunning, nil
		}
	}
	return StatusStopped, nil
}

// ---------------------------------------------------------------------------
// Backend registry + CLI hooks.
// ---------------------------------------------------------------------------

// Current returns the Service backend for runtime.GOOS, or an error on
// unsupported platforms. Backends are registered by the build-tagged files.
func Current() (Service, error) {
	if factory, ok := backends[runtime.GOOS]; ok {
		return factory(), nil
	}
	return nil, fmt.Errorf("platform: unsupported GOOS %q", runtime.GOOS)
}

var backends = map[string]func() Service{}

// registerBackend is called from init() in the build-tagged backend files.
func registerBackend(goos string, factory func() Service) {
	backends[goos] = factory
}

// defaultExecutable resolves the daemon binary for Install when the caller
// passes an empty executable: NEXUS_DAEMON_BIN wins, else the current
// process binary.
func defaultExecutable() (string, error) {
	if bin := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_BIN")); bin != "" {
		return bin, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("platform: resolve daemon binary: %w", err)
	}
	return exe, nil
}

// Install is the `nexus daemon install` hook: registers the daemon service
// for executable+args (empty executable resolves via NEXUS_DAEMON_BIN or the
// current binary) and enables start-on-login on the current OS.
func Install(executable string, args []string) error {
	if strings.TrimSpace(executable) == "" {
		var err error
		executable, err = defaultExecutable()
		if err != nil {
			return err
		}
	}
	svc, err := Current()
	if err != nil {
		return err
	}
	return svc.Install(executable, args)
}

// Uninstall is the `nexus daemon uninstall` hook: stops the daemon and
// removes the OS service definition on the current OS.
func Uninstall() error {
	svc, err := Current()
	if err != nil {
		return err
	}
	return svc.Uninstall()
}

// Status reports the daemon service state on the current OS.
func Status() (ServiceStatus, error) {
	svc, err := Current()
	if err != nil {
		return StatusUnknown, err
	}
	return svc.Status()
}
