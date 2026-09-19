package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"central-memory/internal/store"
)

func TestAgentV1DiscoveryAndMemory(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")

	rec := doJSON(t, s, http.MethodGet, "/v1/agent", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discovery status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var disc map[string]any
	decodeBody(t, rec, &disc)
	if disc["name"] != "nexus-agent-api" {
		t.Fatalf("unexpected discovery name: %v", disc["name"])
	}

	rec = doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "agent-api-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)
	ensureMembership(t, s, token, project.ID)

	content := "Prefer HTTPS /v1/agent when Nexus MCP is unavailable; never hunt for mem.exe."
	rec = doJSON(t, s, http.MethodPost, "/v1/agent/memory/write", token, map[string]any{
		"project_id": project.ID,
		"key":        "agents/remote-api",
		"content":    content,
		"level":      "project",
		"scope":      "decision",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("agent write status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodPost, "/v1/agent/memory/search", token, map[string]any{
		"project_id": project.ID,
		"query":      "mem.exe",
		"limit":      10,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("agent search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var search struct {
		Count int              `json:"count"`
		Items []map[string]any `json:"items"`
	}
	decodeBody(t, rec, &search)
	if search.Count < 1 {
		t.Fatalf("expected at least one hit, got %#v", search)
	}
}

func TestAgentV1RequiresProjectWhenAmbiguous(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "bob")

	for _, folder := range []string{"agent-a", "agent-b"} {
		rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
			"folder_name": folder,
		})
		var project store.Project
		decodeBody(t, rec, &project)
		ensureMembership(t, s, token, project.ID)
	}

	rec := doJSON(t, s, http.MethodPost, "/v1/agent/memory/search", token, map[string]any{
		"query": "anything",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without project_id, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentV1MCPToolsListAndCall(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "carol")

	rec := doJSON(t, s, http.MethodPost, "/projects/resolve", token, map[string]string{
		"folder_name": "agent-mcp-proj",
	})
	var project store.Project
	decodeBody(t, rec, &project)
	ensureMembership(t, s, token, project.ID)

	listReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	rec = doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", token, map[string]string{
		"X-Nexus-Project": project.ID,
	}, listReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body = %s", rec.Code, rec.Body.String())
	}

	writeReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "memory_write",
			"arguments": map[string]any{
				"project_id": project.ID,
				"key":        "mcp/remote",
				"content":    "Remote MCP JSON-RPC over HTTPS works for cloud agents without local binaries.",
			},
		},
	}
	rec = doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", token, map[string]string{
		"X-Nexus-Project": project.ID,
	}, writeReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var rpc struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result any `json:"result"`
	}
	decodeBody(t, rec, &rpc)
	if rpc.Error != nil {
		t.Fatalf("tools/call error: %s", rpc.Error.Message)
	}
}

func doJSONWithHeaders(t *testing.T, s *Server, method, target, token string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}
