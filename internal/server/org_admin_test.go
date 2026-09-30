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

func TestOrgSharesListAndAdminRevoke(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	org, err := mem.CreateOrganization(ctx, "SharesCo", "shares-co", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "carol", store.OrgRoleAdmin, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "bob", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "app", "App", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	alice := loginAs(t, s, "alice")
	carol := loginAs(t, s, "carol")
	bob := loginAs(t, s, "bob")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_share_tab",
		"origin_machine_id": "lap", "title": "SECRET-TITLE", "summary": "SECRET-SUMMARY",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)
	rec = doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+sess.ID+"/grants/bob", alice, map[string]any{
		"live": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/shares", bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member shares: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/shares", carol, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin shares: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "SECRET-TITLE") || strings.Contains(body, "SECRET-SUMMARY") {
		t.Fatalf("shares leaked content: %s", body)
	}
	var listed struct {
		Items []store.OrgSessionShare `json:"items"`
		Count int                     `json:"count"`
	}
	decodeBody(t, rec, &listed)
	if listed.Count != 1 || listed.Items[0].SessionID != sess.ID || listed.Items[0].GranteeID != "bob" {
		t.Fatalf("listed=%+v", listed)
	}
	if listed.Items[0].OwnerID != "alice" || listed.Items[0].ProjectID != proj.ID || !listed.Items[0].Live {
		t.Fatalf("row=%+v", listed.Items[0])
	}

	rec = doJSON(t, s, http.MethodDelete, "/orgs/"+org.ID+"/shares/"+sess.ID+"/bob", carol, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	ok, err := mem.CanReadAgentSession(ctx, "bob", sess.ID)
	if err != nil || ok {
		t.Fatalf("bob still reads after admin revoke: %v %v", ok, err)
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID+"/shares", carol, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("after revoke: %d %s", rec.Code, rec.Body.String())
	}
}
