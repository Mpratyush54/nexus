package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

// mcpToolJSON calls one cloud MCP tool and decodes its structured result.
func mcpToolJSON(t *testing.T, s *Server, token, projectID, tool string, args map[string]any) map[string]any {
	t.Helper()
	headers := map[string]string{}
	if projectID != "" {
		headers["X-Nexus-Project"] = projectID
	}
	rec := doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", token, headers, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", tool, rec.Code, rec.Body.String())
	}
	var rpc struct {
		Error  any            `json:"error"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil {
		t.Fatalf("%s error: %v", tool, rpc.Error)
	}
	return rpc.Result
}

// TestMCPSessionSummaryFallsBackToTurns: a captured chat with turns but no
// stored summary used to return summary:"" over MCP.
func TestMCPSessionSummaryFallsBackToTurns(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "mcp-summary-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": proj.ID, "harness": "claude", "native_id": "ses_nosummary",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)
	turns := []map[string]any{
		{"idx": 0, "role": "user", "text_preview": "Please make the deploy script retry on network errors."},
		{"idx": 1, "role": "assistant", "text_preview": "Looking at deploy.sh now."},
		{"idx": 2, "role": "user", "text_preview": "Also log each attempt."},
		{"idx": 3, "role": "assistant", "text_preview": "Done: deploy.sh retries three times and logs each attempt."},
	}
	for _, turn := range turns {
		rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+created.ID+"/turns", owner, turn)
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("turn: %d %s", rec.Code, rec.Body.String())
		}
	}

	out := mcpToolJSON(t, s, owner, proj.ID, "session_summary_get", map[string]any{"session_id": created.ID})
	summary, _ := out["summary"].(string)
	if !strings.Contains(summary, "retry on network errors") || !strings.Contains(summary, "retries three times") {
		t.Fatalf("summary fallback = %q", summary)
	}
	if strings.Contains(summary, "Looking at deploy.sh") {
		t.Fatalf("fallback must use the latest assistant reply: %q", summary)
	}

	// A harvest summary memory wins over the turn overview.
	if err := mem.CreateMemoryItem(ctx, &store.MemoryItem{
		ProjectID: proj.ID, Key: "session/" + created.ID, Content: "Goal: retrying deploys. Outcome: deploy.sh retries and logs.",
		Level: store.LevelProject, Scope: "episode_summary", Status: store.StatusConfirmed, Confidence: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	out = mcpToolJSON(t, s, owner, proj.ID, "session_summary_get", map[string]any{"session_id": created.ID})
	if out["summary"] != "Goal: retrying deploys. Outcome: deploy.sh retries and logs." {
		t.Fatalf("stored summary = %v", out["summary"])
	}
}

// TestMCPWorkspaceInfoNoPlaceholders: without a project the tool used to
// answer project:"project" with empty branch/commit/path.
func TestMCPWorkspaceInfoNoPlaceholders(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	var ids []string
	for _, name := range []string{"ws-alpha", "ws-beta"} {
		p, err := mem.ResolveProject(ctx, "", "", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mem.ClaimProject(ctx, p.ID, "owner"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	owner := loginAs(t, s, "owner")

	// No project, no live workspace: nothing invented, projects listed.
	out := mcpToolJSON(t, s, owner, "", "workspace_info", map[string]any{})
	for _, k := range []string{"project", "project_id", "branch", "commit", "path", "is_dirty"} {
		if _, ok := out[k]; ok {
			t.Fatalf("unknown %s must be omitted: %v", k, out)
		}
	}
	if projects, _ := out["projects"].([]any); len(projects) != 2 {
		t.Fatalf("projects = %v", out["projects"])
	}

	// A live workspace of the caller picks its project.
	if err := mem.RegisterWorkspace(ctx, &store.Workspace{ProjectID: ids[1], UserID: "owner", MachineID: "lap", Path: `D:\beta`,
		Branch: "main", CommitSHA: "abc123", IsDirty: true}); err != nil {
		t.Fatal(err)
	}
	out = mcpToolJSON(t, s, owner, "", "workspace_info", map[string]any{})
	if out["project"] != "ws-beta" || out["project_id"] != ids[1] || out["branch"] != "main" || out["commit"] != "abc123" ||
		out["path"] != `D:\beta` || out["is_dirty"] != true {
		t.Fatalf("resolved workspace_info = %v", out)
	}

	// An explicit project without a workspace: name and id only.
	out = mcpToolJSON(t, s, owner, "", "workspace_info", map[string]any{"project_id": ids[0]})
	if out["project"] != "ws-alpha" || out["project_id"] != ids[0] {
		t.Fatalf("explicit project = %v", out)
	}
	for _, k := range []string{"branch", "commit", "path", "is_dirty", "note"} {
		if _, ok := out[k]; ok {
			t.Fatalf("%s must be omitted: %v", k, out)
		}
	}
}
