package server

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestShareDirectoryAndOrganizationGrant(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	org, err := mem.CreateOrganization(ctx, "Acme", "acme-share", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "bob", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "carol", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	project, err := mem.CreateOrgProject(ctx, org.ID, "sharing", "Sharing", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, project.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	alice := loginAs(t, s, "alice")
	carol := loginAs(t, s, "carol")
	outsider := loginAs(t, s, "outsider")
	path := "/v1/sharing/recipients?project_id=" + project.ID
	if rec := doJSON(t, s, http.MethodGet, path, outsider, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider directory = %d %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, s, http.MethodGet, path, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("directory = %d %s", rec.Code, rec.Body.String())
	}
	var directory struct {
		Organization struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"organization"`
		People []struct {
			UserID           string `json:"user_id"`
			Name             string `json:"name"`
			ProjectRole      string `json:"project_role"`
			OrganizationRole string `json:"organization_role"`
		} `json:"people"`
	}
	decodeBody(t, rec, &directory)
	if directory.Organization.ID != org.ID || len(directory.People) != 2 {
		t.Fatalf("directory = %+v", directory)
	}
	if directory.People[0].Name == "" || directory.People[1].Name == "" {
		t.Fatalf("missing recipient names: %+v", directory.People)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": project.ID, "harness": "codex", "native_id": "share-org-chat", "title": "Sharing",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session = %d %s", rec.Code, rec.Body.String())
	}
	var session store.AgentSession
	decodeBody(t, rec, &session)
	sharePath := "/v1/agent-sessions/" + session.ID + "/grants/organization"
	if rec := doJSON(t, s, http.MethodPost, sharePath, alice, map[string]any{"organization_id": org.ID}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed share = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, sharePath, alice, map[string]any{"organization_id": "different-org", "confirm": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-organization share = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, sharePath, outsider, map[string]any{"organization_id": org.ID, "confirm": true}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner share = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, sharePath, alice, map[string]any{"organization_id": org.ID, "confirm": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("org share = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+session.ID, carol, nil); rec.Code != http.StatusOK {
		t.Fatalf("org grantee read = %d %s", rec.Code, rec.Body.String())
	}
}
