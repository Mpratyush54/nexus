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

func TestSafeNameIndexedCollisionFree(t *testing.T) {
	a := safeName(filepath.Join("some", "parent1", "app"), 0)
	b := safeName(filepath.Join("other", "parent2", "app"), 1)
	if a == b {
		t.Fatalf("same-named roots collide: %q == %q", a, b)
	}
	if !strings.HasPrefix(a, "0_") || !strings.HasPrefix(b, "1_") {
		t.Errorf("index prefix missing: %q, %q", a, b)
	}
	if !strings.HasSuffix(a, "app") || !strings.HasSuffix(b, "app") {
		t.Errorf("base lost: %q, %q", a, b)
	}
	// Sanitization still applies alongside the index.
	got := safeName(`D:\my proj`, 3)
	if !strings.HasPrefix(got, "3_") || strings.ContainsAny(got, ": ") {
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
	src := t.TempDir()
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

func TestRootsForInJoinsLeafDotUnderEachRoot(t *testing.T) {
	home := filepath.Join("fake", "home")
	r1 := filepath.Join("fake", "r1")
	r2 := filepath.Join("fake", "r2")
	got := RootsForIn(home, []string{".agent"}, []string{".dot"}, []string{"a/b"}, []string{r1, r2})
	want := map[string]bool{
		filepath.Join(home, ".agent"):       true,
		filepath.Join(r1, "a", "b", ".dot"): true,
		filepath.Join(r2, "a", "b", ".dot"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %d entries", got, len(want))
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected root %q (full: %q)", g, got)
		}
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
	src := t.TempDir()
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
	// index.json is written only on success: a failed export must not leave
	// one behind to be mistaken for a clean export.
	if _, err := os.Stat(g.indexPath(vault)); !os.IsNotExist(err) {
		t.Errorf("failed export must not write index.json (stat err: %v)", err)
	}
}

func TestExportSuccessWritesIndex(t *testing.T) {
	src := t.TempDir()
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
