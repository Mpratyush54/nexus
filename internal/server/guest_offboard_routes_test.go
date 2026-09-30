package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestGuestLinkOwnerAcceptAndRead(t *testing.T) {
	s := newTestServer()
	s.registerGuestOffboardRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	proj, err := mem.ResolveProject(ctx, "", "", "guest-http")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "editor", "owner"); err != nil {
		t.Fatal(err)
	}
	org, err := mem.CreateOrganization(ctx, "Acme", "acme-guest", "founder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "org-admin", store.OrgRoleAdmin, "founder"); err != nil {
		t.Fatal(err)
	}

	ownerTok := loginAs(t, s, "owner")
	editorTok := loginAs(t, s, "editor")
	guestTok := loginAs(t, s, "guest")
	adminTok := loginAs(t, s, "org-admin")

	const title = "SECRET-TITLE"
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", ownerTok, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_guest_http",
		"title": title, "summary": "SECRET-SUMMARY", "visibility": "private",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/guest-links", "", map[string]any{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon create: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/guest-links", editorTok, map[string]any{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner create: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/guest-links", ownerTok, map[string]any{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create link: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, title) || strings.Contains(body, "SECRET-SUMMARY") {
		t.Fatalf("create link leaked content: %s", body)
	}
	var link store.GuestLink
	decodeBody(t, rec, &link)
	if link.Token == "" || !link.SingleUse || link.SessionID != sess.ID {
		t.Fatalf("link: %+v", link)
	}
	until := time.Until(link.ExpiresAt)
	if until < 6*24*time.Hour || until > 8*24*time.Hour {
		t.Fatalf("default expiry %s", link.ExpiresAt)
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/guest-links/accept", "", map[string]any{"token": link.Token})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon accept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/guest-links/accept", guestTok, map[string]any{"token": link.Token})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	acceptBody := rec.Body.String()
	if strings.Contains(acceptBody, title) || strings.Contains(acceptBody, "SECRET-SUMMARY") || strings.Contains(acceptBody, link.Token) {
		t.Fatalf("accept response leaked content or token: %s", acceptBody)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/guest-links/accept", guestTok, map[string]any{"token": link.Token})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second accept: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, guestTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), title) {
		t.Fatalf("grantee get: %d %s", rec.Code, rec.Body.String())
	}
	member, err := mem.IsProjectMember(ctx, "guest", proj.ID)
	if err != nil || member {
		t.Fatalf("guest became a project member: %v %v", member, err)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, adminTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("org admin read: %d %s", rec.Code, rec.Body.String())
	}

	expired, err := mem.CreateGuestLink(ctx, sess.ID, "owner", time.Now().Add(-time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/guest-links/accept", guestTok, map[string]any{"token": expired.Token})
	if rec.Code != http.StatusGone {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOffboardCountsOmitSessionContent(t *testing.T) {
	s := newTestServer()
	s.registerGuestOffboardRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	org, err := mem.CreateOrganization(ctx, "Acme", "acme-offboard", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "carol", store.OrgRoleAdmin, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "bob", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "leaver", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "receiver", store.OrgRoleMember, "alice"); err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "org-repo", "Org", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "leaver", "alice"); err != nil {
		t.Fatal(err)
	}
	personal, err := mem.ResolveProject(ctx, "", "", "personal-offboard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, personal.ID, "leaver"); err != nil {
		t.Fatal(err)
	}

	alice := loginAs(t, s, "alice")
	carol := loginAs(t, s, "carol")
	bob := loginAs(t, s, "bob")
	leaver := loginAs(t, s, "leaver")
	receiver := loginAs(t, s, "receiver")
	kept := loginAs(t, s, "kept-user")

	const title = "SECRET-TITLE"
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", leaver, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_offboard",
		"origin_machine_id": "lap-1", "title": title, "summary": "SECRET-SUMMARY",
		"visibility": "private",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("org session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions", leaver, map[string]any{
		"project_id": personal.ID, "harness": "claude", "native_id": "ses_personal",
		"title": title, "visibility": "private",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("personal session: %d %s", rec.Code, rec.Body.String())
	}
	var personalSess store.AgentSession
	decodeBody(t, rec, &personalSess)

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+sess.ID+"/grants", leaver, map[string]any{"user_id": "kept-user"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, carol, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin before offboard: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/offboard", bob, map[string]any{
		"from_user_id": "leaver", "to_user_id": "receiver",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member offboard: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/offboard", carol, map[string]any{
		"from_user_id": "leaver", "to_user_id": "receiver",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin offboard: %d %s", rec.Code, rec.Body.String())
	}
	offBody := rec.Body.String()
	if strings.Contains(offBody, title) || strings.Contains(offBody, "SECRET-SUMMARY") || strings.Contains(offBody, "ses_offboard") {
		t.Fatalf("offboard leaked content: %s", offBody)
	}
	var result store.OffboardResult
	decodeBody(t, rec, &result)
	if result.SessionsTransferred != 1 || result.GrantsKept != 1 {
		t.Fatalf("result = %+v body %s", result, offBody)
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, receiver, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), title) {
		t.Fatalf("receiver get: %d %s", rec.Code, rec.Body.String())
	}
	var owned store.AgentSession
	decodeBody(t, rec, &owned)
	if owned.OwnerUserID != "receiver" || owned.Visibility != "private" {
		t.Fatalf("transferred session: %+v", owned)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/timeline", receiver, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), sess.ID) {
		t.Fatalf("receiver timeline: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, leaver, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("leaver still reads: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, kept, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant not kept: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, carol, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin after offboard: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sess.ID, alice, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("owner role reads private session: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+personalSess.ID, leaver, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("personal session: %d %s", rec.Code, rec.Body.String())
	}
	gotPersonal, err := mem.GetAgentSession(ctx, personalSess.ID)
	if err != nil || gotPersonal.OwnerUserID != "leaver" {
		t.Fatalf("personal owner: %+v %v", gotPersonal, err)
	}

	evs, err := mem.ListOrgAudit(ctx, org.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Action != "member.offboarded" {
			continue
		}
		found = true
		meta, err := json.Marshal(ev.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(meta), title) || strings.Contains(string(meta), "SECRET-SUMMARY") {
			t.Fatalf("audit metadata leaked content: %s", meta)
		}
		if ev.ResourceID != "leaver" {
			t.Fatalf("audit resource: %+v", ev)
		}
	}
	if !found {
		t.Fatalf("missing member.offboarded audit: %+v", evs)
	}
}
