package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentAccessAllowTool(t *testing.T) {
	if err := (*AgentAccess)(nil).AllowTool("memory_write"); err != nil {
		t.Fatal(err)
	}
	blocked := &AgentAccess{AgentID: "a", Mode: ModeBlocked}
	if err := blocked.AllowTool("memory_search"); err == nil {
		t.Fatal("expected block")
	}
	ro := &AgentAccess{AgentID: "a", Mode: ModeReadOnly}
	if err := ro.AllowTool("memory_write"); err == nil {
		t.Fatal("read_only should deny write")
	}
	if err := ro.AllowTool("memory_search"); err != nil {
		t.Fatal(err)
	}
}

func TestCallToolPermissionAndLog(t *testing.T) {
	var logged []ToolCallLog
	logger := toolLogFn(func(_ context.Context, _ string, call ToolCallLog) error {
		logged = append(logged, call)
		return nil
	})
	s := NewServer(nil, Config{
		ProjectID: "p1",
		AgentName: "cursor-agent",
		Access: &AgentAccess{
			AgentID: "cursor-agent",
			Mode:    ModeBlocked,
		},
		ToolLogger: logger,
	})
	_, err := s.CallTool(context.Background(), "workspace_info", nil)
	if err == nil {
		t.Fatal("expected permission error")
	}
	if len(logged) != 1 || logged[0].ToolName != "workspace_info" || logged[0].Error == "" {
		t.Fatalf("log = %+v", logged)
	}
}

func TestCallToolRateLimit(t *testing.T) {
	s := NewServer(nil, Config{
		ProjectID:   "p1",
		AgentName:   "agent",
		RateLimiter: NewRateLimiter(),
		Access:      &AgentAccess{AgentID: "agent", Mode: ModeFull, RateLimit: 1},
	})
	if _, err := s.CallTool(context.Background(), "workspace_info", nil); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.CallTool(context.Background(), "workspace_info", nil); err == nil {
		t.Fatal("second should rate-limit")
	}
}

func TestHTTPStoreLogToolCall(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"event_type":"MCP_TOOL_CALL"}`))
	}))
	defer srv.Close()

	h := NewHTTPStore(srv.URL, "tok")
	err := h.LogToolCall(context.Background(), "proj-1", ToolCallLog{
		AgentID: "cursor-agent", ToolName: "memory_search",
		Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/projects/proj-1/mcp/tool-calls") {
		t.Fatalf("path %q", gotPath)
	}
	if gotBody["tool_name"] != "memory_search" {
		t.Fatalf("body %+v", gotBody)
	}
}

type toolLogFn func(context.Context, string, ToolCallLog) error

func (f toolLogFn) LogToolCall(ctx context.Context, projectID string, call ToolCallLog) error {
	return f(ctx, projectID, call)
}
