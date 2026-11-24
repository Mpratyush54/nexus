package platform

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestAppDataDirForGOOS(t *testing.T) {
	home := filepath.Join("h", "me")
	cases := []struct {
		goos, appData, xdg, want string
	}{
		{"windows", `C:\Users\me\AppData\Roaming`, "", `C:\Users\me\AppData\Roaming`},
		{"windows", "", "", filepath.Join(home, "AppData", "Roaming")},
		{"darwin", "", "", filepath.Join(home, "Library", "Application Support")},
		// APPDATA / XDG_CONFIG_HOME must not leak into the macOS answer:
		// Cursor and VS Code ignore them there.
		{"darwin", "/bogus", "/also-bogus", filepath.Join(home, "Library", "Application Support")},
		{"linux", "", "", filepath.Join(home, ".config")},
		{"linux", "", "/xdg", "/xdg"},
		{"linux", "", "relative", filepath.Join(home, ".config")},
	}
	for _, c := range cases {
		if got := appDataDirForGOOS(c.goos, c.appData, c.xdg, home); got != c.want {
			t.Errorf("appDataDirForGOOS(%q, %q, %q) = %q, want %q", c.goos, c.appData, c.xdg, got, c.want)
		}
	}
}

func TestAppDataDirDarwinUsesHome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", "")
	if got, want := AppDataDir(), filepath.Join(home, "Library", "Application Support"); got != want {
		t.Fatalf("AppDataDir() = %q, want %q", got, want)
	}
}
