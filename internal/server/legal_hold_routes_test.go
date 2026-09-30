package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestLegalHoldRequiresTwoDistinctOwners(t *testing.T) {
	s := newTestServer()
	s.registerLegalHoldRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	org, err := mem.CreateOrganization(ctx, "HoldCo", "hold-co", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "bob", store.OrgRoleOwner, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "carol", store.OrgRoleAdmin, "alice"); err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "app", "App", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}

	alice := loginAs(t, s, "alice")
	bob := loginAs(t, s, "bob")
	carol := loginAs(t, s, "carol")

	const title = "SECRET-TITLE"
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_hold",
		"origin_machine_id": "lap", "title": title, "summary": "SECRET-SUMMARY",
		"visibility": "private",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/export", carol, map[string]any{
		"session_ids": []string{sess.ID},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin create: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/export", alice, map[string]any{
		"session_ids":   []string{sess.ID},
		"custodian_ids": []string{"alice"},
		"reason":        "compliance",
		"approver_ids":  []string{"bob"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	createBody := rec.Body.String()
	if strings.Contains(createBody, title) || strings.Contains(createBody, "SECRET-SUMMARY") {
		t.Fatalf("create leaked content: %s", createBody)
	}
	var req store.LegalHoldRequest
	decodeBody(t, rec, &req)
	if req.Status != "pending" || req.ID == "" {
		t.Fatalf("request: %+v", req)
	}

	// Same owner cannot self-approve (single owner rejection path).
	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/"+req.ID+"/approve", alice, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self-approve: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/"+req.ID+"/approve", bob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	exportBody := rec.Body.String()
	if strings.Contains(exportBody, title) || strings.Contains(exportBody, "SECRET-SUMMARY") {
		t.Fatalf("export leaked titles: %s", exportBody)
	}
	var out struct {
		Request *store.LegalHoldRequest `json:"request"`
		Receipt *store.LegalHoldReceipt `json:"receipt"`
	}
	decodeBody(t, rec, &out)
	if out.Receipt == nil || out.Receipt.SessionCount != 1 {
		t.Fatalf("receipt: %+v", out.Receipt)
	}
	if out.Receipt.Sessions[0].SessionID != sess.ID {
		t.Fatalf("receipt session: %+v", out.Receipt.Sessions[0])
	}
	if out.Receipt.Sessions[0].ContentHash == "" {
		t.Fatalf("expected content hash: %+v", out.Receipt.Sessions[0])
	}

	evs, err := mem.ListOrgAudit(ctx, org.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"legal_hold.requested": false,
		"legal_hold.approved":  false,
		"legal_hold.exported":  false,
	}
	for _, ev := range evs {
		if _, ok := want[ev.Action]; ok {
			want[ev.Action] = true
			meta, _ := json.Marshal(ev.Metadata)
			if strings.Contains(string(meta), title) {
				t.Fatalf("audit leaked title: %s", meta)
			}
		}
	}
	for action, ok := range want {
		if !ok {
			t.Fatalf("missing audit %s: %+v", action, evs)
		}
	}
}

func TestLegalHoldSingleOwnerCannotComplete(t *testing.T) {
	s := newTestServer()
	s.registerLegalHoldRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	org, err := mem.CreateOrganization(ctx, "Solo", "solo-hold", "alice")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "app", "App", "", "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	alice := loginAs(t, s, "alice")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_solo",
		"title": "SECRET-TITLE", "visibility": "private",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var sess store.AgentSession
	decodeBody(t, rec, &sess)

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/export", alice, map[string]any{
		"session_ids":  []string{sess.ID},
		"approver_ids": []string{"alice"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self in approver_ids: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/export", alice, map[string]any{
		"session_ids": []string{sess.ID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var req store.LegalHoldRequest
	decodeBody(t, rec, &req)

	rec = doJSON(t, s, http.MethodPost, "/orgs/"+org.ID+"/legal-hold/"+req.ID+"/approve", alice, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("solo approve: %d %s", rec.Code, rec.Body.String())
	}
}
