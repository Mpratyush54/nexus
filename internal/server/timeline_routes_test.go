package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestAgentSessionNativeIDTimelineAndComplete(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "timeline-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "editor", "owner"); err != nil {
		t.Fatal(err)
	}
	org, err := mem.CreateOrganization(ctx, "Acme", "acme-tl", "founder")
	if err != nil {
		t.Fatal(err)
	}
	orgProj, err := mem.CreateOrgProject(ctx, org.ID, "org-tl", "Org", "", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, "org-admin", store.OrgRoleAdmin, "founder"); err != nil {
		t.Fatal(err)
	}

	ownerTok := loginAs(t, s, "owner")
	editorTok := loginAs(t, s, "editor")
	adminTok := loginAs(t, s, "org-admin")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", ownerTok, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_01HZ",
		"origin_machine_id": "machine-a", "title": "Auth refactor",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)
	if created.NativeID != "ses_01HZ" || created.ID == "" || created.OwnerUserID != "owner" {
		t.Fatalf("session: %+v", created)
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID, editorTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor get: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/turns", ownerTok, map[string]any{
		"idx": 0, "role": "assistant", "text_preview": "edited auth",
		"tool_calls": []map[string]any{{"name": "edit", "path": "auth.go"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("turn: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/turns", ownerTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "auth.go") {
		t.Fatalf("turns: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions", ownerTok, map[string]any{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("version: %d %s", rec.Code, rec.Body.String())
	}
	var ver store.SessionVersion
	decodeBody(t, rec, &ver)

	transcript := []byte(`{"turn":1}`)
	sum := shaHex(transcript)
	manifest := map[string]any{
		"harness": "claude", "native_id": "ses_01HZ",
		"transcript": map[string]any{"blob": "sha256:" + sum},
		"files":      []map[string]any{{"path": "auth.go", "blob": "sha256:deadbeef"}},
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions/1/complete", ownerTok, manifest)
	if rec.Code != http.StatusConflict {
		t.Fatalf("incomplete complete: %d %s", rec.Code, rec.Body.String())
	}
	var missingBody struct {
		Missing []string `json:"missing"`
	}
	decodeBody(t, rec, &missingBody)
	if len(missingBody.Missing) == 0 {
		t.Fatalf("missing list empty: %s", rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+sum, ownerTok, map[string]any{
		"project_id": proj.ID, "purpose": "transcript", "body_b64": base64.StdEncoding.EncodeToString(transcript),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("put transcript: %d %s", rec.Code, rec.Body.String())
	}
	file := []byte("package auth")
	fileSum := shaHex(file)
	manifest["files"] = []map[string]any{{"path": "auth.go", "blob": fileSum}}
	rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+fileSum, ownerTok, map[string]any{
		"project_id": proj.ID, "purpose": "file", "body_b64": base64.StdEncoding.EncodeToString(file),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("put file: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions/1/complete", ownerTok, manifest)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &ver)
	if ver.State != "complete" {
		t.Fatalf("state %s", ver.State)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/versions/1/complete", ownerTok, manifest)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second complete: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/timeline", ownerTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ses_01HZ") {
		t.Fatalf("timeline: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/timeline", editorTok, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "ses_01HZ") {
		t.Fatalf("editor timeline leaked: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/grants", ownerTok, map[string]any{"user_id": "editor"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID, editorTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee get: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions", ownerTok, map[string]any{
		"project_id": orgProj.ID, "harness": "codex", "native_id": "rollout-private",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("org session: %d %s", rec.Code, rec.Body.String())
	}
	var orgSess store.AgentSession
	decodeBody(t, rec, &orgSess)
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+orgSess.ID, adminTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("org admin private: %d %s", rec.Code, rec.Body.String())
	}

	if err := mem.SetStorageUsage(ctx, proj.ID, 10, 10); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPut, "/v1/blobs/"+shaHex([]byte("more")), ownerTok, map[string]any{
		"project_id": proj.ID, "purpose": "file", "body_b64": base64.StdEncoding.EncodeToString([]byte("more")),
	})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("file at cap: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTimelineShowsOwnerLegacySnapshotsWhenNoAgentSessionsExist(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "legacy-timeline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: "legacy-session", ProjectID: project.ID, OwnerUserID: "owner",
		Harness: "codex", ConversationID: "thread-1", TurnCount: 23,
	}); err != nil {
		t.Fatal(err)
	}
	ownerTok := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodGet, "/v1/timeline", ownerTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "legacy-session") || !strings.Contains(rec.Body.String(), "legacy_snapshot") {
		t.Fatalf("legacy timeline: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/legacy-session", ownerTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "legacy_snapshot") {
		t.Fatalf("legacy read model: %d %s", rec.Code, rec.Body.String())
	}
}
