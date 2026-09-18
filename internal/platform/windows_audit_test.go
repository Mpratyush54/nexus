package platform

// Audit tests for windows.go's Task Scheduler surface via the pure schtasks
// argv builder in platform.go. Never touches schtasks: Install/Uninstall and
// Status side effects are not executed (Status is read-only and only asserted
// for enum membership on windows).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAuditSchtasksStructure(t *testing.T) {
	argv := RenderSchtasksCreateArgs(SchtasksName, `C:\tools\nexus.exe`, []string{"daemon", "run"})
	if len(argv) == 0 || argv[0] != "/Create" {
		t.Fatalf("argv must start with /Create: %q", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"/Create", "/TN", SchtasksName, "/TR", "/SC", "ONLOGON", "/DELAY", "/RL", "HIGHEST", "/F"} {
		if !strings.Contains(joined, want) {
			t.Errorf("schtasks args missing %q: %q", want, joined)
		}
	}
	// Positional shape: /TN <name> ... /TR <cmdline> ... /SC ONLOGON.
	tn, tr, sc := -1, -1, -1
	for i, a := range argv {
		switch a {
		case "/TN":
			tn = i
		case "/TR":
			tr = i
		case "/SC":
			sc = i
		}
	}
	if tn < 0 || tr < 0 || sc < 0 {
		t.Fatalf("missing /TN /TR /SC switches: %q", argv)
	}
	if argv[tn+1] != SchtasksName {
		t.Errorf("/TN value = %q, want %q", argv[tn+1], SchtasksName)
	}
	if argv[sc+1] != "ONLOGON" {
		t.Errorf("/SC value = %q, want ONLOGON", argv[sc+1])
	}
	if !(tn < tr && tr < sc) {
		t.Errorf("switch order wrong (/TN < /TR < /SC): %q", argv)
	}
}

func TestAuditSchtasksTRQuoting(t *testing.T) {
	exe := `C:\my tools\nexus.exe`
	argv := RenderSchtasksCreateArgs(SchtasksName, exe, []string{"daemon", "run"})
	var tr string
	for i, a := range argv {
		if a == "/TR" && i+1 < len(argv) {
			tr = argv[i+1]
		}
	}
	if tr == "" {
		t.Fatal("missing /TR value")
	}
	if !strings.HasPrefix(tr, `"`) {
		t.Errorf("/TR does not quote spaced executable: %q", tr)
	}
	if !strings.Contains(tr, `my tools`) || !strings.Contains(tr, "nexus.exe") {
		t.Errorf("/TR missing executable path: %q", tr)
	}
	if !strings.Contains(tr, "daemon") || !strings.Contains(tr, "run") {
		t.Errorf("/TR missing args: %q", tr)
	}
	// /TR embeds exactly the shared command-line builder output.
	if want := execCommandLine(exe, []string{"daemon", "run"}); tr != want {
		t.Errorf("/TR = %q, want execCommandLine %q", tr, want)
	}
}

func TestAuditSchtasksDeterministic(t *testing.T) {
	a := strings.Join(RenderSchtasksCreateArgs(SchtasksName, "exe", []string{"daemon"}), " ")
	b := strings.Join(RenderSchtasksCreateArgs(SchtasksName, "exe", []string{"daemon"}), " ")
	if a != b {
		t.Error("RenderSchtasksCreateArgs is not deterministic")
	}
}

func TestAuditWindowsConfigMapping(t *testing.T) {
	got := configDirForGOOS("windows", `X:\other`, `C:\Users\a\AppData\Roaming`, "")
	if !strings.HasPrefix(got, `C:\Users\a\AppData\Roaming`) {
		t.Errorf("windows APPDATA preference: got %q", got)
	}
	// Empty APPDATA falls back to the config base.
	got = configDirForGOOS("windows", `X:\base`, "", "")
	if got != filepath.Join(`X:\base`, "nexus") {
		t.Errorf("windows fallback: got %q", got)
	}
	if !strings.HasSuffix(got, "nexus") {
		t.Errorf("windows config must end in nexus: %q", got)
	}
}

func TestAuditSchtasksNameConst(t *testing.T) {
	if SchtasksName == "" {
		t.Fatal("SchtasksName is empty")
	}
	if SchtasksName != ServiceName {
		t.Errorf("SchtasksName = %q, want ServiceName %q", SchtasksName, ServiceName)
	}
}

func TestAuditCurrentBackend(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows backend registration only present on windows")
	}
	svc, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if svc == nil {
		t.Fatal("Current returned nil service on windows")
	}
	// Read-only Status query: only assert the result is a known enum value,
	// never a side effect.
	st, _ := svc.Status()
	switch st {
	case StatusNotInstalled, StatusRunning, StatusStopped, StatusUnknown:
	default:
		t.Errorf("Status = %q, not a known ServiceStatus", st)
	}
}

func TestAuditProjectRootsDerivation(t *testing.T) {
	roots := []string{filepath.Join("a", "one"), filepath.Join("b", "two")}
	t.Setenv("NEXUS_PROJECT_ROOTS", strings.Join(roots, string(os.PathListSeparator)))
	got := ProjectRoots()
	if len(got) != 2 || got[0] != roots[0] || got[1] != roots[1] {
		t.Fatalf("env roots: got %q", got)
	}
	// Blank entries are skipped.
	t.Setenv("NEXUS_PROJECT_ROOTS", roots[0]+string(os.PathListSeparator)+string(os.PathListSeparator)+roots[1])
	if got := ProjectRoots(); len(got) != 2 {
		t.Fatalf("blank entries not skipped: %q", got)
	}
	// Unset env falls back to home, never to a hardcoded location.
	t.Setenv("NEXUS_PROJECT_ROOTS", "")
	prev := userHomeDirFunc
	userHomeDirFunc = func() (string, error) { return filepath.Join("fake", "home"), nil }
	defer func() { userHomeDirFunc = prev }()
	got = ProjectRoots()
	if len(got) != 1 || got[0] != filepath.Join("fake", "home") {
		t.Fatalf("default root is home: got %q", got)
	}
}

func TestAuditConfigCacheEnvOverrides(t *testing.T) {
	t.Setenv("NEXUS_CONFIG_DIR", filepath.Join("some", "custom", "dir"))
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("some", "custom", "dir") {
		t.Errorf("NEXUS_CONFIG_DIR override: got %q", got)
	}
	t.Setenv("NEXUS_CACHE_DIR", filepath.Join("some", "cache", "dir"))
	cgot, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if cgot != filepath.Join("some", "cache", "dir") {
		t.Errorf("NEXUS_CACHE_DIR override: got %q", cgot)
	}
	// Cache falls back under the config dir when no cache base exists.
	t.Setenv("NEXUS_CACHE_DIR", "")
	t.Setenv("NEXUS_CONFIG_DIR", filepath.Join("cfg", "root"))
	prevCache := userCacheDirFunc
	userCacheDirFunc = func() (string, error) { return "", nil }
	defer func() { userCacheDirFunc = prevCache }()
	cgot, err = CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cgot, filepath.Join("nexus", "cache")) && !strings.Contains(cgot, "cache") {
		t.Errorf("cache fallback missing cache segment: %q", cgot)
	}
}
