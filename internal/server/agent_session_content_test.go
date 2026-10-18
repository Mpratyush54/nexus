package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestAgentSessionContentReadsHistoricalNativeID(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	project, err := mem.ResolveProject(t.Context(), "", "", "content-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(t.Context(), project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(t.Context(), project.ID, "viewer", "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	viewer := loginAs(t, s, "viewer")
	const nativeID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	created := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", owner, map[string]any{
		"project_id": project.ID, "harness": "codex", "native_id": nativeID,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var session store.AgentSession
	decodeBody(t, created, &session)
	posted := doJSON(t, s, http.MethodPost, "/sessions/"+nativeID+"/operations", owner, map[string]any{
		"project_id": project.ID, "harness": "codex",
		"file_operations": []map[string]any{{"tool_name": "apply_patch", "file_path": "main.go", "op_type": "edit"}},
		"tool_executions": []map[string]any{{"tool_name": "exec_command", "command_line": "go test ./..."}},
	})
	if posted.Code != http.StatusCreated {
		t.Fatalf("post operations: %d %s", posted.Code, posted.Body.String())
	}
	for _, path := range []string{"files", "operations", "memories"} {
		denied := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+session.ID+"/"+path, viewer, nil)
		if denied.Code != http.StatusForbidden {
			t.Fatalf("%s leaked: %d %s", path, denied.Code, denied.Body.String())
		}
	}
	files := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+session.ID+"/files", owner, nil)
	if files.Code != http.StatusOK || !strings.Contains(files.Body.String(), "main.go") {
		t.Fatalf("files: %d %s", files.Code, files.Body.String())
	}
	commands := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+session.ID+"/operations", owner, nil)
	if commands.Code != http.StatusOK || !strings.Contains(commands.Body.String(), "go test ./...") {
		t.Fatalf("commands: %d %s", commands.Code, commands.Body.String())
	}
	if err := mem.CreateMemoryItem(t.Context(), &store.MemoryItem{
		ProjectID: project.ID, SessionID: nativeID, Key: "session-decision",
		Content: "Use the new capture bridge", Level: store.LevelSession,
		Status: store.StatusProposed, Visibility: store.VisibilityProject,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.CreateMemoryItem(t.Context(), &store.MemoryItem{
		ProjectID: project.ID, Key: "unrelated-project-memory",
		Content: "Do not show in this session", Level: store.LevelProject,
		Status: store.StatusProposed, Visibility: store.VisibilityProject,
	}); err != nil {
		t.Fatal(err)
	}
	memories := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+session.ID+"/memories", owner, nil)
	if memories.Code != http.StatusOK || !strings.Contains(memories.Body.String(), "Use the new capture bridge") ||
		strings.Contains(memories.Body.String(), "Do not show in this session") {
		t.Fatalf("memories: %d %s", memories.Code, memories.Body.String())
	}
}
