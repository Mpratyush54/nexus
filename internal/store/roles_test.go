package store

// roles_test.go — unit tests for RBAC helpers + MemStore RoleStore (issue #163).

import (
	"context"
	"errors"
	"testing"
)

func TestBuiltinRolePermissions(t *testing.T) {
	owner, ok := BuiltinRolePermissions(RoleOwner)
	if !ok || len(owner) != len(AllPermissions) {
		t.Fatalf("OWNER perms = %d ok=%v, want all %d", len(owner), ok, len(AllPermissions))
	}
	admin, _ := BuiltinRolePermissions(RoleAdmin)
	if len(admin) != len(AllPermissions) {
		t.Fatalf("ADMIN should have all permissions, got %d", len(admin))
	}
	editor, ok := BuiltinRolePermissions(RoleEditor)
	if !ok || roleHasPerm(editor, PermMemberInvite) || roleHasPerm(editor, PermMemberManage) {
		t.Fatalf("EDITOR must not manage members: %v", editor)
	}
	if !roleHasPerm(editor, PermMemoryWrite) {
		t.Fatal("EDITOR needs memory:write")
	}
	viewer, ok := BuiltinRolePermissions(RoleViewer)
	if !ok || roleHasPerm(viewer, PermMemoryWrite) || !roleHasPerm(viewer, PermMemoryRead) {
		t.Fatalf("VIEWER read-only: %v", viewer)
	}
}

func TestNormalizePermissionsRejectsUnknown(t *testing.T) {
	_, err := NormalizePermissions([]string{PermMemoryRead, "memory:explode"})
	if err == nil {
		t.Fatal("expected unknown permission error")
	}
	got, err := NormalizePermissions([]string{PermMemoryWrite, PermMemoryRead, PermMemoryWrite})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != PermMemoryRead || got[1] != PermMemoryWrite {
		t.Fatalf("got %v", got)
	}
}

func TestMemStoreRoleCRUDAndPermissions(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	p, err := s.ResolveProject(ctx, "", "", "rbac-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, p.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	// Owner has everything.
	ok, err := s.HasPermission(ctx, "alice", p.ID, PermMemberManage)
	if err != nil || !ok {
		t.Fatalf("owner manage = %v %v", ok, err)
	}

	if err := s.GrantMember(ctx, p.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	role, err := s.GetMemberRole(ctx, "bob", p.ID)
	if err != nil || role != RoleEditor {
		t.Fatalf("bob role = %q %v, want EDITOR", role, err)
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemoryWrite)
	if !ok {
		t.Fatal("editor should write memory")
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemberInvite)
	if ok {
		t.Fatal("editor must not invite")
	}

	if err := s.SetMemberRole(ctx, p.ID, "bob", RoleViewer); err != nil {
		t.Fatal(err)
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemoryWrite)
	if ok {
		t.Fatal("viewer must not write")
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemoryRead)
	if !ok {
		t.Fatal("viewer should read")
	}

	custom := &ProjectRole{
		ProjectID:   p.ID,
		Name:        "Reviewer",
		Description: "confirm only",
		Permissions: []string{PermMemoryRead, PermMemoryConfirm},
	}
	if err := s.CreateProjectRole(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if custom.ID == "" || custom.IsBuiltin {
		t.Fatalf("unexpected custom role: %+v", custom)
	}
	if err := s.SetMemberRole(ctx, p.ID, "bob", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemoryConfirm)
	if !ok {
		t.Fatal("custom reviewer should confirm")
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemoryWrite)
	if ok {
		t.Fatal("custom reviewer must not write")
	}

	roles, err := s.ListProjectRoles(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) < 5 { // 4 builtin + 1 custom
		t.Fatalf("list roles = %d, want >= 5", len(roles))
	}

	custom.Permissions = []string{PermMemoryRead}
	if err := s.UpdateProjectRole(ctx, custom); err != nil {
		t.Fatal(err)
	}
	ok, _ = s.HasPermission(ctx, "bob", p.ID, PermMemoryConfirm)
	if ok {
		t.Fatal("after update, confirm should be gone")
	}

	if err := s.DeleteProjectRole(ctx, p.ID, custom.ID); err != nil {
		t.Fatal(err)
	}
	role, _ = s.GetMemberRole(ctx, "bob", p.ID)
	if role != RoleViewer {
		t.Fatalf("after delete custom, bob role = %q, want VIEWER", role)
	}

	// Cannot change creator role away from OWNER.
	if err := s.SetMemberRole(ctx, p.ID, "alice", RoleAdmin); !errors.Is(err, ErrConflict) {
		t.Fatalf("demote creator = %v, want conflict", err)
	}
	// Cannot create built-in name.
	if err := s.CreateProjectRole(ctx, &ProjectRole{ProjectID: p.ID, Name: "OWNER", Permissions: nil}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create OWNER = %v, want conflict", err)
	}
}
