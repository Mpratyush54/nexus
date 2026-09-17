package platform

// Audit tests for linux.go's systemd surface via the pure unit renderer in
// platform.go. Never touches systemctl: Install/Uninstall are not executed.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditSystemdDeterministic(t *testing.T) {
	a := RenderSystemdUnit("Nexus workspace daemon", "/opt/nexus/nexus", []string{"daemon", "run"})
	b := RenderSystemdUnit("Nexus workspace daemon", "/opt/nexus/nexus", []string{"daemon", "run"})
	if a != b {
		t.Error("RenderSystemdUnit is not deterministic")
	}
}

func TestAuditSystemdDefaultDescription(t *testing.T) {
	for _, desc := range []string{"", "   "} {
		unit := RenderSystemdUnit(desc, "/opt/nexus/nexus", nil)
		if !strings.Contains(unit, "Description=Nexus workspace daemon") {
			t.Errorf("empty description: missing default:\n%s", unit)
		}
	}
	unit := RenderSystemdUnit("Custom desc", "/opt/nexus/nexus", nil)
	if !strings.Contains(unit, "Description=Custom desc") {
		t.Errorf("custom description lost:\n%s", unit)
	}
}

func TestAuditSystemdContents(t *testing.T) {
	exe := filepath.Join("opt", "nexus", "nexus")
	unit := RenderSystemdUnit("Nexus workspace daemon", exe, []string{"daemon", "run", "--port", "7171"})
	for _, want := range []string{
		"[Unit]", "[Service]", "[Install]",
		"After=network-online.target",
		"Wants=network-online.target",
		"Type=simple",
		"ExecStart=",
		"Restart=on-failure",
		"RestartSec=5s",
		"WantedBy=default.target",
		exe, "daemon", "--port", "7171",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestAuditSystemdExecStartQuoting(t *testing.T) {
	// An executable path with spaces must be quoted on the ExecStart line.
	unit := RenderSystemdUnit("", `C:\my tools\nexus.exe`, []string{"daemon", "run"})
	var execLine string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			execLine = line
		}
	}
	if execLine == "" {
		t.Fatal("unit missing ExecStart line")
	}
	if !strings.HasPrefix(execLine, "ExecStart=\"") {
		t.Errorf("ExecStart does not quote spaced executable: %q", execLine)
	}
	if !strings.Contains(execLine, `my tools`) || !strings.Contains(execLine, "nexus.exe") {
		t.Errorf("ExecStart missing executable path: %q", execLine)
	}
	if !strings.Contains(execLine, "daemon") {
		t.Errorf("ExecStart missing args: %q", execLine)
	}
}

func TestAuditQuoteArgMatrix(t *testing.T) {
	cases := []struct {
		in   string
		want string // empty means: only assert quoted-ness, not exact text
	}{
		{"", `""`},
		{"plain", "plain"},
		{`C:\tools\nexus.exe`, `C:\tools\nexus.exe`},
		{"--port", "--port"},
	}
	for _, tc := range cases {
		if got := quoteArg(tc.in); got != tc.want {
			t.Errorf("quoteArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Whitespace forces double-quoting.
	for _, in := range []string{"my arg", "a\tb", " daemon run "} {
		got := quoteArg(in)
		if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
			t.Errorf("quoteArg(%q) = %q, want double-quoted", in, got)
		}
	}
	// Interior quotes and backslashes are escaped.
	got := quoteArg(`say "hi"`)
	if !strings.Contains(got, `\"`) {
		t.Errorf("quoteArg missing escaped quote: %q", got)
	}
	got = quoteArg(`C:\my tools\x`)
	if !strings.Contains(got, `\\`) {
		t.Errorf("quoteArg missing escaped backslash: %q", got)
	}
}

func TestAuditExecCommandLineJoin(t *testing.T) {
	got := execCommandLine("/opt/nexus/nexus", []string{"daemon", "run", "--port", "7171"})
	want := "/opt/nexus/nexus daemon run --port 7171"
	if got != want {
		t.Errorf("execCommandLine = %q, want %q", got, want)
	}
	if got := execCommandLine("", nil); got != `""` {
		t.Errorf("empty executable = %q, want quoted empty", got)
	}
	// Round-trip: the systemd renderer embeds exactly this line.
	unit := RenderSystemdUnit("", "/opt/nexus/nexus", []string{"daemon", "run"})
	if !strings.Contains(unit, "ExecStart="+execCommandLine("/opt/nexus/nexus", []string{"daemon", "run"})) {
		t.Error("systemd ExecStart does not embed execCommandLine output")
	}
}

func TestAuditLinuxConfigMapping(t *testing.T) {
	got := configDirForGOOS("linux", "/home/a/.config", "", "/home/a")
	if got != filepath.Join("/home/a", ".config", "nexus") {
		t.Errorf("linux xdg mapping: got %q", got)
	}
	// Empty XDG base falls back to $HOME/.config.
	got = configDirForGOOS("linux", "", "", "/home/a")
	if got != filepath.Join("/home/a", ".config", "nexus") {
		t.Errorf("linux home fallback: got %q", got)
	}
	if !strings.HasSuffix(got, "nexus") {
		t.Errorf("linux config must end in nexus: %q", got)
	}
}

func TestAuditCacheDirMapping(t *testing.T) {
	got := cacheDirForGOOS("linux", "/home/a/.cache", "/home/a/.config", "/home/a")
	if got != filepath.Join("/home/a", ".cache", "nexus") {
		t.Errorf("cache base honored: got %q", got)
	}
	got = cacheDirForGOOS("linux", "", "/home/a/.config", "/home/a")
	if !strings.HasSuffix(got, filepath.Join("nexus", "cache")) {
		t.Errorf("cache fallback under config: got %q", got)
	}
}

func TestAuditSystemdUnitNameConst(t *testing.T) {
	if SystemdUnitName == "" {
		t.Fatal("SystemdUnitName is empty")
	}
	if !strings.HasSuffix(SystemdUnitName, ".service") {
		t.Errorf("SystemdUnitName = %q, want *.service", SystemdUnitName)
	}
}
