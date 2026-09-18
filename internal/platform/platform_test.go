package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDirForGOOS(t *testing.T) {
	cases := []struct {
		goos       string
		configBase string
		appData    string
		home       string
		wantSub    string
	}{
		{"windows", `C:\Users\a\AppData\Roaming`, `C:\Users\a\AppData\Roaming`, `/home/a`, filepath.Join("AppData", "Roaming", "nexus")},
		{"darwin", "/home/a/Library/Application Support", "", "/home/a", filepath.Join("Library", "Application Support", "nexus")},
		{"linux", "/home/a/.config", "", "/home/a", filepath.Join(".config", "nexus")},
	}
	for _, tc := range cases {
		got := configDirForGOOS(tc.goos, tc.configBase, tc.appData, tc.home)
		if !strings.HasSuffix(got, "nexus") {
			t.Errorf("goos=%s: got %q, want suffix nexus", tc.goos, got)
		}
		if !strings.Contains(strings.ReplaceAll(got, `\`, `/`), strings.ReplaceAll(tc.wantSub, `\`, `/`)) && tc.goos != "linux" {
			t.Errorf("goos=%s: got %q, want it to contain %q", tc.goos, got, tc.wantSub)
		}
		if strings.Contains(got, "D:") {
			t.Errorf("goos=%s: got %q, must not hardcode a D: drive", tc.goos, got)
		}
	}
	// Windows prefers APPDATA over the generic config base.
	got := configDirForGOOS("windows", `X:\other`, `C:\Users\a\AppData\Roaming`, "")
	if !strings.HasPrefix(got, `C:\Users\a\AppData\Roaming`) {
		t.Errorf("windows APPDATA preference: got %q", got)
	}
	// darwin derives from home, linux from the XDG config base.
	if got := configDirForGOOS("darwin", "/ignored", "", "/Users/a"); got != filepath.Join("/Users/a", "Library", "Application Support", "nexus") {
		t.Errorf("darwin home mapping: got %q", got)
	}
	if got := configDirForGOOS("linux", "/home/a/.config", "", "/home/a"); got != filepath.Join("/home/a", ".config", "nexus") {
		t.Errorf("linux xdg mapping: got %q", got)
	}
}

func TestCacheDirForGOOS(t *testing.T) {
	got := cacheDirForGOOS("linux", "/home/a/.cache", "/home/a/.config", "/home/a")
	if got != filepath.Join("/home/a", ".cache", "nexus") {
		t.Errorf("cache base honored: got %q", got)
	}
	got = cacheDirForGOOS("linux", "", "/home/a/.config", "/home/a")
	if !strings.HasSuffix(got, filepath.Join("nexus", "cache")) {
		t.Errorf("cache fallback under config: got %q", got)
	}
}

func TestConfigDirEnvOverride(t *testing.T) {
	t.Setenv("NEXUS_CONFIG_DIR", filepath.Join("some", "custom", "dir"))
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("some", "custom", "dir") {
		t.Errorf("NEXUS_CONFIG_DIR override: got %q", got)
	}
}

func TestCacheDirEnvOverride(t *testing.T) {
	t.Setenv("NEXUS_CACHE_DIR", filepath.Join("some", "cache", "dir"))
	got, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("some", "cache", "dir") {
		t.Errorf("NEXUS_CACHE_DIR override: got %q", got)
	}
}

func TestConfigDirUsesUserConfigDir(t *testing.T) {
	t.Setenv("NEXUS_CONFIG_DIR", "")
	prev := userConfigDirFunc
	userConfigDirFunc = func() (string, error) { return filepath.Join("fake", "base"), nil }
	defer func() { userConfigDirFunc = prev }()
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("fake", "base", "nexus") {
		t.Errorf("ConfigDir should join os.UserConfigDir+nexus, got %q", got)
	}
}

func TestProjectRootsEnvAndDefault(t *testing.T) {
	roots := []string{filepath.Join("a", "one"), filepath.Join("b", "two")}
	t.Setenv("NEXUS_PROJECT_ROOTS", strings.Join(roots, string(os.PathListSeparator)))
	got := ProjectRoots()
	if len(got) != 2 || got[0] != roots[0] || got[1] != roots[1] {
		t.Fatalf("env roots: got %q", got)
	}
	t.Setenv("NEXUS_PROJECT_ROOTS", "")
	prev := userHomeDirFunc
	userHomeDirFunc = func() (string, error) { return filepath.Join("fake", "home"), nil }
	defer func() { userHomeDirFunc = prev }()
	got = ProjectRoots()
	if len(got) != 1 || got[0] != filepath.Join("fake", "home") {
		t.Fatalf("default root is home: got %q", got)
	}
	for _, r := range got {
		if strings.Contains(r, "D:") {
			t.Errorf("project root must not hardcode D:: %q", r)
		}
	}
}

func TestRenderSystemdUnitContainsExecStart(t *testing.T) {
	exe := filepath.Join("opt", "nexus", "nexus")
	unit := RenderSystemdUnit("Nexus workspace daemon", exe, []string{"daemon", "run", "--port", "7171"})
	if !strings.Contains(unit, "ExecStart=") {
		t.Fatal("unit missing ExecStart=")
	}
	if !strings.Contains(unit, exe) {
		t.Errorf("unit missing executable %q:\n%s", exe, unit)
	}
	for _, want := range []string{"WantedBy=default.target", "Restart=always", "StandardOutput=journal", "StandardError=journal", "daemon", "--port"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderLaunchdPlistContainsExecutable(t *testing.T) {
	exe := filepath.Join("opt", "nexus", "nexus")
	plist := RenderLaunchdPlist(LaunchdLabel, exe, []string{"daemon", "run"})
	for _, want := range []string{LaunchdLabel, exe, "ProgramArguments", "RunAtLoad", "KeepAlive", "ThrottleInterval", "StandardOutPath", "StandardErrorPath", "daemon"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
}

// TestParseSchtasksStatusMultilingual pins locale-independent status parsing
// (Issue #111): missing-task output in several languages maps to
// not-installed, running states map to running, other output maps to stopped.
func TestParseSchtasksStatusMultilingual(t *testing.T) {
	for _, out := range []string{
		`ERROR: The specified task name "nexus-daemon" cannot be found`,
		`FEHLER: Der Aufgabenname wurde nicht gefunden`,
		`ERREUR : le nom de tâche est introuvable`,
	} {
		st, err := parseSchtasksStatus(out, fmt.Errorf("exit 1"))
		if err != nil {
			t.Errorf("missing output %q should not error, got %v", out, err)
		}
		if st != StatusNotInstalled {
			t.Errorf("missing output %q = %q, want not-installed", out, st)
		}
	}
	st, err := parseSchtasksStatus("Status:          Running", nil)
	if err != nil || st != StatusRunning {
		t.Errorf("running = (%q,%v), want (running,nil)", st, err)
	}
	st, err = parseSchtasksStatus("Status: Wird ausgeführt", nil)
	if err != nil || st != StatusRunning {
		t.Errorf("german running = (%q,%v), want (running,nil)", st, err)
	}
	st, err = parseSchtasksStatus("Status:          Ready", nil)
	if err != nil || st != StatusStopped {
		t.Errorf("ready = (%q,%v), want (stopped,nil)", st, err)
	}
	if _, err := parseSchtasksStatus("access denied", fmt.Errorf("exit 1")); err == nil {
		t.Error("unexpected query failure should return error")
	}
}

func TestRenderSchtasksCreateArgs(t *testing.T) {
	exe := `C:\tools\nexus.exe`
	argv := RenderSchtasksCreateArgs(SchtasksName, exe, []string{"daemon", "run"})
	joined := strings.Join(argv, " ")
	for _, want := range []string{"/Create", "/TN", SchtasksName, "/SC", "ONLOGON", "daemon"} {
		if !strings.Contains(joined, want) {
			t.Errorf("schtasks args missing %q: %q", want, joined)
		}
	}
	if !strings.Contains(joined, exe) {
		t.Errorf("schtasks /TR missing executable: %q", joined)
	}
}

func TestQuoteArg(t *testing.T) {
	if got := quoteArg(""); got != `""` {
		t.Errorf("empty: got %q", got)
	}
	if got := quoteArg(`C:\tools\nexus.exe`); got != `C:\tools\nexus.exe` {
		t.Errorf("plain path unquoted: got %q", got)
	}
	if got := quoteArg(`C:\my tools\nexus.exe`); !strings.HasPrefix(got, `"`) {
		t.Errorf("spaced path quoted: got %q", got)
	}
}

// TestSchtasksStateFromList pins the locale-robust status parser (issue
// #111): English plus localized Status values map correctly, and anything
// unrecognized yields unknown instead of a false stopped/running.
func TestSchtasksStateFromList(t *testing.T) {
	const header = "TaskName:     \\nexus-daemon\nRun As User:  test\n"
	cases := []struct {
		name string
		text string
		want ServiceStatus
	}{
		{"english running", header + "Status:            Running\n", StatusRunning},
		{"english ready", header + "Status:            Ready\n", StatusStopped},
		{"english disabled", header + "Status:            Disabled\n", StatusStopped},
		{"case-insensitive", header + "STATUS:            RUNNING\n", StatusRunning},
		{"german running", header + "Status:            Wird ausgeführt\n", StatusRunning},
		{"german ready", header + "Status:            Bereit\n", StatusStopped},
		{"french running", header + "Statut:            En cours d'exécution\n", StatusRunning},
		{"spanish running", header + "Estado:            En ejecución\n", StatusRunning},
		{"russian running", header + "Состояние:         Выполняется\n", StatusRunning},
		{"unrecognized value is unknown", header + "Status:            Inconnu\n", StatusUnknown},
		{"no status line is unknown", "ERROR: The system cannot find the file specified.\n", StatusUnknown},
		{"empty output is unknown", "", StatusUnknown},
		{"empty value is unknown", header + "Status:\n", StatusUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := schtasksStateFromList(tc.text); got != tc.want {
				t.Errorf("schtasksStateFromList = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNoHardcodedDrivePaths guards the issue's acceptance criterion: no
// source file in this package may contain a hardcoded Windows drive literal.
func TestNoHardcodedDrivePaths(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		// Strip the test's own `D:` string literals from consideration by
		// scanning for drive patterns outside this test function is
		// overkill; instead assert on likely hardcode shapes.
		bads := []string{"\"D:\\", "\"D:/", "D:\\Users", "D:\\central-memory"}
		for _, bad := range bads {
			if strings.Contains(src, bad) && e.Name() != "platform_test.go" {
				t.Errorf("%s contains hardcoded path literal %q", e.Name(), bad)
			}
		}
	}
}
