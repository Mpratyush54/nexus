package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func auditFakeFP(origin, root string) FingerprintFunc {
	return func(dir string) (string, string) { return origin, root }
}

func TestAuditSeedProjectsKeys(t *testing.T) {
	// Same normalized URL (different spellings) -> one seed, first wins.
	got := SeedProjectsWith([]string{"D:/x/proj", "D:/y/proj"},
		func(dir string) (string, string) {
			if strings.Contains(dir, "x") {
				return "git@github.com:Org/Repo.git", ""
			}
			return "https://github.com/org/repo", ""
		})
	if len(got) != 1 || got[0].CanonicalURL != "github.com/org/repo" || got[0].DisplayName != "proj" {
		t.Errorf("URL dedup: %+v", got)
	}
	// Root fallback, then folder fallback (case-insensitive).
	got = SeedProjectsWith([]string{"D:/a/P", "D:/b/P"}, auditFakeFP("", "abc123"))
	if len(got) != 1 || got[0].RootCommit != "abc123" {
		t.Errorf("root fallback: %+v", got)
	}
	got = SeedProjectsWith([]string{"D:/a/Proj", "D:/b/proj"}, nil)
	if len(got) != 1 || got[0].FolderName != "Proj" {
		t.Errorf("folder fallback (first wins): %+v", got)
	}
	// Nil fingerprinter never panics.
	if got := SeedProjectsWith(nil, nil); len(got) != 0 {
		t.Errorf("nil dirs: %+v", got)
	}
	if CLIUsage == "" || !strings.Contains(CLIUsage, "nexus migrate") {
		t.Error("CLIUsage must document the entrypoint")
	}
}

func writeAuditMigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRunWithCounts(t *testing.T) {
	vault := t.TempDir()
	writeAuditMigFile(t, filepath.Join(vault, "memory", "global", "learnings.md"),
		"# L\n\n## 2026-09-01 10:00\ntags: a\n\nGlobal memory number one is long enough.\n\n## short\n\ntiny\n")
	writeAuditMigFile(t, filepath.Join(vault, "memory", "projects", "projA", "MEMORY.md"),
		"## 2026-09-02 10:00\n\nProject memory number one is long enough.\n")
	writeAuditMigFile(t, filepath.Join(vault, "agents", "cursor", "normalized", "sessions.jsonl"),
		"{\"session_id\":\"s1\",\"project\":\"p\"}\nbad-line\n")
	c, err := RunWith(vault, []string{"D:/w/one"}, true, auditFakeFP("https://github.com/o/r", ""))
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if !c.DryRun || c.Memories != 2 || c.Events != 2 || c.Projects != 1 || c.Skipped != 2 {
		t.Errorf("counts wrong: %+v (want dry,2 mem,2 ev,1 proj,2 skipped)", c)
	}
	// Cross-file duplicate content dedups to one memory.
	writeAuditMigFile(t, filepath.Join(vault, "memory", "projects", "projB", "MEMORY.md"),
		"## 2026-09-03 10:00\n\nGlobal memory number one is long enough.\n")
	c2, err := RunWith(vault, nil, true, nil)
	if err != nil {
		t.Fatalf("RunWith dedup: %v", err)
	}
	if c2.Memories != 2 {
		t.Errorf("dedup memories = %d, want 2 (global dup in projB collapses)", c2.Memories)
	}
	// Missing vault errors; blank path errors.
	if _, err := RunWith(filepath.Join(vault, "nope"), nil, true, nil); err == nil {
		t.Error("missing vault must error")
	}
	if _, err := RunWith("  ", nil, true, nil); err == nil {
		t.Error("blank vault must error")
	}
	// Partial vault (no files at all) succeeds with zeros.
	empty := t.TempDir()
	c3, err := RunWith(empty, nil, true, nil)
	if err != nil || c3.Memories != 0 || c3.Events != 0 {
		t.Errorf("partial vault: %+v %v", c3, err)
	}
}
