package handoff

import (
	"strings"
	"testing"
)

func TestAuditBuildHandoffShapes(t *testing.T) {
	task := TaskStatus{ID: "t1", Title: "Fix login", Status: "IN_PROGRESS", AssignedTo: "alice"}
	mems := []MemorySnippet{{Key: "k", Content: "c", Level: "session"}}
	files := []string{"a.go", "b.go"}
	pkg, ev := BuildHandoff("proj", "sess", "alice", "bob", task, mems, files, BranchState{Branch: "feat", CommitSHA: "abc", IsDirty: true})
	if pkg.ID == "" || pkg.ProjectID != "proj" || pkg.FromUser != "alice" || pkg.ToUser != "bob" {
		t.Errorf("routing wrong: %+v", pkg)
	}
	if pkg.Accepted || pkg.AcceptedBy != "" || !pkg.CreatedAt.IsZero() == false {
		t.Errorf("fresh package must be unaccepted with CreatedAt set: %+v", pkg)
	}
	if ev.Type != EventHandoffInitiated || ev.HandoffID != pkg.ID {
		t.Errorf("init event wrong: %+v", ev)
	}
	if ev.Payload["task_id"] != "t1" || ev.Payload["memories_count"] != 1 {
		t.Errorf("init payload wrong: %+v", ev.Payload)
	}
	// Deep copy: caller mutation must not corrupt package.
	mems[0].Key = "MUT"
	files[0] = "MUT"
	if pkg.Memories[0].Key == "MUT" || pkg.ModifiedFiles[0] == "MUT" {
		t.Error("slices must be deep-copied")
	}
	// IDs unique.
	p2, _ := BuildHandoff("proj", "sess", "alice", "bob", task, nil, nil, BranchState{})
	if p2.ID == pkg.ID {
		t.Error("handoff IDs must be unique")
	}
	// Full variant carries agents + note.
	pf, evf := BuildHandoffFull("p", "s", "a", "b", "claude", "copilot", task, nil, nil, BranchState{Branch: "m"}, "note-hi")
	if pf.FromAgent != "claude" || pf.ToAgent != "copilot" || pf.Note != "note-hi" {
		t.Errorf("full routing wrong: %+v", pf)
	}
	if evf.Payload["to_agent"] != "copilot" {
		t.Errorf("full payload to_agent wrong: %+v", evf.Payload)
	}
}

func TestAuditAcceptHandoff(t *testing.T) {
	task := TaskStatus{ID: "t1", Title: "T", Status: "OPEN", AssignedTo: "alice"}
	pkg, _ := BuildHandoff("p", "s", "alice", "bob", task, nil, nil, BranchState{})
	got, ev, err := AcceptHandoff(pkg, "bob")
	if err != nil || !got.Accepted || got.AcceptedBy != "bob" || got.AcceptedAt.IsZero() {
		t.Fatalf("accept failed: %+v %v", got, err)
	}
	if got.Task.AssignedTo != "bob" {
		t.Errorf("task must re-point at accepter: %+v", got.Task)
	}
	if ev.Type != EventHandoffAccepted || ev.HandoffID != pkg.ID || ev.ToUser != "bob" {
		t.Errorf("accept event wrong: %+v", ev)
	}
	// Double accept conflicts.
	if _, _, err := AcceptHandoff(pkg, "bob"); err == nil {
		t.Error("double accept must fail")
	}
	// Wrong recipient conflicts.
	pkg2, _ := BuildHandoff("p", "s", "alice", "bob", task, nil, nil, BranchState{})
	if _, _, err := AcceptHandoff(pkg2, "mallory"); err == nil {
		t.Error("wrong-recipient accept must fail")
	}
	// Nil package errors.
	if _, _, err := AcceptHandoff(nil, "bob"); err == nil {
		t.Error("nil package must error")
	}
	// Empty ToUser + empty byUser: allowed (no constraint to violate).
	pkg3, _ := BuildHandoff("p", "s", "alice", "", task, nil, nil, BranchState{})
	if _, _, err := AcceptHandoff(pkg3, ""); err != nil {
		t.Errorf("open accept should succeed: %v", err)
	}
}

func TestAuditLockSet(t *testing.T) {
	l := NewLockSet()
	if err := l.Acquire("f.go", "alice"); err != nil {
		t.Fatal(err)
	}
	if l.Holder("f.go") != "alice" {
		t.Error("holder wrong")
	}
	// Re-acquire own is no-op.
	if err := l.Acquire("f.go", "alice"); err != nil {
		t.Errorf("own re-acquire: %v", err)
	}
	// Other owner locked out.
	if err := l.Acquire("f.go", "bob"); err == nil {
		t.Error("conflicting acquire must fail")
	}
	// Release by wrong owner / unheld errors.
	if err := l.Release("f.go", "bob"); err == nil {
		t.Error("stray unlock must fail")
	}
	if err := l.Release("ghost.go", "alice"); err == nil {
		t.Error("unlock of unheld must fail")
	}
	if err := l.Release("f.go", "alice"); err != nil {
		t.Errorf("owner release: %v", err)
	}
	// Whitespace-trimmed paths; blank rejected.
	if err := l.Acquire("  ", "alice"); err == nil {
		t.Error("blank path must be rejected")
	}
	if err := l.Acquire("g.go", ""); err == nil {
		t.Error("blank owner must be rejected")
	}
	// LockAll rollback: partial acquisition leaves no residue.
	_ = l.Acquire("busy.go", "bob")
	if err := l.LockAll([]string{"fresh.go", "busy.go"}, "alice"); err == nil {
		t.Error("LockAll with conflict must fail")
	}
	if l.Holder("fresh.go") != "" {
		t.Error("LockAll must roll back partial acquisitions")
	}
	// UnlockAll idempotent skip.
	_ = l.Acquire("u1.go", "alice")
	l.UnlockAll([]string{"u1.go", "other.go"}, "alice")
	l.UnlockAll([]string{"u1.go"}, "alice") // second pass must not error/panic
	if l.Holder("u1.go") != "" {
		t.Error("UnlockAll must release")
	}
	// UnlockAll only releases own.
	_ = l.Acquire("v.go", "bob")
	l.UnlockAll([]string{"v.go"}, "alice")
	if l.Holder("v.go") != "bob" {
		t.Error("UnlockAll must skip others' locks")
	}
}

func TestAuditTranslations(t *testing.T) {
	pkg, _ := BuildHandoffFull("p", "s", "alice", "bob", "claude", "copilot",
		TaskStatus{ID: "t", Title: "Fix <login> & go", Description: "desc", Status: "IN_PROGRESS", AssignedTo: "bob"},
		[]MemorySnippet{{Key: "k&1", Content: "c<d>"}},
		[]string{"a.go"}, BranchState{Branch: "feat", CommitSHA: "sha", IsDirty: true}, "note-n")
	cl := TranslateForAgent(pkg, "claude-code")
	if cl.Format != "mcp-context-block" || !strings.Contains(cl.Content, "<handoff_context>") {
		t.Errorf("claude shape wrong: %+v", cl)
	}
	if strings.Contains(cl.Content, "<login>") {
		t.Error("MCP block must XML-escape")
	}
	co := TranslateForAgent(pkg, "Copilot")
	if co.Format != "instruction-file" || !strings.Contains(co.Content, "Copilot") {
		t.Errorf("copilot shape wrong: %+v", co)
	}
	cu := TranslateForAgent(pkg, "cursor")
	if cu.Format != "instruction-file" || !strings.Contains(cu.Content, "Cursor") {
		t.Errorf("cursor shape wrong: %+v", cu)
	}
	gen := TranslateForAgent(pkg, "opencode")
	if gen.Format != "markdown" || !strings.Contains(gen.Content, "Session handoff") {
		t.Errorf("fallback markdown wrong: %+v", gen)
	}
	// Nil package yields skeletal (non-empty) block, never panics.
	nilTr := TranslateForAgent(nil, "claude")
	if nilTr.Content == "" || !strings.Contains(nilTr.Content, "<handoff_context>") {
		t.Errorf("nil package must yield skeletal block: %+v", nilTr)
	}
	// Unknown agent falls back to markdown.
	if tr := TranslateForAgent(pkg, ""); tr.Format != "markdown" {
		t.Errorf("empty agent must fall back to markdown: %+v", tr)
	}
}
