package continuex

import "testing"

func TestPermissionModeMapping(t *testing.T) {
	for _, mode := range []string{"", "acceptEdits", "plan", "bypassPermissions"} {
		if !validPermissionMode(mode) {
			t.Fatalf("%q refused", mode)
		}
	}
	if validPermissionMode("yolo") || validPermissionMode("default ") {
		t.Fatal("unknown mode accepted")
	}
	for mode, want := range map[string][2]string{
		"":                  {"untrusted", "workspace-write"},
		"acceptEdits":       {"on-request", "workspace-write"},
		"bypassPermissions": {"never", "danger-full-access"},
		"plan":              {"on-request", "read-only"},
	} {
		if p, s := codexPolicy(mode); p != want[0] || s != want[1] {
			t.Errorf("codexPolicy(%q) = %s/%s", mode, p, s)
		}
	}
	gemini := []string{"default", "autoEdit", "yolo", "plan"}
	claude := []string{"default", "acceptEdits", "bypassPermissions", "plan"}
	for _, c := range []struct {
		mode string
		ids  []string
		want string
	}{
		{"acceptEdits", gemini, "autoEdit"},
		{"bypassPermissions", gemini, "yolo"},
		{"acceptEdits", claude, "acceptEdits"},
		{"bypassPermissions", claude, "bypassPermissions"},
		{"plan", claude, "plan"},
		{"acceptEdits", []string{"agent", "ask"}, ""},
		{"", gemini, ""},
	} {
		if got := acpModeFor(c.mode, c.ids); got != c.want {
			t.Errorf("acpModeFor(%q, %v) = %q, want %q", c.mode, c.ids, got, c.want)
		}
	}
}
