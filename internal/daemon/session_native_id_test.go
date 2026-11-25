package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// A transcript file's base name is only a session id when the file holds
// exactly one conversation. Container files and brain transcripts produced
// empty "state"/"opencode"/"transcript" chats in Timeline.
func TestSessionIDPerSourceShape(t *testing.T) {
	const brainID = "3f2b8c1e-9a4d-4e6f-8b2a-1c0d9e8f7a6b"
	cases := []struct {
		name string
		path string
		want string
	}{
		{"claude jsonl keeps file id", filepath.Join("home", ".claude", "projects", "D--ws", "8d1f0a2c-1111-2222-3333-444455556666.jsonl"), "8d1f0a2c-1111-2222-3333-444455556666"},
		{"codex rollout keeps file id", filepath.Join("home", ".codex", "sessions", "2026", "rollout-2026-11-01-abc.jsonl"), "rollout-2026-11-01-abc"},
		{"copilot chatSessions keeps file id", filepath.Join("Code", "User", "workspaceStorage", "abc123", "chatSessions", "0b1c2d3e-aaaa-bbbb-cccc-ddddeeeeffff.json"), "0b1c2d3e-aaaa-bbbb-cccc-ddddeeeeffff"},
		{"cursor state.vscdb", filepath.Join("Cursor", "User", "globalStorage", "state.vscdb"), ""},
		{"copilot workspace state.vscdb", filepath.Join("Code", "User", "workspaceStorage", "0123456789abcdef0123456789abcdef", "state.vscdb"), ""},
		{"cursor ai-code-tracking", filepath.Join("home", ".cursor", "ai-tracking", "ai-code-tracking.db"), ""},
		{"opencode db without session", filepath.Join("home", ".local", "share", "opencode", "opencode.db"), ""},
		{"antigravity brain full", filepath.Join("home", ".gemini", "antigravity", "brain", brainID, ".system_generated", "logs", "transcript_full.jsonl"), brainID},
		{"antigravity brain short", filepath.Join("home", ".gemini", "antigravity", "brain", brainID, ".system_generated", "logs", "transcript.jsonl"), brainID},
		{"gemini transcript under uuid folder", filepath.Join("home", ".gemini", "x", brainID, "logs", "transcript.jsonl"), brainID},
		{"transcript with no conversation folder", filepath.Join("home", "logs", "transcript_full.jsonl"), ""},
		{"smoke test leftover", filepath.Join("tmp", "harvest-smoke-test.vscdb"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionID(tc.path); got != tc.want {
				t.Fatalf("sessionID(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestTurnPayloadSessionIDFromSource(t *testing.T) {
	// OpenCode rows carry their own session id, which always wins.
	p := turnPayload("conversation_turn", "opencode", filepath.Join("opencode", "opencode.db"), Turn{Speaker: "user", Content: "hi", SessionID: "ses_abc"})
	if p["session_id"] != "ses_abc" {
		t.Fatalf("opencode session id = %v", p["session_id"])
	}
	// The raw fallback scan of opencode.db has no session: not a chat.
	p = turnPayload("conversation_turn", "opencode", filepath.Join("opencode", "opencode.db"), Turn{Speaker: "user", Content: "hi"})
	if p["session_id"] != "" {
		t.Fatalf("opencode fallback session id = %v, want empty", p["session_id"])
	}
	p = turnPayload("conversation_turn", "cursor", filepath.Join("Cursor", "state.vscdb"), Turn{Speaker: "user", Content: "hi"})
	if p["session_id"] != "" {
		t.Fatalf("cursor state session id = %v, want empty", p["session_id"])
	}
}

// Claude Code writes each tool_use (an Agent sub-agent run included) on its
// own line with no text. Those turns must reach the cloud so the apps can
// show them in the work log; text-less records without calls still must not.
func TestMirrorSessionTurnsKeepsToolOnlyTurns(t *testing.T) {
	var turns []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent-sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "s1"})
		case "/v1/agent-sessions/s1/turns/batch":
			var body struct {
				Turns []map[string]any `json:"turns"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			turns = append(turns, body.Turns...)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	r := &Runtime{Daemon: &Daemon{}, Processor: &Processor{Store: NewHTTPMemoryStore(srv.URL, "tok", "p1")}}
	agent := map[string]any{"name": "Agent", "id": "toolu_1", "input": map[string]any{"description": "List files", "subagent_type": "Explore"}}
	batch := []Event{
		{Type: EventConversationTurn, Payload: map[string]any{"speaker": "user", "content": "start a sub-agent", "timestamp": "2026-11-22T09:20:00Z"}},
		{Type: EventConversationTurn, Payload: map[string]any{"speaker": "assistant", "content": "", "tool_calls": []any{agent}, "timestamp": "2026-11-22T09:20:01Z"}},
		{Type: EventConversationTurn, Payload: map[string]any{"speaker": "assistant", "content": "  ", "timestamp": "2026-11-22T09:20:02Z"}},
	}
	if id := r.mirrorSessionTurns(context.Background(), "86a98602-4ac1-4a67-a498-b25092792f0d", "claude", batch); id != "s1" {
		t.Fatalf("session id = %q", id)
	}
	if len(turns) != 2 {
		t.Fatalf("want the prompt and the tool-only turn, got %d: %+v", len(turns), turns)
	}
	calls, _ := turns[1]["tool_calls"].([]any)
	if turns[1]["role"] != "assistant" || len(calls) != 1 || calls[0].(map[string]any)["name"] != "Agent" {
		t.Fatalf("tool-only turn = %+v", turns[1])
	}
}

func TestMirrorSessionTurnsSkipsUnidentifiedSession(t *testing.T) {
	r := &Runtime{}
	batch := []Event{{Type: EventConversationTurn, Payload: map[string]any{"speaker": "user", "content": "hello there"}}}
	if id := r.mirrorSessionTurns(context.Background(), "", "cursor", batch); id != "" {
		t.Fatalf("mirrored an unidentified session: %q", id)
	}
}
