// Tests for issues #108 (error propagation, collision-free names) and
// #111 (platform-agnostic roots). Temp dirs only, no network, no registry.
package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mksrc stages a source tree OUTSIDE /tmp: ClassifyPath ignores any path
// containing a "/tmp/" segment, and Linux t.TempDir() lives under /tmp, so
// sources staged with t.TempDir() would be classified Ignore and never
// copied (nil error, empty output). Dest/vault dirs may stay in TempDir —
// only source paths are classified.
func mksrc(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir for non-tmp source staging")
	}
	dir, err := os.MkdirTemp(home, ".adapters-test-src-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestSafeNameIndexedCollisionFree(t *testing.T) {
	a := safeName(filepath.Join("some", "parent1", "app"), 0)
	b := safeName(filepath.Join("other", "parent2", "app"), 1)
	if a == b {
		t.Fatalf("same-named roots collide: %q == %q", a, b)
	}
	// Master's "%02d-base" format: zero-padded index, dash separator.
	if !strings.HasPrefix(a, "00-") || !strings.HasPrefix(b, "01-") {
		t.Errorf("index prefix missing: %q, %q", a, b)
	}
	if !strings.HasSuffix(a, "app") || !strings.HasSuffix(b, "app") {
		t.Errorf("base lost: %q, %q", a, b)
	}
	// Sanitization still applies alongside the index.
	got := safeName(`D:\my proj`, 3)
	if !strings.HasPrefix(got, "03-") || strings.ContainsAny(got, ": ") {
		t.Errorf("sanitize+index: got %q", got)
	}
}

func TestCopyFilteredSkipsMissingRoots(t *testing.T) {
	dest := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	copied, skipped, err := CopyFiltered([]string{missing}, dest, 1<<20, "", t.TempDir())
	if err != nil {
		t.Fatalf("missing root should skip, got error: %v", err)
	}
	if len(copied) != 0 || len(skipped) != 0 {
		t.Errorf("missing root should yield nothing, got %d copied %d skipped", len(copied), len(skipped))
	}
}

func TestCopyFilteredPropagatesCopyError(t *testing.T) {
	src := mksrc(t)
	writeFile(t, filepath.Join(src, "blocked.txt"), "data")
	dest := t.TempDir()
	// Block the destination path with a directory so os.Create fails.
	blocked := filepath.Join(dest, safeName(src, 0), "blocked.txt")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := CopyFiltered([]string{src}, dest, 1<<20, "", t.TempDir())
	if err == nil {
		t.Fatal("CopyFiltered should return the copy error, got nil")
	}
	if !strings.Contains(err.Error(), "blocked.txt") {
		t.Errorf("error should name the file, got: %v", err)
	}
}

func TestRootsForJoinsLeafDotUnderEachRoot(t *testing.T) {
	// RootsFor resolves leaves/roots from project + platform globals
	// (no injectable seam); the branch-local RootsForIn seam was dropped
	// at merge. RootsFor shape is covered by TestAuditRootsForHomeAndLeaves
	// in walk_audit_test.go. This test pins the join math used to build
	// per-leaf dot-dir roots.
	home := filepath.Join("fake", "home")
	r1 := filepath.Join("fake", "r1")
	leaf := filepath.Join("a", "b")
	dot := ".dot"
	if got := filepath.Join(r1, leaf, dot); got != filepath.Join("fake", "r1", "a", "b", ".dot") {
		t.Fatalf("join math = %q", got)
	}
	if got := filepath.Join(home, ".agent"); got != filepath.Join("fake", "home", ".agent") {
		t.Fatalf("home join = %q", got)
	}
}

func srcIndex(t *testing.T, g genericAdapter, src string) int {
	t.Helper()
	for i, r := range g.roots() {
		if r == src {
			return i
		}
	}
	t.Fatalf("src %q not in roots %q", src, g.roots())
	return -1
}

func TestExportPropagatesCopyError(t *testing.T) {
	src := mksrc(t)
	writeFile(t, filepath.Join(src, "blocked.txt"), "data")
	vault := t.TempDir()
	g := genericAdapter{name: "testexporterr", absRoots: []string{src}, maxBytes: 1 << 20}
	blocked := filepath.Join(g.rawDir(vault), safeName(src, srcIndex(t, g, src)), "blocked.txt")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := g.Export(vault); err == nil {
		t.Fatal("Export should propagate the copy error, got nil")
	}
	// index.json is always written (manifest records partial state even on
	// failure); the error — not a missing index — signals the failure.
	if _, err := os.Stat(g.indexPath(vault)); err != nil {
		t.Errorf("failed export must still write index.json (stat err: %v)", err)
	}
}

func TestExportSuccessWritesIndex(t *testing.T) {
	src := mksrc(t)
	writeFile(t, filepath.Join(src, "notes.txt"), "hello")
	vault := t.TempDir()
	g := genericAdapter{name: "testexportok", absRoots: []string{src}, maxBytes: 1 << 20}
	if err := g.Export(vault); err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(g.rawDir(vault), safeName(src, srcIndex(t, g, src)), "notes.txt"))
	if err != nil {
		t.Fatalf("raw copy missing: %v", err)
	}
	if string(raw) != "hello" {
		t.Errorf("raw copy content = %q, want %q", raw, "hello")
	}
	idx, err := os.ReadFile(g.indexPath(vault))
	if err != nil {
		t.Fatalf("index.json missing after successful export: %v", err)
	}
	if !strings.Contains(string(idx), "notes.txt") {
		t.Errorf("index.json should list the file, got: %s", idx)
	}
}
