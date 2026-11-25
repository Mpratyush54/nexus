package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

// Session content (legacy snapshots, operations) is keyed by a UUID column.
// A non-UUID agent native id such as OpenCode's "ses_…" used to fail the
// owner insert and answer 500 "could not record session owner", which
// clients retried on every cycle. It is a client error: 400.
func TestSessionContentRejectsNonUUIDSessionID(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	project, err := mem.ResolveProject(t.Context(), "", "", "owner-native-ids")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(t.Context(), project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	for _, id := range []string{"ses_f5489d01effe5oGhwcLSpWk2ci", "rollout-2026-11-23T04-31-14-5d92f0af", "agent-0123456789abcdef"} {
		snap := doJSON(t, s, http.MethodPost, "/sessions/"+id+"/snapshot", owner, map[string]any{
			"project_id": project.ID, "harness": "opencode", "conversation_id": id, "transcript_payload_b64": "eA==",
		})
		if snap.Code != http.StatusBadRequest || !strings.Contains(snap.Body.String(), "UUID") {
			t.Fatalf("snapshot for %s: %d %s", id, snap.Code, snap.Body.String())
		}
		ops := doJSON(t, s, http.MethodPost, "/sessions/"+id+"/operations", owner, map[string]any{
			"project_id": project.ID, "harness": "opencode",
			"file_operations": []map[string]any{{"tool_name": "edit", "file_path": "a.go", "op_type": "edit"}},
		})
		if ops.Code != http.StatusBadRequest {
			t.Fatalf("operations for %s: %d %s", id, ops.Code, ops.Body.String())
		}
	}
	// A UUID session id still works.
	const id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	snap := doJSON(t, s, http.MethodPost, "/sessions/"+id+"/snapshot", owner, map[string]any{
		"project_id": project.ID, "harness": "claude", "conversation_id": id, "transcript_payload_b64": "eA==",
	})
	if snap.Code != http.StatusCreated {
		t.Fatalf("uuid snapshot: %d %s", snap.Code, snap.Body.String())
	}
}
