package handoff

import (
	"errors"
	"strings"
	"testing"
)

func samplePackage() (*HandoffPackage, Event) {
	return BuildHandoffFull(
		"proj-1", "sess-1", "alice", "bob",
		"claude", "copilot",
		TaskStatus{ID: "t-1", Title: "Fix login race", Description: "guard session refresh", Status: "IN_PROGRESS", AssignedTo: "alice"},
		[]MemorySnippet{{Key: "gotcha", Content: "refresh must hold mutex", Level: "session"}},
		[]string{"internal/server/ws.go", "internal/store/sessions.go"},
		BranchState{Branch: "feat/handoff", CommitSHA: "abc123", IsDirty: true},
		"halfway through, tests green",
	)
}

func TestBuildAcceptRoundTrip(t *testing.T) {
	pkg, initEv := samplePackage()
	if initEv.Type != EventHandoffInitiated {
		t.Fatalf("init event = %q, want %q", initEv.Type, EventHandoffInitiated)
	}
	if pkg.ID == "" || pkg.Accepted {
		t.Fatalf("fresh package must have ID and Accepted=false, got %+v", pkg)
	}
	if initEv.HandoffID != pkg.ID {
		t.Fatalf("event handoff %q != package %q", initEv.HandoffID, pkg.ID)
	}

	accepted, acceptEv, err := AcceptHandoff(pkg, "bob")
	if err != nil {
		t.Fatalf("AcceptHandoff: %v", err)
	}
	if acceptEv.Type != EventHandoffAccepted {
		t.Fatalf("accept event = %q, want %q", acceptEv.Type, EventHandoffAccepted)
	}
	if !accepted.Accepted || accepted.AcceptedBy != "bob" || accepted.AcceptedAt.IsZero() {
		t.Fatalf("accept not stamped: %+v", accepted)
	}
	if accepted.Task.AssignedTo != "bob" {
		t.Fatalf("task assignee = %q, want bob", accepted.Task.AssignedTo)
	}
	// Task status, memories, files, branch survive the round-trip.
	if accepted.Task.Status != "IN_PROGRESS" || len(accepted.Memories) != 1 ||
		len(accepted.ModifiedFiles) != 2 || accepted.Branch.Branch != "feat/handoff" {
		t.Fatalf("package content lost in round-trip: %+v", accepted)
	}

	// Double accept conflicts.
	if _, _, err := AcceptHandoff(pkg, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("double accept err = %v, want ErrConflict", err)
	}
	// Wrong recipient conflicts.
	fresh, _ := samplePackage()
	if _, _, err := AcceptHandoff(fresh, "mallory"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-recipient accept err = %v, want ErrConflict", err)
	}
}

func TestAdvisoryLockConflict(t *testing.T) {
	locks := NewLockSet()
	if err := locks.Acquire("a.go", "alice"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Same owner re-acquire is a no-op.
	if err := locks.Acquire("a.go", "alice"); err != nil {
		t.Fatalf("re-acquire own lock: %v", err)
	}
	// Conflicting owner fails.
	if err := locks.Acquire("a.go", "bob"); !errors.Is(err, ErrLocked) {
		t.Fatalf("conflict err = %v, want ErrLocked", err)
	}
	if got := locks.Holder("a.go"); got != "alice" {
		t.Fatalf("holder = %q, want alice", got)
	}
	// Release by wrong owner fails; by holder succeeds.
	if err := locks.Release("a.go", "bob"); err == nil {
		t.Fatal("release by non-holder should fail")
	}
	if err := locks.Release("a.go", "alice"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := locks.Holder("a.go"); got != "" {
		t.Fatalf("holder after release = %q, want empty", got)
	}
}

func TestAdvisoryLockAllRollback(t *testing.T) {
	locks := NewLockSet()
	if err := locks.Acquire("shared.go", "alice"); err != nil {
		t.Fatalf("setup acquire: %v", err)
	}
	// LockAll hitting a conflict must roll back the files it just took.
	err := locks.LockAll([]string{"fresh.go", "shared.go"}, "bob")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("LockAll err = %v, want ErrLocked", err)
	}
	if got := locks.Holder("fresh.go"); got != "" {
		t.Fatalf("fresh.go holder = %q after rollback, want empty", got)
	}
	if got := locks.Holder("shared.go"); got != "alice" {
		t.Fatalf("shared.go holder = %q, want alice", got)
	}
	// UnlockAll is idempotent and scoped to the owner.
	locks.UnlockAll([]string{"fresh.go", "shared.go"}, "bob")
	if got := locks.Holder("shared.go"); got != "alice" {
		t.Fatalf("UnlockAll by non-owner must not steal, holder = %q", got)
	}
}

func TestTranslationShapes(t *testing.T) {
	pkg, _ := samplePackage()

	mcp := TranslateForAgent(pkg, "claude-code")
	if mcp.Format != "mcp-context-block" {
		t.Fatalf("claude format = %q, want mcp-context-block", mcp.Format)
	}
	for _, want := range []string{"<handoff_context>", "Fix login race", "internal/server/ws.go", "feat/handoff"} {
		if !strings.Contains(mcp.Content, want) {
			t.Fatalf("MCP block missing %q:\n%s", want, mcp.Content)
		}
	}

	copilot := TranslateForAgent(pkg, "copilot")
	if copilot.Format != "instruction-file" {
		t.Fatalf("copilot format = %q, want instruction-file", copilot.Format)
	}
	for _, want := range []string{"Copilot", "Fix login race", "internal/server/ws.go", ".github/muse-instructions.md"} {
		if !strings.Contains(copilot.Content, want) {
			t.Fatalf("copilot file missing %q:\n%s", want, copilot.Content)
		}
	}

	cursor := TranslateForAgent(pkg, "cursor")
	if cursor.Format != "instruction-file" {
		t.Fatalf("cursor format = %q, want instruction-file", cursor.Format)
	}
	if !strings.Contains(cursor.Content, ".cursorrules") {
		t.Fatalf("cursor file should target .cursorrules:\n%s", cursor.Content)
	}

	generic := TranslateForAgent(pkg, "opencode")
	if generic.Format != "markdown" || !strings.Contains(generic.Content, "Fix login race") {
		t.Fatalf("generic translation wrong: %+v", generic)
	}
}
