package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestOrgCreateListGet(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")

	rec := doJSON(t, s, http.MethodPost, "/orgs", alice, map[string]string{
		"name": "Acme Labs",
		"slug": "acme",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /orgs: %d %s", rec.Code, rec.Body.String())
	}
	var created store.Organization
	decodeBody(t, rec, &created)
	if created.ID == "" || created.Name != "Acme Labs" || created.Slug != "acme" {
		t.Fatalf("unexpected org: %+v", created)
	}
	if created.CreatedBy != "alice" {
		t.Fatalf("created_by = %q, want alice", created.CreatedBy)
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /orgs: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []*store.Organization `json:"items"`
		Count int                   `json:"count"`
	}
	decodeBody(t, rec, &list)
	if list.Count != 1 || len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	bob := loginAs(t, s, "bob")
	rec = doJSON(t, s, http.MethodGet, "/orgs", bob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /orgs bob: %d", rec.Code)
	}
	decodeBody(t, rec, &list)
	if list.Count != 0 {
		t.Fatalf("bob should see 0 orgs, got %d", list.Count)
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+created.ID, bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /orgs/{id} as non-member: %d, want 403", rec.Code)
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+created.ID, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /orgs/{id}: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Organization *store.Organization         `json:"organization"`
		Members      []*store.OrganizationMember `json:"members"`
		Projects     []*store.Project            `json:"projects"`
	}
	decodeBody(t, rec, &detail)
	if detail.Organization == nil || detail.Organization.ID != created.ID {
		t.Fatalf("detail org: %+v", detail.Organization)
	}
	if len(detail.Members) != 1 || detail.Members[0].Role != store.OrgRoleAdmin {
		t.Fatalf("members = %+v", detail.Members)
	}
	if len(detail.Projects) != 0 {
		t.Fatalf("projects = %+v, want empty", detail.Projects)
	}
}

func TestOrgMembersAndProjects(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	_ = loginAs(t, s, "carol")

	rec := doJSON(t, s, http.MethodPost, "/orgs", alice, map[string]string{"name": "Team"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create org: %d %s", rec.Code, rec.Body.String())
	}
	var org store.Organization
	decodeBody(t, rec, &org)

	// Non-admin cannot add members.
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/members", bob, map[string]string{
		"user_id": "carol",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bob add member: %d, want 403", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/members", alice, map[string]string{
		"user_id": "bob",
		"role":    "MEMBER",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add bob: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/members", alice, map[string]string{
		"user_id": "carol",
		"role":    "ADMIN",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add carol admin: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/orgs/"+org.ID+"/members/bob/role", alice, map[string]string{
		"role": "ADMIN",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("promote bob: %d %s", rec.Code, rec.Body.String())
	}

	// Member (not admin) cannot create projects — demote bob first for the
	// negative case, then create as alice.
	rec = doJSON(t, s, http.MethodPut, "/orgs/"+org.ID+"/members/bob/role", alice, map[string]string{
		"role": "MEMBER",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("demote bob: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/projects", bob, map[string]string{
		"folder_name": "denied-proj",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member create project: %d, want 403", rec.Code)
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/projects", alice, map[string]any{
		"folder_name":   "nexus",
		"display_name":  "Nexus",
		"canonical_url": "https://github.com/acme/nexus.git",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	var proj store.Project
	decodeBody(t, rec, &proj)
	if proj.OrgID != org.ID || proj.FolderName != "nexus" || proj.CreatedBy != "alice" {
		t.Fatalf("project = %+v", proj)
	}

	// Org admin carol gets project access without an explicit project grant.
	ok, err := s.Store.IsProjectMember(t.Context(), "carol", proj.ID)
	if err != nil {
		t.Fatalf("IsProjectMember carol: %v", err)
	}
	if !ok {
		t.Fatal("org admin carol should access org project")
	}

	// Org member bob does NOT automatically get project access.
	ok, err = s.Store.IsProjectMember(t.Context(), "bob", proj.ID)
	if err != nil {
		t.Fatalf("IsProjectMember bob: %v", err)
	}
	if ok {
		t.Fatal("org member bob must not auto-access org projects")
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get org: %d %s", rec.Code, rec.Body.String())
	}
	var detail map[string]json.RawMessage
	decodeBody(t, rec, &detail)
	var projects []*store.Project
	if err := json.Unmarshal(detail["projects"], &projects); err != nil {
		t.Fatalf("unmarshal projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != proj.ID {
		t.Fatalf("projects = %+v", projects)
	}

	// Cannot remove last remaining path that leaves zero admins if we remove
	// all but one — remove carol (extra admin) then try to remove alice.
	rec = doJSON(t, s, http.MethodDelete, "/orgs/"+org.ID+"/members/carol", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove carol: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodDelete, "/orgs/"+org.ID+"/members/alice", alice, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("remove last admin: %d, want 409", rec.Code)
	}
}

func TestOrgCreateRequiresAuth(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s, http.MethodPost, "/orgs", "", map[string]string{"name": "X"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /orgs: %d, want 401", rec.Code)
	}
}
