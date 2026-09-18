package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestAgentPermissionRoutes(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "agent-perm-proj")

	rec := doJSON(t, s, http.MethodPut, "/projects/"+projectID+"/agents/cursor-agent", token, map[string]any{
		"mode":       "propose_only",
		"rate_limit": 12,
		"tools": map[string]bool{
			"memory_search": true,
			"file_write":    false,
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/agents", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list status = %d", rec.Code)
	}
	var list struct {
		Count int                      `json:"count"`
		Items []*store.AgentPermission `json:"items"`
	}
	decodeBody(t, rec, &list)
	if list.Count != 1 || list.Items[0].AgentID != "cursor-agent" {
		t.Fatalf("list = %+v", list)
	}
	if list.Items[0].Mode != store.AgentModeProposeOnly || list.Items[0].RateLimit != 12 {
		t.Fatalf("perm = %+v", list.Items[0])
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/agents/cursor-agent", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET one status = %d", rec.Code)
	}

	rec = doJSON(t, s, http.MethodDelete, "/projects/"+projectID+"/agents/cursor-agent", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/agents/cursor-agent", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d", rec.Code)
	}
}

func TestMCPToolCallEventAndFanout(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mcp-call-proj")

	hub := NewHub()
	s.AttachHub(hub)
	viewer := newHubClient(hub, "alice", "", "")
	mustSubscribe(t, hub, viewer, projectID, "")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+projectID+"/mcp/tool-calls", token, map[string]any{
		"agent_id":    "cursor-agent",
		"tool_name":   "memory_search",
		"arguments":   map[string]any{"query": "redis"},
		"result":      map[string]any{"items_included": 3},
		"duration_ms": 12,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	evs, err := s.Store.ListEvents(t.Context(), projectID, 0, 10)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events: %v len=%d", err, len(evs))
	}
	found := false
	for _, ev := range evs {
		if ev.EventType == EventMCPToolCall {
			found = true
			if aid, _ := ev.Payload["agent_id"].(string); aid != "cursor-agent" {
				t.Fatalf("payload agent_id = %v", ev.Payload["agent_id"])
			}
		}
	}
	if !found {
		t.Fatal("expected MCP_TOOL_CALL event")
	}

	msg := readMsg(t, viewer)
	if msg.Type != WSMsgEvent || msg.EventType != EventMCPToolCall {
		t.Fatalf("hub msg = %+v", msg)
	}
}

func TestMCPToolCallRespectsBlockedAgent(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mcp-block-proj")

	ps := s.Store.(store.AgentPermissionStore)
	if err := ps.SetAgentPermission(t.Context(), &store.AgentPermission{
		ProjectID: projectID, AgentID: "bad-agent", Mode: store.AgentModeBlocked, RateLimit: 60,
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/projects/"+projectID+"/mcp/tool-calls", token, map[string]any{
		"agent_id":  "bad-agent",
		"tool_name": "memory_search",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d want 403 body = %s", rec.Code, rec.Body.String())
	}
}

func TestAgentMinuteLimiter(t *testing.T) {
	l := newAgentMinuteLimiter()
	if !l.allow("a", 2) || !l.allow("a", 2) {
		t.Fatal("first two should pass")
	}
	if l.allow("a", 2) {
		t.Fatal("third should fail")
	}
	// Different key is independent.
	if !l.allow("b", 1) {
		t.Fatal("other key should pass")
	}
	_ = time.Now() // keep time import warm if tests evolve
}

func TestLogMCPToolCallHelper(t *testing.T) {
	st := store.NewMemStore()
	p, err := st.ResolveProject(t.Context(), "https://github.com/x/y.git", "sha", "y")
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	c := newHubClient(hub, "u", "", "")
	mustSubscribe(t, hub, c, p.ID, "")
	ev, err := LogMCPToolCall(t.Context(), st, hub, p.ID, "agent-1", "workspace_info", nil, map[string]any{"ok": true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.EventType != EventMCPToolCall {
		t.Fatalf("type %q", ev.EventType)
	}
	raw := readMsg(t, c)
	b, _ := json.Marshal(raw)
	if !bytesContains(b, []byte(EventMCPToolCall)) {
		t.Fatalf("payload missing type: %s", b)
	}
}

func TestMCPToolCallList(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mcp-list-proj")

	rec := doJSON(t, s, http.MethodPost, "/projects/"+projectID+"/mcp/tool-calls", token, map[string]any{
		"agent_id":  "cursor-agent",
		"tool_name": "memory_write",
		"arguments": map[string]any{"key": "k"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/mcp/tool-calls", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count int `json:"count"`
		Items []struct {
			EventType string `json:"event_type"`
			AgentID   string `json:"agent_id"`
			ToolName  string `json:"tool_name"`
		} `json:"items"`
	}
	decodeBody(t, rec, &out)
	if out.Count != 1 || out.Items[0].EventType != EventMCPToolCall || out.Items[0].ToolName != "memory_write" {
		t.Fatalf("list = %+v", out)
	}
}

func bytesContains(haystack, needle []byte) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) &&
		(string(haystack) == string(needle) || len(haystack) > 0 && containsBytes(haystack, needle)))
}

func containsBytes(h, n []byte) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			if h[i+j] != n[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
