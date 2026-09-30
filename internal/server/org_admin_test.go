package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestOrgOwnerBillingCaptureAuditAndStorage(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	org, err := mem.CreateOrganization(ctx, "Acme", "acme-admin", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "carol", store.OrgRoleAdmin, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "bob", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "nexus", "Nexus", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}

	alice := loginAs(t, s, "alice")
	carol := loginAs(t, s, "carol")
	bob := loginAs(t, s, "bob")
	editor := loginAs(t, s, "editor")

	rec := doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/billing", carol, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin billing get: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/billing", carol, map[string]any{"plan_id": "free"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin billing set: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/billing", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner billing get: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/orgs/"+org.ID+"/members/bob/role", carol, map[string]string{"role": "OWNER"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin grant owner: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/orgs/"+org.ID+"/members/alice/role", carol, map[string]string{"role": "ADMIN"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin demote owner: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/orgs/"+org.ID+"/members/bob/role", alice, map[string]string{"role": "OWNER"})
	if rec.Code != http.StatusOK {
		t.Fatalf("owner grant owner: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/orgs/"+org.ID+"/members/bob/role", alice, map[string]string{"role": "MEMBER"})
	if rec.Code != http.StatusOK {
		t.Fatalf("owner demote extra owner: %d %s", rec.Code, rec.Body.String())
	}

	const secretTitle = "PRIVATE-TITLE-XYZ"
	const secretNative = "ses_secret_native"
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": secretNative,
		"origin_machine_id": "lap-1", "title": secretTitle, "summary": "do not show admins",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/storage", carol, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("storage: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, secretTitle) || strings.Contains(body, secretNative) || strings.Contains(body, "do not show admins") {
		t.Fatalf("admin storage leaked session content: %s", body)
	}
	if !strings.Contains(body, `"session_count":1`) {
		t.Fatalf("storage missing session count: %s", body)
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/storage", bob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("member storage: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretTitle) || strings.Contains(rec.Body.String(), proj.FolderName) {
		t.Fatalf("member storage should be own usage only: %s", rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj.ID+"/capture", editor, map[string]any{"enabled": false})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stranger capture: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj.ID+"/capture", carol, map[string]any{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin capture off: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_blocked",
		"origin_machine_id": "lap-1",
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "capture is off") {
		t.Fatalf("upload while off: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPut, "/projects/"+proj.ID+"/capture", alice, map[string]any{"enabled": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("owner capture on: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/audit", carol, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "project.capture_toggled") {
		t.Fatalf("admin audit: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/audit", bob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("member audit: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "project.capture_toggled") {
		t.Fatalf("member saw admin audit: %s", rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/audit?format=csv", carol, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "project.capture_toggled") {
		t.Fatalf("csv audit: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv content type: %s", rec.Header().Get("Content-Type"))
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/invites", carol, map[string]any{
		"email": "sam@acme.dev", "role": "OWNER",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin owner invite: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/invites", alice, map[string]any{
		"email": "sam@acme.dev", "role": "MEMBER",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	var inv store.OrgInvite
	decodeBody(t, rec, &inv)
	if inv.Token == "" || inv.Email != "sam@acme.dev" {
		t.Fatalf("invite: %+v", inv)
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/invites", carol, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), inv.Token) {
		t.Fatalf("invite list leaked token: %d %s", rec.Code, rec.Body.String())
	}
	sam := loginAs(t, s, "sam")
	rec = doJSON(t, s, http.MethodPost, "/orgs/invites/accept", sam, map[string]any{"token": inv.Token})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	role, err := mem.GetOrgMemberRole(ctx, org.ID, "sam")
	if err != nil || role != store.OrgRoleMember {
		t.Fatalf("sam role = %q %v", role, err)
	}
	rec = doJSON(t, s, http.MethodPost, "/orgs/invites/accept", sam, map[string]any{"token": inv.Token})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second accept: %d %s", rec.Code, rec.Body.String())
	}
}
