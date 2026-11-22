package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"central-memory/internal/usage"
)

// Shapes copied from real local transcripts (values shortened).
const (
	claudeThinkingLine = `{"type":"assistant","requestId":"req_1","timestamp":"2026-11-22T10:00:00Z","message":{"id":"msg_1","model":"claude-opus-5","role":"assistant","content":[{"type":"thinking","thinking":""}],"usage":{"input_tokens":2,"cache_creation_input_tokens":26954,"cache_read_input_tokens":35579,"output_tokens":481}}}`
	claudeToolLine     = `{"type":"assistant","requestId":"req_1","timestamp":"2026-11-22T10:00:01Z","message":{"id":"msg_1","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"Reading the file."}],"usage":{"input_tokens":2,"cache_creation_input_tokens":26954,"cache_read_input_tokens":35579,"output_tokens":481}}}`
	codexContextLine   = `{"timestamp":"2026-11-22T10:36:21Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"D:\\x","model":"gpt-6.1-sol"}}`
	codexTokenLine     = `{"timestamp":"2026-11-22T10:36:24Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":27535,"cached_input_tokens":13824,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":27540},"last_token_usage":{"input_tokens":27535,"cached_input_tokens":13824,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":27540}},"rate_limits":{}}}`
	codexRateOnlyLine  = `{"timestamp":"2026-11-22T10:36:25Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{}}}`
)

func TestParseLineUsageClaudeCodexGemini(t *testing.T) {
	u, key, _ := parseLineUsage([]byte(claudeToolLine))
	if u == nil || key != "claude:msg_1" || u.Model != "claude-opus-5" || u.Input != 2 || u.Output != 481 || u.CacheRead != 35579 || u.CacheWrite != 26954 {
		t.Fatalf("claude usage = %+v %q", u, key)
	}
	if _, _, model := parseLineUsage([]byte(codexContextLine)); model != "gpt-6.1-sol" {
		t.Fatalf("codex model = %q", model)
	}
	u, key, _ = parseLineUsage([]byte(codexTokenLine))
	if u == nil || key != "codex-total:27540" || u.Input != 27535-13824 || u.CacheRead != 13824 || u.Output != 5 {
		t.Fatalf("codex usage = %+v %q", u, key)
	}
	if u, _, _ := parseLineUsage([]byte(codexRateOnlyLine)); u != nil {
		t.Fatalf("rate-limit-only token_count must be ignored: %+v", u)
	}
	gem := `{"id":"g1","timestamp":"2026-11-22T11:00:00Z","type":"gemini","content":"done","model":"gemini-3-pro","tokens":{"input":1000,"output":50,"cached":400,"thoughts":20,"tool":0,"total":1070}}`
	u, key, _ = parseLineUsage([]byte(gem))
	if u == nil || key != "gemini:g1" || u.Input != 600 || u.CacheRead != 400 || u.Output != 70 || u.Model != "gemini-3-pro" {
		t.Fatalf("gemini usage = %+v %q", u, key)
	}
	if u, _, _ := parseLineUsage([]byte(`{"role":"user","content":"hi"}`)); u != nil {
		t.Fatal("plain line has no usage")
	}
}

func TestTailFileEmitsUsageWithoutPollutingDialogue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout.jsonl")
	writeLines(t, p,
		codexContextLine,
		`{"timestamp":"2026-11-22T10:36:22Z","type":"event_msg","payload":{"type":"user_message","message":"fix it"}}`,
		codexTokenLine,
		codexTokenLine, // Codex repeats token_count events
		codexRateOnlyLine,
		claudeThinkingLine,
	)
	h, got := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var usageOnly, dialogue int
	for _, turn := range turns {
		if turn.UsageOnly() {
			usageOnly++
			if turn.Usage.Model == "" {
				t.Fatalf("usage without model: %+v", turn)
			}
		} else {
			dialogue++
		}
	}
	if dialogue != 1 || usageOnly != 3 {
		t.Fatalf("dialogue=%d usageOnly=%d turns=%+v", dialogue, usageOnly, turns)
	}
	if turns[1].Usage.Model != "gpt-6.1-sol" || turns[1].UsageKey != turns[2].UsageKey {
		t.Fatalf("codex usage should carry the model and a repeatable key: %+v %+v", turns[1], turns[2])
	}
	var flagged int
	for _, ev := range *got {
		if only, _ := ev.Payload["usage_only"].(bool); only {
			flagged++
			if u, key := usageFromPayload(ev.Payload["usage"]); u == nil || key == "" {
				t.Fatalf("usage payload lost: %+v", ev.Payload)
			}
		}
	}
	if flagged != 3 {
		t.Fatalf("usage_only events = %d", flagged)
	}
	h.mu.Lock()
	batch := len(h.batch[p])
	h.mu.Unlock()
	if batch != 1 {
		t.Fatalf("session-complete batch must hold dialogue only, got %d", batch)
	}
}

func TestMirrorPostsUsageToLedgerAndDialogueToTurns(t *testing.T) {
	var records []map[string]any
	var turns []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent-sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "s1"})
		case "/v1/agent-sessions/s1/usage":
			var body struct {
				Records []map[string]any `json:"records"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			records = append(records, body.Records...)
			w.WriteHeader(http.StatusCreated)
		case "/v1/agent-sessions/s1/turns/batch":
			var body struct {
				Turns []map[string]any `json:"turns"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			turns = append(turns, body.Turns...)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", "p1")
	at := time.Now().UTC()
	_, err := hs.MirrorAgentSessionTurns(t.Context(), "claude", "n1", "m1", "C:/w", []MirroredTurn{
		{Idx: 10, Role: "assistant", TextPreview: "hello", SourceAt: &at, Usage: &usage.Entry{Model: "claude-opus-5", Input: 3, Output: 4}, UsageKey: "claude:msg_1"},
		{Idx: 11, Usage: &usage.Entry{Model: "gpt-6.1-sol", Output: 9, CostUSD: usage.Cost(0.01)}, UsageKey: "codex-total:9", UsageOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0]["usage"] != nil {
		t.Fatalf("dialogue turns must stay old-server compatible: %+v", turns)
	}
	if len(records) != 2 || records[0]["key"] != "claude:msg_1" || records[1]["cost_usd"] != 0.01 || records[1]["idx"] != float64(11) {
		t.Fatalf("usage records = %+v", records)
	}
}
