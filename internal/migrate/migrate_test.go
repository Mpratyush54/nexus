package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeFP(mapDir map[string][2]string) FingerprintFunc {
	return func(dir string) (string, string) {
		if v, ok := mapDir[dir]; ok {
			return v[0], v[1]
		}
		return "", ""
	}
}

func TestSeedProjectsDedupsByURL(t *testing.T) {
	fp := fakeFP(map[string][2]string{
		`D:\a`: {"git@github.com:Acme/Shop.git", "root1"},
		`D:\b`: {"https://github.com/acme/shop", "root1"}, // same repo, other spelling
		`D:\c`: {"git@github.com:Acme/Blog.git", "root2"},
	})
	seeds := SeedProjectsWith([]string{`D:\a`, `D:\b`, `D:\c`}, fp)
	if len(seeds) != 2 {
		t.Fatalf("got %d seeds, want 2 (a+b converge)", len(seeds))
	}
	if seeds[0].CanonicalURL != "github.com/acme/shop" {
		t.Errorf("canonical = %q, want normalized github.com/acme/shop", seeds[0].CanonicalURL)
	}
}

func TestSeedProjectsFallsBackToRootThenFolder(t *testing.T) {
	fp := fakeFP(map[string][2]string{
		`D:\a`: {"", "abc123"}, // no remote: root commit identity
		`D:\b`: {"", "abc123"}, // same repo, other checkout -> dup
		`D:\c`: {"", ""},       // no git at all: folder fallback
		`D:\C`: {"", ""},       // same folder, other case -> dup
	})
	seeds := SeedProjectsWith([]string{`D:\a`, `D:\b`, `D:\c`, `D:\C`}, fp)
	if len(seeds) != 2 {
		t.Fatalf("got %d seeds, want 2 (root-dup + folder-dup collapse)", len(seeds))
	}
	if seeds[0].RootCommit != "abc123" {
		t.Errorf("root = %q, want abc123", seeds[0].RootCommit)
	}
}

func writeVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("memory/global/learnings.md", learningsFixture)
	mustWrite("memory/projects/shop/MEMORY.md",
		"## 2026-09-11 10:00\ntags: deploy\n\nThe team decided on blue-green deploys after the Friday outage postmortem.\n")
	mustWrite("agents/claude/normalized/sessions.jsonl", sessionsFixture)
	return root
}

func TestRunWithDryRunCounts(t *testing.T) {
	root := writeVault(t)
	fp := fakeFP(map[string][2]string{
		`D:\a`: {"git@github.com:Acme/Shop.git", "root1"},
		`D:\b`: {"git@github.com:Acme/Shop.git", "root1"},
	})
	c, err := RunWith(root, []string{`D:\a`, `D:\b`}, true, fp)
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if !c.DryRun {
		t.Errorf("DryRun not echoed")
	}
	if c.Memories != 4 { // 3 global + 1 project
		t.Errorf("memories = %d, want 4", c.Memories)
	}
	if c.Projects != 1 { // a+b are the same repo
		t.Errorf("projects = %d, want 1", c.Projects)
	}
	if c.Events != 6 { // 3 unique sessions x STARTED/ENDED
		t.Errorf("events = %d, want 6", c.Events)
	}
	if c.Skipped != 3 { // sessions fixture skips; markdown all valid
		t.Errorf("skipped = %d, want 3", c.Skipped)
	}
}

func TestRunMissingVaultErrors(t *testing.T) {
	if _, err := RunWith(filepath.Join(t.TempDir(), "nope"), nil, true, nil); err == nil {
		t.Fatal("want error for missing vault, got nil")
	}
	if _, err := RunWith("", nil, true, nil); err == nil {
		t.Fatal("want error for empty vault path, got nil")
	}
}

func TestRunToleratesPartialVault(t *testing.T) {
	root := t.TempDir() // no memory/, no agents/
	c, err := RunWith(root, nil, true, nil)
	if err != nil {
		t.Fatalf("partial vault should not error: %v", err)
	}
	if c.Memories != 0 || c.Events != 0 || c.Projects != 0 {
		t.Errorf("empty vault counts = %+v, want zeros", c)
	}
}
