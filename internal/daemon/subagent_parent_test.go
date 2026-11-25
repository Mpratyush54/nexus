package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubagentParentNativeID(t *testing.T) {
	cases := map[string]string{
		`C:\Users\me\.claude\projects\D--repo\0f8e2c1a-6d2b-4f6e-9a51-3b7c2d1e0f9a\subagents\agent-a1b2c3d4e5f6a7b8c.jsonl`: "0f8e2c1a-6d2b-4f6e-9a51-3b7c2d1e0f9a",
		"/home/me/.claude/projects/-repo/parent-1/subagents/agent-0123456789abcdef.jsonl":                                   "parent-1",
		"/home/me/.claude/projects/-repo/0f8e2c1a-6d2b-4f6e-9a51-3b7c2d1e0f9a.jsonl":                                        "",
		"/home/me/.claude/projects/-repo/parent-1/subagents/notes.jsonl":                                                    "",
		"/home/me/.claude/projects/-repo/agent-0123456789abcdef.jsonl":                                                      "",
		"": "",
	}
	for path, want := range cases {
		if got := subagentParentNativeID(path); got != want {
			t.Errorf("subagentParentNativeID(%q) = %q, want %q", path, got, want)
		}
	}
}

// A sub-agent run is upserted with its parent's native id; a server that
// predates the field (unknown fields are a 400) still gets the run.
func TestMirrorSubagentSendsParent(t *testing.T) {
	for _, oldServer := range []bool{false, true} {
		var bodies []map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/agent-sessions" {
				http.NotFound(w, r)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies = append(bodies, body)
			if _, ok := body["parent_native_id"]; ok && oldServer {
				http.Error(w, `invalid JSON body: json: unknown field "parent_native_id"`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cloud-session"})
		}))
		hs := NewHTTPMemoryStore(srv.URL, "test-token", "project-1")
		id, err := hs.MirrorAgentSessionTurnsWithParent(t.Context(), "claude", "agent-0123456789abcdef", "m1", "C:/w", "parent-1", nil, "")
		srv.Close()
		if err != nil || id != "cloud-session" {
			t.Fatalf("old=%v: id=%q err=%v", oldServer, id, err)
		}
		if bodies[0]["parent_native_id"] != "parent-1" {
			t.Fatalf("old=%v: first body %v", oldServer, bodies[0])
		}
		if oldServer {
			if len(bodies) != 2 || bodies[1]["parent_native_id"] != nil || !strings.HasPrefix(bodies[1]["native_id"].(string), "agent-") {
				t.Fatalf("retry bodies %v", bodies)
			}
		} else if len(bodies) != 1 {
			t.Fatalf("new server: %d upserts", len(bodies))
		}
	}
}
