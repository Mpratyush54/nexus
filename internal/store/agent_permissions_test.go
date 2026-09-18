// agent_permissions_test.go — MemStore agent permission CRUD + gate checks.
package store

import (
	"context"
	"testing"
)

func TestAgentPermissionCRUD(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "https://github.com/a/b.git", "abc", "b")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetAgentPermission(ctx, p.ID, "cursor-agent"); err != ErrNotFound {
		t.Fatalf("Get missing: %v", err)
	}

	perm := &AgentPermission{
		ProjectID: p.ID,
		AgentID:   "cursor-agent",
		Mode:      AgentModeProposeOnly,
		RateLimit: 10,
		Tools: map[string]bool{
			"memory_search": true,
			"memory_write":  true,
			"file_write":    false,
		},
	}
	if err := s.SetAgentPermission(ctx, perm); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgentPermission(ctx, p.ID, "cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != AgentModeProposeOnly || got.RateLimit != 10 {
		t.Fatalf("got %+v", got)
	}
	if !got.Tools["memory_search"] || got.Tools["file_write"] {
		t.Fatalf("tools %+v", got.Tools)
	}

	list, err := s.ListAgentPermissions(ctx, p.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	if err := s.DeleteAgentPermission(ctx, p.ID, "cursor-agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgentPermission(ctx, p.ID, "cursor-agent"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}

func TestCheckAgentToolAllowed(t *testing.T) {
	if err := CheckAgentToolAllowed(nil, "memory_write"); err != nil {
		t.Fatalf("nil perm should allow: %v", err)
	}
	blocked := &AgentPermission{AgentID: "a", Mode: AgentModeBlocked}
	if err := CheckAgentToolAllowed(blocked, "memory_search"); err == nil {
		t.Fatal("blocked should deny")
	}
	ro := &AgentPermission{AgentID: "a", Mode: AgentModeReadOnly}
	if err := CheckAgentToolAllowed(ro, "memory_search"); err != nil {
		t.Fatalf("read_only search: %v", err)
	}
	if err := CheckAgentToolAllowed(ro, "memory_write"); err == nil {
		t.Fatal("read_only write should deny")
	}
	tools := &AgentPermission{
		AgentID: "a", Mode: AgentModeFull,
		Tools: map[string]bool{"file_write": false},
	}
	if err := CheckAgentToolAllowed(tools, "file_write"); err == nil {
		t.Fatal("explicit deny should block")
	}
	if err := CheckAgentToolAllowed(tools, "memory_search"); err != nil {
		t.Fatalf("unset tool should allow under full: %v", err)
	}
}

func TestNormalizeAgentMode(t *testing.T) {
	m, err := NormalizeAgentMode("propose-only")
	if err != nil || m != AgentModeProposeOnly {
		t.Fatalf("got %q %v", m, err)
	}
	if _, err := NormalizeAgentMode("nope"); err == nil {
		t.Fatal("expected error")
	}
}
