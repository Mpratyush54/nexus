package teleport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPreparePlanDryRun(t *testing.T) {
	root := t.TempDir()
	plan := BuildPreparePlan(PrepareArgs{
		SessionID: "asess_abcd1234ef",
		Root:      root,
		Commit:    "9c1dabc0deadbeef",
		FromOS:    "mac",
		ToOS:      "windows",
		FromHome:  "/Users/priya",
		ToHome:    `C:\Users\pratyush`,
		Agent:     "claude",
		Text:      "ping ada@ex.com from /Users/priya/code",
		Home:      "/Users/priya",
		Secrets:   []string{".env", "keys/id"},
	})
	if !plan.DryRun || !plan.Repo.Found {
		t.Fatalf("repo: %+v", plan.Repo)
	}
	if plan.Commit.Action != "fetch" && plan.Commit.Action != "present" && plan.Commit.Action != "missing" {
		t.Fatalf("commit action=%q", plan.Commit.Action)
	}
	if plan.Target.Mode != "worktree" || plan.Target.WorktreePath == "" {
		t.Fatalf("target: %+v", plan.Target)
	}
	if plan.Agent.Name != "claude" {
		t.Fatalf("agent=%q", plan.Agent.Name)
	}
	if len(plan.Paths) == 0 || plan.Secrets.Mode != "names_only" || len(plan.Secrets.Names) != 2 {
		t.Fatalf("paths/secrets: %+v %+v", plan.Paths, plan.Secrets)
	}
	if len(plan.Continue) < 2 {
		t.Fatalf("continue modes: %+v", plan.Continue)
	}
	if plan.Preview == "" || plan.Preview == "ping ada@ex.com from /Users/priya/code" {
		t.Fatalf("preview not redacted: %q", plan.Preview)
	}
}

func TestBuildPreparePlanMissingRoot(t *testing.T) {
	plan := BuildPreparePlan(PrepareArgs{SessionID: "s1", Agent: "cursor"})
	if plan.Repo.Found {
		t.Fatal("expected missing repo")
	}
	if plan.Target.Mode != "worktree" {
		t.Fatalf("mode=%q", plan.Target.Mode)
	}
	_ = filepath.Join(os.TempDir())
}
