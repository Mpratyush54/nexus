package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"net/http"
	"testing"

	"central-memory/internal/store"
)

// TestSessionContentIsOwnerOrGrantee is the P0 task 1 regression
// (product spec 4.5 / roadmap acceptance a): project membership, org ADMIN,
// and project session:read do not reveal someone else's snapshot.
func TestSessionContentIsOwnerOrGrantee(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()

	org, err := mem.CreateOrganization(ctx, "Acme", "acme-acl", "org-founder")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := mem.CreateOrgProject(ctx, org.ID, "acl-proj", "ACL", "", "", "session-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "org-admin", store.OrgRoleAdmin, "org-founder"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "editor", "session-owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "viewer", "session-owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.SetMemberRole(ctx, proj.ID, "viewer", store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	canRead, err := mem.HasPermission(ctx, "viewer", proj.ID, store.PermSessionRead)
	if err != nil || !canRead {
		t.Fatalf("viewer session:read = %v err=%v", canRead, err)
	}

	ownerTok := loginAs(t, s, "session-owner")
	editorTok := loginAs(t, s, "editor")
	viewerTok := loginAs(t, s, "viewer")
	adminTok := loginAs(t, s, "org-admin")

	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	gz := mustGzipACL(t, []byte(`{"turn":1}`))
	body := map[string]any{
		"project_id":             proj.ID,
		"harness":                "antigravity",
		"conversation_id":        "c1",
		"turn_count":             1,
		"transcript_payload_b64": base64.StdEncoding.EncodeToString(gz),
	}
	rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessionID+"/snapshot", ownerTok, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner snapshot: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+sessionID+"/operations", ownerTok, map[string]any{
		"project_id": proj.ID,
		"harness":    "antigravity",
		"file_operations": []map[string]any{{
			"tool_name": "view_file", "file_path": "secret.go", "op_type": "read",
		}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner ops: %d %s", rec.Code, rec.Body.String())
	}

	// Owner can list, get, download, and read provenance.
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/projects/" + proj.ID + "/snapshots", http.StatusOK},
		{http.MethodGet, "/sessions/" + sessionID + "/snapshot", http.StatusOK},
		{http.MethodGet, "/sessions/" + sessionID + "/snapshot/download", http.StatusOK},
		{http.MethodGet, "/sessions/" + sessionID + "/operations", http.StatusOK},
		{http.MethodGet, "/sessions/" + sessionID + "/files", http.StatusOK},
	} {
		rec = doJSON(t, s, c.method, c.path, ownerTok, nil)
		if rec.Code != c.want {
			t.Fatalf("owner %s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}

	// Editor, viewer (session:read), and org admin are 403 on every read.
	for _, tok := range []struct{ name, token string }{
		{"editor", editorTok},
		{"viewer", viewerTok},
		{"org-admin", adminTok},
	} {
		for _, path := range []string{
			"/projects/" + proj.ID + "/snapshots",
			"/sessions/" + sessionID + "/snapshot",
			"/sessions/" + sessionID + "/snapshot/download",
			"/sessions/" + sessionID + "/operations",
			"/sessions/" + sessionID + "/files",
		} {
			rec = doJSON(t, s, http.MethodGet, path, tok.token, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s GET %s: %d %s", tok.name, path, rec.Code, rec.Body.String())
			}
		}
	}

	// A person-specific grant lets that member through and nobody else.
	if err := mem.GrantSessionContent(ctx, sessionID, proj.ID, "editor", "session-owner"); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/sessions/"+sessionID+"/snapshot", editorTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee get: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj.ID+"/snapshots", editorTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee list: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/sessions/"+sessionID+"/snapshot", viewerTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer still private: %d %s", rec.Code, rec.Body.String())
	}

	// Ownerless rows are hidden from members and visible to project admins,
	// who can assign or delete them.
	orphanID := "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: orphanID, ProjectID: proj.ID, Harness: "antigravity",
		ConversationID: "orphan", TranscriptPayload: []byte("gz"),
	}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/sessions/"+orphanID+"/snapshot", editorTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor orphan get: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/projects/"+proj.ID+"/snapshots", ownerTok, nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(orphanID)) {
		t.Fatalf("admin list should include ownerless snapshot: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+orphanID+"/snapshot/owner", editorTok, map[string]any{"user_id": "editor"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor assign: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+orphanID+"/snapshot/owner", ownerTok, map[string]any{"user_id": "editor"})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin assign: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/sessions/"+orphanID+"/snapshot", editorTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("assignee get: %d %s", rec.Code, rec.Body.String())
	}
	// Project admin no longer sees content once an owner is set.
	rec = doJSON(t, s, http.MethodGet, "/sessions/"+orphanID+"/snapshot", ownerTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin after assign: %d %s", rec.Code, rec.Body.String())
	}

	orphan2 := "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee"
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: orphan2, ProjectID: proj.ID, Harness: "antigravity",
		ConversationID: "orphan2", TranscriptPayload: []byte("gz"),
	}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodDelete, "/sessions/"+orphan2+"/snapshot", editorTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodDelete, "/sessions/"+orphan2+"/snapshot", ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/sessions/"+orphan2+"/snapshot", ownerTok, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted get: %d %s", rec.Code, rec.Body.String())
	}

	// Member grant emits an audit row.
	rec = doJSON(t, s, http.MethodPost, "/projects/"+proj.ID+"/members", ownerTok, map[string]any{"user_id": "new-member"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant member: %d %s", rec.Code, rec.Body.String())
	}
	evs, err := mem.ListAuditEvents(ctx, proj.ID, "member.invited", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].ResourceID != "new-member" || evs[0].PrevHash == "" {
		t.Fatalf("audit = %+v", evs)
	}
	assignEvs, err := mem.ListAuditEvents(ctx, proj.ID, "session.owner_transferred", 10)
	if err != nil || len(assignEvs) != 1 {
		t.Fatalf("owner transfer audit = %+v err=%v", assignEvs, err)
	}
}

func mustGzipACL(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
