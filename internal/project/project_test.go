// Tests for the project leaf-identity helpers (issue #47).
//
// NOTE on scope: the remote-URL normalizer (NormalizeRemoteURL) lives in
// internal/store (store/projects.go), not in this package — importing it
// here would be an import cycle (store already imports project for
// Fingerprint), and it is already pinned by a 24-case table in
// store/projects_test.go. These tests pin THIS package's own normalization
// semantics instead: SystemDir case-folding, marker matching (incl. the
// "*.sln" glob), and Fingerprint's best-effort empty identity off-repo.
package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemDirTable(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"recycle bin upper", "$RECYCLE.BIN", true},
		{"recycle bin lower", "$recycle.bin", true},
		{"sysvol mixed", "System Volume Information", true},
		{"program files", "Program Files", true},
		{"windowsapps", "WindowsApps", true},
		{"recovery", "Recovery", true},
		{"system sav", "system.sav", true},
		{"plain src", "src", false},
		{"repo dir", "gitlab-test", false},
		{"dot git", ".git", false},
		{"empty", "", false},
		{"prefix only", "$recycle", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SystemDir(tc.input); got != tc.want {
				t.Errorf("SystemDir(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestHasMarkerTable(t *testing.T) {
	write := func(t *testing.T, dir, name string, isDir bool) {
		t.Helper()
		p := filepath.Join(dir, name)
		if isDir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		entries map[string]bool // file/dir name → isDir
		want    bool
	}{
		{"git dir", map[string]bool{".git": true}, true},
		{"package json", map[string]bool{"package.json": false}, true},
		{"go mod", map[string]bool{"go.mod": false}, true},
		{"pyproject", map[string]bool{"pyproject.toml": false}, true},
		{"sln glob", map[string]bool{"app.sln": false}, true},
		{"plain source only", map[string]bool{"main.go": false}, false},
		{"tooling dir name is not itself a marker", map[string]bool{"node_modules": true}, false},
		{"empty dir", nil, false},
		{"near-miss marker", map[string]bool{"go.mod.bak": false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, isDir := range tc.entries {
				write(t, dir, name, isDir)
			}
			if got := hasMarker(dir); got != tc.want {
				t.Errorf("hasMarker(%v) = %v, want %v", tc.entries, got, tc.want)
			}
		})
	}
	if hasMarker(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("hasMarker(missing dir) = true, want false")
	}
}

func TestFingerprintNonRepoEmpty(t *testing.T) {
	// Best-effort identity: off-repo yields ("",""), and the per-dir cache
	// returns the same pair on repeat calls.
	dir := t.TempDir()
	o1, r1 := Fingerprint(dir)
	if o1 != "" || r1 != "" {
		t.Fatalf("Fingerprint(plain dir) = (%q,%q), want (\"\",\"\")", o1, r1)
	}
	if o2, r2 := Fingerprint(dir); o2 != o1 || r2 != r1 {
		t.Errorf("cached Fingerprint = (%q,%q), want (%q,%q)", o2, r2, o1, r1)
	}
}
