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
// ~/.config/systemd/user/nexus-daemon.service.
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
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5s\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// RenderLaunchdPlist renders a launchd plist that starts the daemon at login
// (RunAtLoad + KeepAlive). ProgramArguments carries executable+args verbatim
// as an array so no shell quoting is involved; the executable path is always
// present in the output.
func RenderLaunchdPlist(label, executable string, args []string) string {
	if strings.TrimSpace(label) == "" {
		label = LaunchdLabel
	}
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
// windows backend executes it.
func RenderSchtasksCreateArgs(taskName, executable string, args []string) []string {
	return []string{
		"/Create",
		"/TN", taskName,
		"/TR", execCommandLine(executable, args),
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
		"/F",
	}
}

// ---------------------------------------------------------------------------
// schtasks status parsing (pure — unit-testable on any GOOS).
// ---------------------------------------------------------------------------

// schtasksStatusHeaders are the localized `schtasks /Query /FO LIST` field
// names carrying the task state (English, French, Spanish, Italian,
// Portuguese, Russian, Chinese). Matched case-insensitively.
var schtasksStatusHeaders = map[string]bool{
	"status": true, "statut": true, "estado": true, "stato": true,
	"состояние": true, "状态": true, "状態": true, "상태": true,
}

// schtasksRunningTokens are localized "Running" state values. A task whose
// status line contains one of these is executing; anything else recognized
// (Ready, Disabled, Queued, ...) means installed-but-stopped.
var schtasksRunningTokens = []string{
	"running",         // English
	"wird ausgeführt", // German ("Wird ausgeführt")
	"ausgeführt",      // German (short form)
	"en cours",        // French ("En cours d'exécution")
	"ejecuci",         // Spanish ("En ejecución")
	"execu",           // Portuguese ("Em execução") / Italian ("In esecuzione")
	"uitgevoerd",      // Dutch ("Wordt uitgevoerd")
	"wykonywane",      // Polish
	"выполняется",     // Russian ("Выполняется")
	"运行",              // Chinese ("正在运行")
	"実行中",             // Japanese
	"실행 중",            // Korean
}

// schtasksIdleTokens are localized idle state values mapping to
// StatusStopped. Unrecognized non-empty values yield StatusUnknown rather
// than a guess, so non-English systems never get a false stopped/running.
var schtasksIdleTokens = []string{
	"ready", "queued", "disabled", // English
	"bereit", "deaktiviert", // German
	"prêt", "pret", "désactivé", "desactive", // French
	"lista", "deshabilitada", "en cola", // Spanish
	"pronto", "disabilitata", "accodata", // Italian
	"pronta", "desabilitada", // Portuguese
	"gereed", "uitgeschakeld", // Dutch
	"готов", "отключена", // Russian
	"就绪",   // Chinese
	"準備完了", // Japanese
}

// schtasksStateFromList parses `schtasks /Query /TN <name> /FO LIST` text
// into a ServiceStatus. Existence is NOT derived here — the caller uses the
// schtasks exit code for that (locale-independent). Returns StatusUnknown
// when no status line is present, the value is empty, or the value is not
// a recognized running/idle token.
func schtasksStateFromList(text string) ServiceStatus {
	value, found := "", false
	for _, line := range strings.Split(text, "\n") {
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if schtasksStatusHeaders[strings.ToLower(strings.TrimSpace(name))] {
			value, found = strings.TrimSpace(val), true
			break
		}
	}
	if !found || strings.TrimSpace(value) == "" {
		return StatusUnknown
	}
	low := strings.ToLower(value)
	for _, tok := range schtasksRunningTokens {
		if strings.Contains(low, tok) {
			return StatusRunning
		}
	}
	for _, tok := range schtasksIdleTokens {
		if strings.Contains(low, tok) {
			return StatusStopped
		}
	}
	return StatusUnknown
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
