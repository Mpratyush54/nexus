package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Service identity
// ---------------------------------------------------------------------------

func TestServiceNames(t *testing.T) {
	if AppName == "" || ServiceName == "" {
		t.Fatal("AppName and ServiceName must be non-empty")
	}
	if LaunchdLabel() == "" || SystemdUnitName() == "" || WindowsTaskName() == "" {
		t.Fatal("per-OS service names must be non-empty")
	}
	if !strings.HasSuffix(SystemdUnitName(), ".service") {
		t.Fatalf("systemd unit name %q must end in .service", SystemdUnitName())
	}
	if !strings.Contains(LaunchdLabel(), AppName) {
		t.Fatalf("launchd label %q should contain app name %q", LaunchdLabel(), AppName)
	}
}

// ---------------------------------------------------------------------------
// Config/cache dirs: per-GOOS logic via pure funcs + synthetic bases.
// No host OS dependency: bases below mirror os.UserConfigDir /
// os.UserCacheDir values on each OS.
// ---------------------------------------------------------------------------

func TestConfigDirForBasePerGOOS(t *testing.T) {
	cases := []struct {
		goos string
		base string
	}{
		{"windows", filepath.Join(`C:`, "Users", "a", "AppData", "Roaming")},
		{"darwin", "/Users/a/Library/Application Support"},
		{"linux", "/home/a/.config"},
	}
	for _, tc := range cases {
		got := ConfigDirForBase(tc.base)
		want := filepath.Join(tc.base, AppName)
		if got != want {
			t.Errorf("GOOS=%s: ConfigDirForBase(%q) = %q, want %q", tc.goos, tc.base, got, want)
		}
		if !strings.HasSuffix(got, AppName) {
			t.Errorf("GOOS=%s: result %q missing app suffix", tc.goos, got)
		}
	}
}

func TestCacheDirForBasePerGOOS(t *testing.T) {
	cases := []struct {
		goos string
		base string
	}{
		{"windows", filepath.Join(`C:`, "Users", "a", "AppData", "Local")},
		{"darwin", "/Users/a/Library/Caches"},
		{"linux", "/home/a/.cache"},
	}
	for _, tc := range cases {
		got := CacheDirForBase(tc.base)
		want := filepath.Join(tc.base, AppName)
		if got != want {
			t.Errorf("GOOS=%s: CacheDirForBase(%q) = %q, want %q", tc.goos, tc.base, got, want)
		}
	}
}

func TestConfigDirLiveSuffix(t *testing.T) {
	dir, err := ConfigDir()
	if err != nil {
		t.Skipf("no user config dir on this host: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("ConfigDir() = %q, want absolute path", dir)
	}
	if filepath.Base(dir) != AppName {
		t.Errorf("ConfigDir() = %q, want base %q", dir, AppName)
	}
}

func TestCacheDirLiveSuffix(t *testing.T) {
	dir, err := CacheDir()
	if err != nil {
		t.Skipf("no user cache dir on this host: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("CacheDir() = %q, want absolute path", dir)
	}
	if filepath.Base(dir) != AppName {
		t.Errorf("CacheDir() = %q, want base %q", dir, AppName)
	}
}

func TestConfigFilePath(t *testing.T) {
	got := ConfigFilePath(filepath.Join("base", AppName))
	if filepath.Base(got) != "config.json" {
		t.Errorf("ConfigFilePath = %q, want config.json leaf", got)
	}
}

// ---------------------------------------------------------------------------
// launchd plist generation
// ---------------------------------------------------------------------------

func TestLaunchdPlist(t *testing.T) {
	exe := "/usr/local/bin/nexus"
	plist := LaunchdPlist(LaunchdLabel(), exe, []string{"daemon", "--port", "8080"})
	for _, want := range []string{
		`<?xml version="1.0"`,
		"<plist",
		LaunchdLabel(),
		"<key>ProgramArguments</key>",
		"<string>" + exe + "</string>",
		"<string>daemon</string>",
		"<string>--port</string>",
		"<string>8080</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q\n--- plist ---\n%s", want, plist)
		}
	}
}

func TestLaunchdPlistEscapesXML(t *testing.T) {
	plist := LaunchdPlist("com.example.a&b", "/bin/a<b>", []string{`x"y'z`})
	for _, want := range []string{"a&amp;b", "a&lt;b&gt;", "x&quot;y&apos;z"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing escaped %q\n--- plist ---\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "a&b</string>") {
		t.Error("plist contains unescaped &")
	}
}

// ---------------------------------------------------------------------------
// systemd unit generation
// ---------------------------------------------------------------------------

func TestSystemdUnit(t *testing.T) {
	exe := "/usr/local/bin/nexus"
	unit := SystemdUnit("nexus workspace daemon", exe, []string{"daemon"})
	for _, want := range []string{
		"[Unit]",
		"Description=nexus workspace daemon",
		"[Service]",
		"ExecStart=" + exe + " daemon",
		"Restart=always",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q\n--- unit ---\n%s", want, unit)
		}
	}
}

func TestSystemdUnitQuotesSpacedPaths(t *testing.T) {
	unit := SystemdUnit("", "/opt/my apps/nexus", []string{"daemon"})
	if !strings.Contains(unit, `ExecStart="/opt/my apps/nexus" daemon`) {
		t.Errorf("unit ExecStart not quoted:\n%s", unit)
	}
	if !strings.Contains(unit, "Description=nexus workspace daemon") {
		t.Errorf("empty description should fall back to default:\n%s", unit)
	}
}

// ---------------------------------------------------------------------------
// Windows task command builders
// ---------------------------------------------------------------------------

func TestWindowsTaskCommand(t *testing.T) {
	got := WindowsTaskCommand(`C:\Program Files\nexus\nexus.exe`, []string{"daemon"})
	want := `"C:\Program Files\nexus\nexus.exe" daemon`
	if got != want {
		t.Errorf("WindowsTaskCommand = %q, want %q", got, want)
	}
	plain := WindowsTaskCommand(`/usr/bin/nexus`, []string{"daemon", "--port", "8080"})
	if plain != "/usr/bin/nexus daemon --port 8080" {
		t.Errorf("WindowsTaskCommand plain = %q", plain)
	}
}

func TestSchtasksArgs(t *testing.T) {
	create := SchtasksCreateArgs("NexusDaemon", `"C:\x\nexus.exe" daemon`)
	joined := strings.Join(create, " ")
	for _, want := range []string{"/Create", "/TN", "NexusDaemon", "/TR", "/SC", "ONLOGON", "/F"} {
		if !strings.Contains(joined, want) {
			t.Errorf("create args missing %q: %q", want, joined)
		}
	}
	del := strings.Join(SchtasksDeleteArgs("NexusDaemon"), " ")
	if !strings.Contains(del, "/Delete") || !strings.Contains(del, "NexusDaemon") {
		t.Errorf("delete args wrong: %q", del)
	}
	q := strings.Join(SchtasksQueryArgs("NexusDaemon"), " ")
	if !strings.Contains(q, "/Query") || !strings.Contains(q, "LIST") {
		t.Errorf("query args wrong: %q", q)
	}
}

func TestParseSchtasksStatus(t *testing.T) {
	running := "HostName: X\nTaskName: \\NexusDaemon\nStatus: Running\n"
	if st, ok := ParseSchtasksStatus(running); !ok || st != StatusRunning {
		t.Errorf("running parse = %q,%v", st, ok)
	}
	ready := "TaskName: \\NexusDaemon\nStatus: Ready\n"
	if st, ok := ParseSchtasksStatus(ready); !ok || st != StatusStopped {
		t.Errorf("ready parse = %q,%v", st, ok)
	}
	missing := "ERROR: The system cannot find the file specified."
	if st, ok := ParseSchtasksStatus(missing); !ok || st != StatusNotInstalled {
		t.Errorf("missing parse = %q,%v", st, ok)
	}
	if _, ok := ParseSchtasksStatus("garbage output"); ok {
		t.Error("garbage output should not parse")
	}
}

// ---------------------------------------------------------------------------
// Guards: no hardcoded drive-letter paths in implementation sources.
// Only non-test sources are scanned (test fixtures may use drive-letter
// examples as data). Patterns are built at runtime so this file itself
// never contains a drive-letter literal.
// ---------------------------------------------------------------------------

func TestNoHardcodedDrivePaths(t *testing.T) {
	letter := string([]byte{'D', ':'}) + string([]byte{'\\'})
	letterFwd := string([]byte{'D', ':', '/'})
	matches, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), letter) || strings.Contains(string(data), letterFwd) {
			t.Errorf("implementation file %s contains a hardcoded drive-letter path", m)
		}
	}
}

// ---------------------------------------------------------------------------
// Misc pure helpers
// ---------------------------------------------------------------------------

func TestDefaultDaemonArgs(t *testing.T) {
	args := DefaultDaemonArgs()
	if len(args) == 0 || args[0] != "daemon" {
		t.Errorf("DefaultDaemonArgs() = %v, want [daemon ...]", args)
	}
}

func TestResolveExeExplicit(t *testing.T) {
	got, err := resolveExe("some/relative/nexus")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveExe(relative) = %q, want absolute", got)
	}
	if _, err := resolveExe(""); err != nil {
		t.Errorf("resolveExe(\"\") (current executable) failed: %v", err)
	}
}
