package platform

// Audit tests for darwin.go's launchd surface via the pure plist renderer in
// platform.go. Never touches launchctl: Install/Uninstall are not executed.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditDarwinPlistDeterministic(t *testing.T) {
	a := RenderLaunchdPlist(LaunchdLabel, "/opt/nexus/nexus", []string{"daemon", "run"})
	b := RenderLaunchdPlist(LaunchdLabel, "/opt/nexus/nexus", []string{"daemon", "run"})
	if a != b {
		t.Error("RenderLaunchdPlist is not deterministic")
	}
}

func TestAuditDarwinPlistDefaultLabel(t *testing.T) {
	for _, label := range []string{"", "   "} {
		plist := RenderLaunchdPlist(label, "/opt/nexus/nexus", nil)
		if !strings.Contains(plist, LaunchdLabel) {
			t.Errorf("empty label: plist missing default %q", LaunchdLabel)
		}
	}
	if LaunchdLabel == "" {
		t.Fatal("LaunchdLabel const is empty")
	}
}

func TestAuditDarwinPlistContents(t *testing.T) {
	exe := filepath.Join("opt", "nexus", "nexus")
	plist := RenderLaunchdPlist(LaunchdLabel, exe, []string{"daemon", "run", "--port", "7171"})
	for _, want := range []string{
		LaunchdLabel, exe, "ProgramArguments", "RunAtLoad", "KeepAlive",
		"ThrottleInterval", "StandardOutPath", "StandardErrorPath",
		"daemon", "run", "--port", "7171", "<true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
	if !strings.HasPrefix(plist, "<?xml") {
		t.Error("plist missing XML prolog")
	}
}

func TestAuditDarwinPlistArgsVerbatim(t *testing.T) {
	// ProgramArguments carries argv verbatim: no shell quoting is involved,
	// so an arg with spaces appears exactly as-is inside <string>.
	plist := RenderLaunchdPlist(LaunchdLabel, "/opt/nexus/nexus", []string{"my arg", "a&b"})
	if !strings.Contains(plist, "<string>my arg</string>") {
		t.Errorf("spaced arg not verbatim:\n%s", plist)
	}
	// XML metachars in values are escaped, never raw.
	if strings.Contains(plist, "<string>a&b</string>") {
		t.Errorf("unescaped & in plist:\n%s", plist)
	}
	if !strings.Contains(plist, "a&amp;b") {
		t.Errorf("plist missing &amp; escape:\n%s", plist)
	}
}

func TestAuditDarwinPlistEscapesXML(t *testing.T) {
	plist := RenderLaunchdPlist("a&b", "/opt/x<y>.exe", []string{`q"uote`})
	for _, want := range []string{"a&amp;b", "/opt/x&lt;y&gt;.exe", "q&quot;uote"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing escape %q:\n%s", want, plist)
		}
	}
}

func TestAuditDarwinConfigMapping(t *testing.T) {
	got := configDirForGOOS("darwin", "/ignored-base", "", "/Users/a")
	want := filepath.Join("/Users/a", "Library", "Application Support", "nexus")
	if got != want {
		t.Errorf("darwin home mapping: got %q, want %q", got, want)
	}
	// Empty home falls back to the config base.
	got = configDirForGOOS("darwin", "/cfg/base", "", "")
	if got != filepath.Join("/cfg/base", "nexus") {
		t.Errorf("darwin fallback: got %q", got)
	}
	if !strings.HasSuffix(got, "nexus") {
		t.Errorf("darwin config must end in nexus: %q", got)
	}
}
