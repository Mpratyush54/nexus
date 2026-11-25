package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAugmentedPathKeepsOrderAndAddsExisting(t *testing.T) {
	sep := string(os.PathListSeparator)
	exists := func(p string) bool { return p != "/missing" }
	got := augmentedPath("/usr/bin"+sep+"/bin", []string{"/opt/homebrew/bin", "/usr/bin", "/missing"}, exists)
	want := "/usr/bin" + sep + "/bin" + sep + "/opt/homebrew/bin"
	if got != want {
		t.Fatalf("augmentedPath = %q, want %q", got, want)
	}
}

// Simulates a Finder launch: launchd PATH plus a CLI in ~/.local/bin.
func TestAugmentGUIPathFindsUserCLI(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	AugmentGUIPath()
	if !strings.Contains(os.Getenv("PATH"), bin) {
		t.Fatalf("PATH %q lacks %s", os.Getenv("PATH"), bin)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), "/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Fatalf("existing PATH order changed: %q", os.Getenv("PATH"))
	}
}
