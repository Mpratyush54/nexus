package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestAgentTurnBatchAcceptsLargeIndexesAndRetries(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	project, err := mem.ResolveProject(t.Context(), "", "", "batch-turn-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(t.Context(), project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	token := loginAs(t, s, "owner")
	created := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", token, map[string]any{
		"project_id": project.ID, "harness": "opencode", "native_id": "ses-batch",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var session store.AgentSession
	decodeBody(t, created, &session)
	path := "/v1/agent-sessions/" + session.ID + "/turns/batch"
	turns := []map[string]any{
		{"idx": int64(178970000000000001), "role": "user", "text_preview": "Review the capture", "tool_calls": []any{}},
		{"idx": int64(178970000000000002), "role": "assistant", "text_preview": "Capture reviewed", "tool_calls": []any{}},
	}
	first := doJSON(t, s, http.MethodPost, path, token, map[string]any{"turns": turns})
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), `"created":2`) {
		t.Fatalf("batch create: %d %s", first.Code, first.Body.String())
	}
	retry := doJSON(t, s, http.MethodPost, path, token, map[string]any{"turns": turns})
	if retry.Code != http.StatusCreated || !strings.Contains(retry.Body.String(), `"duplicates":2`) {
		t.Fatalf("batch retry: %d %s", retry.Code, retry.Body.String())
	}
	listed := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+session.ID+"/turns", token, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"count":2`) {
		t.Fatalf("turn list: %d %s", listed.Code, listed.Body.String())
	}
}
