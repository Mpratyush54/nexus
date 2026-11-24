package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// On macOS the default project root is $HOME; scanning ~/Library or the
// media folders would raise privacy prompts for nothing.
func TestLeavesSkipsMacHomeFolders(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	root := t.TempDir()
	for _, d := range []string{
		filepath.Join("Library", "Containers", "com.example", ".git"),
		filepath.Join("Pictures", "repo", ".git"),
		filepath.Join("code", "real", ".git"),
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NEXUS_PROJECT_ROOTS", root)
	for _, l := range Leaves() {
		top := strings.SplitN(l, "/", 2)[0]
		if top == "Library" || top == "Pictures" {
			t.Fatalf("leaf %q comes from a macOS home system folder", l)
		}
	}
	if !SystemDir("Library") || !SystemDir("pictures") {
		t.Fatal("Library and Pictures must be system dirs on macOS")
	}
	if SystemDir("Documents") {
		t.Fatal("Documents may hold projects and must still be scanned")
	}
}
