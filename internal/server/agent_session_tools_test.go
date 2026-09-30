package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestSessionFetch(t *testing.T) {
	s := newTestServer()
	s.registerAgentSessionToolRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "fetch-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "editor", "owner"); err != nil {
		t.Fatal(err)
	}
	org, err := mem.CreateOrganization(ctx, "Acme", "acme-fetch", "founder")
	if err != nil {
		t.Fatal(err)
	}
	orgProj, err := mem.CreateOrgProject(ctx, org.ID, "org-fetch", "Org", "", "", "owner")
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
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_fetch",
		"summary": "Owner summary for the fetch session.",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/fetch?limit=1", editorTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor fetch: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/summary", editorTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor summary: %d %s", rec.Code, rec.Body.String())
	}

	for _, turn := range []map[string]any{
		{"idx": 0, "role": "user", "text_preview": "first turn"},
		{"idx": 1, "role": "assistant", "text_preview": "second turn"},
	} {
		rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/turns", ownerTok, turn)
		if rec.Code != http.StatusCreated {
			t.Fatalf("turn: %d %s", rec.Code, rec.Body.String())
		}
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/fetch?limit=1", ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner fetch: %d %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []store.SessionTurn `json:"items"`
		Next  string              `json:"next_cursor"`
	}
	decodeBody(t, rec, &page)
	if len(page.Items) != 1 || page.Items[0].Idx != 0 || page.Items[0].TextPreview != "first turn" {
		t.Fatalf("first page: %+v", page)
	}
	if page.Next == "" {
		t.Fatal("expected next cursor")
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/fetch?limit=1&cursor="+page.Next, ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second fetch: %d %s", rec.Code, rec.Body.String())
	}
	var rest struct {
		Items []store.SessionTurn `json:"items"`
		Next  string              `json:"next_cursor"`
	}
	decodeBody(t, rec, &rest)
	if len(rest.Items) != 1 || rest.Items[0].Idx != 1 || rest.Items[0].TextPreview != "second turn" {
		t.Fatalf("second page: %+v", rest)
	}
	if rest.Next != "" {
		t.Fatalf("last page cursor: %q", rest.Next)
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions", ownerTok, map[string]any{
		"project_id": orgProj.ID, "harness": "codex", "native_id": "org-private",
		"summary": "Private org session summary.",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("org session: %d %s", rec.Code, rec.Body.String())
	}
	var orgSess store.AgentSession
	decodeBody(t, rec, &orgSess)
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+orgSess.ID+"/fetch", adminTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("org admin fetch: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+orgSess.ID+"/summary", adminTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("org admin summary: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionSummary(t *testing.T) {
	s := newTestServer()
	s.registerAgentSessionToolRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "summary-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "editor", "owner"); err != nil {
		t.Fatal(err)
	}
	ownerTok := loginAs(t, s, "owner")
	const summary = "We pinned the pool after tracing the leak."
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", ownerTok, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_summary",
		"summary": summary,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/summary", ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Summary string `json:"summary"`
	}
	decodeBody(t, rec, &body)
	if body.Summary != summary {
		t.Fatalf("summary %q", body.Summary)
	}

	editorTok := loginAs(t, s, "editor")
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/grants", ownerTok, map[string]any{"user_id": "editor"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/summary", editorTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee summary: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &body)
	if body.Summary != summary {
		t.Fatalf("grantee summary %q", body.Summary)
	}
}

func TestProjectKnowledge(t *testing.T) {
	s := newTestServer()
	s.registerAgentSessionToolRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "knowledge-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	const content = "Project knowledge: pin pgx at v5 after the pool leak."
	if err := mem.CreateMemoryItem(ctx, &store.MemoryItem{
		ProjectID: proj.ID,
		Key:       "deps/pgx",
		Content:   content,
		Level:     store.LevelProject,
		Status:    store.StatusProposed,
		Scope:     "fact",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.CreateMemoryItem(ctx, &store.MemoryItem{
		ProjectID: proj.ID,
		Key:       "prefs/editor",
		Content:   "Only the owner should see this personal preference note.",
		Level:     store.LevelPersonal,
		UserID:    "owner",
		Status:    store.StatusProposed,
		Scope:     "preference",
	}); err != nil {
		t.Fatal(err)
	}
	if publicMemoryStatus(store.StatusConfirmed) != "active" ||
		publicMemoryStatus(store.StatusRejected) != "forgotten" ||
		publicMemoryStatus(store.StatusSuperseded) != "superseded" {
		t.Fatal("public status map mismatch")
	}

	ownerTok := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodGet, "/v1/projects/"+proj.ID+"/knowledge?limit=10", ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("knowledge: %d %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "PROPOSED") || strings.Contains(raw, "CONFIRMED") {
		t.Fatalf("internal status leaked: %s", raw)
	}
	if strings.Contains(raw, "personal preference") {
		t.Fatalf("personal row leaked: %s", raw)
	}
	var body struct {
		Items []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
			Level   string `json:"level"`
		} `json:"items"`
	}
	decodeBody(t, rec, &body)
	if len(body.Items) != 1 || body.Items[0].Content != content || body.Items[0].Status != "active" || body.Items[0].Level != store.LevelProject {
		t.Fatalf("items: %+v", body.Items)
	}
}
