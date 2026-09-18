package server

// rbac_routes_test.go — HTTP coverage for role CRUD + permission gates (issue #163).

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestRoleListIncludesBuiltins(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "roles-list")

	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/roles", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []*store.ProjectRole `json:"items"`
		Count int                  `json:"count"`
	}
	decodeBody(t, rec, &out)
	if out.Count < 4 {
		t.Fatalf("expected >=4 built-ins, got %d", out.Count)
	}
	names := map[string]bool{}
	for _, r := range out.Items {
		names[r.Name] = r.IsBuiltin
	}
	for _, want := range []string{store.RoleOwner, store.RoleAdmin, store.RoleEditor, store.RoleViewer} {
		if !names[want] {
			t.Fatalf("missing builtin %s in %+v", want, names)
		}
	}
}

func TestRoleCRUDAndMemberAssign(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	proj := resolveTestProject(t, s, alice, "roles-crud")

	// Grant bob as EDITOR (default).
	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant = %d body=%s", rec.Code, rec.Body.String())
	}

	// Bob (editor) cannot create roles.
	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj+"/roles", bob, map[string]any{
		"name":        "Auditor",
		"permissions": []string{store.PermMemoryRead},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor create role = %d, want 403", rec.Code)
	}

	// Alice creates custom role.
	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj+"/roles", alice, map[string]any{
		"name":        "Auditor",
		"description": "read + confirm",
		"permissions": []string{store.PermMemoryRead, store.PermMemoryConfirm},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create role = %d body=%s", rec.Code, rec.Body.String())
	}
	var role store.ProjectRole
	decodeBody(t, rec, &role)
	if role.ID == "" || role.Name != "Auditor" || len(role.Permissions) != 2 {
		t.Fatalf("unexpected role: %+v", role)
	}

	// Assign bob → Auditor.
	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj+"/members/bob/role", alice, map[string]string{
		"role": "Auditor",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set role = %d body=%s", rec.Code, rec.Body.String())
	}

	// Update role permissions.
	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj+"/roles/"+role.ID, alice, map[string]any{
		"name":        "Auditor",
		"description": "read only now",
		"permissions": []string{store.PermMemoryRead},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update role = %d body=%s", rec.Code, rec.Body.String())
	}

	// Delete role → bob falls back to VIEWER.
	rec = doJSON(t, s, http.MethodDelete, "/projects/"+proj+"/roles/"+role.ID, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete role = %d body=%s", rec.Code, rec.Body.String())
	}

	rs, _ := s.roleStore()
	got, err := rs.GetMemberRole(t.Context(), "bob", proj)
	if err != nil || got != store.RoleViewer {
		t.Fatalf("bob role after delete = %q %v", got, err)
	}

	// Cannot delete builtin.
	rec = doJSON(t, s, http.MethodDelete, "/projects/"+proj+"/roles/builtin:OWNER", alice, nil)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusConflict {
		t.Fatalf("delete builtin = %d, want 404/409", rec.Code)
	}
}

func TestViewerCannotWriteOrConfirmMemory(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	proj := resolveTestProject(t, s, alice, "rbac-mem")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant = %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj+"/members/bob/role", alice, map[string]string{
		"role": store.RoleViewer,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set viewer = %d body=%s", rec.Code, rec.Body.String())
	}

	// Viewer can search (membership + read).
	rec = doJSON(t, s, http.MethodGet, "/memory/search?project_id="+proj+"&q=x", bob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer search = %d, want 200", rec.Code)
	}

	// Viewer cannot create.
	rec = doJSON(t, s, http.MethodPost, "/memory", bob, map[string]any{
		"project_id": proj,
		"key":        "k1",
		"content":    "viewer should not write this content here",
		"level":      "project",
		"scope":      "fact",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create = %d, want 403", rec.Code)
	}

	// Owner creates; viewer cannot confirm.
	rec = doJSON(t, s, http.MethodPost, "/memory", alice, map[string]any{
		"project_id": proj,
		"key":        "k1",
		"content":    "owner proposed memory content for confirm test",
		"level":      "project",
		"scope":      "fact",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner create = %d body=%s", rec.Code, rec.Body.String())
	}
	var item store.MemoryItem
	decodeBody(t, rec, &item)

	rec = doJSON(t, s, http.MethodPost, "/memory/"+item.ID+"/confirm", bob, map[string]any{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer confirm = %d, want 403", rec.Code)
	}

	// Editor can confirm.
	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj+"/members/bob/role", alice, map[string]string{
		"role": store.RoleEditor,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set editor = %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/memory/"+item.ID+"/confirm", bob, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("editor confirm = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEditorCannotGrantMembers(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	carol := loginAs(t, s, "carol")
	proj := resolveTestProject(t, s, alice, "rbac-invite")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", alice, map[string]string{
		"user_id": "bob",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant bob = %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj+"/members", bob, map[string]string{
		"user_id": "carol",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor invite = %d, want 403", rec.Code)
	}
	_ = carol
}
