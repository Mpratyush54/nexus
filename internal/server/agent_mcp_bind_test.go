package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestAgentMCPRejectsMismatchedAgentHeader(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "bound-user")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "bound-mcp-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)
	ensureMembership(t, s, token, project.ID)

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Subject", "bound-user")
	req.Header.Set("X-Auth-Agent-ID", "cursor")
	req.Header.Set("X-Nexus-Project", project.ID)
	req.Header.Set("X-Nexus-Agent", "opencode")
	rec2 := httptest.NewRecorder()
	s.handleAgentMCP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "does not match") {
		t.Fatalf("body=%s", rec2.Body.String())
	}
}

func TestAgentMCPSeparateRateLimits(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "rate-user")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "rate-mcp-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)
	ensureMembership(t, s, token, project.ID)

	for _, agent := range []string{"cursor", "opencode"} {
		rec = doJSON(t, s, http.MethodPut, "/projects/"+project.ID+"/agents/"+agent, token, map[string]any{
			"mode":       "full",
			"rate_limit": 1,
			"tools": map[string]bool{
				"memory_search": true,
				"memory_write":  true,
			},
		})
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("upsert %s: %d %s", agent, rec.Code, rec.Body.String())
		}
	}

	callSearch := func(agent string) *httptest.ResponseRecorder {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "memory_search",
				"arguments": map[string]any{
					"project_id": project.ID,
					"query":      "rate",
				},
			},
		}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/mcp", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Auth-Subject", "rate-user")
		req.Header.Set("X-Auth-Agent-ID", agent)
		req.Header.Set("X-Nexus-Project", project.ID)
		rec := httptest.NewRecorder()
		s.handleAgentMCP(rec, req)
		return rec
	}

	if code := callSearch("cursor").Code; code != http.StatusOK {
		t.Fatalf("cursor first call: %d", code)
	}
	rec2 := callSearch("cursor")
	if rec2.Code != http.StatusOK {
		t.Fatalf("cursor second http=%d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "rate limit") {
		t.Fatalf("expected rate limit in body: %s", rec2.Body.String())
	}
	if code := callSearch("opencode").Code; code != http.StatusOK {
		t.Fatalf("opencode first call: %d", code)
	}
}

func TestUpsertSessionSummaryShape(t *testing.T) {
	s := newTestServer()
	ms := s.Store.(*store.MemStore)
	ctx := t.Context()
	p, err := ms.ResolveProject(ctx, "", "", "session-compress-proj")
	if err != nil {
		t.Fatal(err)
	}
	job := &store.HarvestJob{ProjectID: p.ID, Source: "test"}
	summary := "Goal: bind MCP to agents. Actions: edited agent_v1.go and AgentsPage.tsx. Outcomes: cursor and opencode have separate tokens."
	item, err := s.upsertSessionSummary(ctx, job, "abc-session", summary)
	if err != nil {
		t.Fatal(err)
	}
	if item.Key != "session/abc-session" || item.Scope != "episode_summary" || item.Level != "project" {
		t.Fatalf("unexpected item: %+v", item)
	}
	again, err := s.upsertSessionSummary(ctx, job, "abc-session", summary+" Updated with tests.")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != item.ID {
		t.Fatalf("expected upsert same id, got %s vs %s", again.ID, item.ID)
	}
	got, err := ms.GetMemoryItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Content, "Updated with tests") {
		t.Fatalf("content not updated: %s", got.Content)
	}
}
