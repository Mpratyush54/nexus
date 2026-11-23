package continuex

import (
	"encoding/json"
	"math"
	"testing"
)

func lastOfKind(q []LiveEvent, kind string) *LiveEvent {
	for i := len(q) - 1; i >= 0; i-- {
		if q[i].Kind == kind {
			return &q[i]
		}
	}
	return nil
}

func TestClaudeLiveUsagePerTurnFromCumulativeModelUsage(t *testing.T) {
	s := &claudeLive{wake: make(chan struct{}, 1), pending: map[string]json.RawMessage{}}
	msg := `{"type":"assistant","session_id":"n1","message":{"id":"msg_1","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}}`
	s.parse([]byte(msg))
	s.parse([]byte(msg)) // same message on its next content line
	if n := len(s.queue); n == 0 {
		t.Fatal("no events")
	}
	usageEvents := 0
	for _, e := range s.queue {
		if e.Kind == "usage" {
			usageEvents++
		}
	}
	if usageEvents != 1 {
		t.Fatalf("repeated message usage must not re-emit: %d", usageEvents)
	}
	if u := lastOfKind(s.queue, "usage").Usage; len(u) != 1 || u[0].Input != 10 || u[0].CacheRead != 100 {
		t.Fatalf("streaming usage = %+v", u)
	}
	// Two results: modelUsage/total_cost_usd are cumulative for the process.
	s.parse([]byte(`{"type":"result","session_id":"n1","total_cost_usd":0.5,"modelUsage":{"claude-opus-5":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":100,"cacheCreationInputTokens":20,"costUSD":0.5}}}`))
	done := lastOfKind(s.queue, "turn_done")
	if done == nil || len(done.Usage) != 1 || done.Usage[0].Output != 5 || *done.Usage[0].CostUSD != 0.5 || done.CostUSD != 0.5 {
		t.Fatalf("first turn = %+v", done)
	}
	s.parse([]byte(`{"type":"result","session_id":"n1","total_cost_usd":0.75,"modelUsage":{"claude-opus-5":{"inputTokens":30,"outputTokens":9,"cacheReadInputTokens":300,"cacheCreationInputTokens":20,"costUSD":0.75}}}`))
	done = lastOfKind(s.queue, "turn_done")
	if len(done.Usage) != 1 || done.Usage[0].Input != 20 || done.Usage[0].Output != 4 || done.Usage[0].CacheWrite != 0 || math.Abs(*done.Usage[0].CostUSD-0.25) > 1e-9 {
		t.Fatalf("second turn must be the delta: %+v", done.Usage)
	}
}

func TestCodexLiveUsageSumsCallsAndDedupesRepeats(t *testing.T) {
	s := &codexLive{wake: make(chan struct{}, 1), work: make(chan struct{}, 1), model: "gpt-6.1-sol",
		pending: map[string]codexApproval{}, items: map[string]json.RawMessage{}, calls: map[string]chan codexRPC{}}
	update := func(total, in, cached, out int) {
		raw, _ := json.Marshal(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{
			"threadId": "", "turnId": "t1", "tokenUsage": map[string]any{
				"total": map[string]any{"inputTokens": 0, "cachedInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": total},
				"last":  map[string]any{"inputTokens": in, "cachedInputTokens": cached, "outputTokens": out, "reasoningOutputTokens": 0, "totalTokens": in + out},
			}}})
		s.parse(raw)
	}
	update(500000, 100, 40, 10) // resumed thread: large prior total
	update(500000, 100, 40, 10) // repeated update
	update(500200, 150, 100, 30)
	s.parse([]byte(`{"method":"turn/completed","params":{"threadId":"","turn":{"id":"t1","status":"completed"}}}`))
	done := lastOfKind(s.queue, "turn_done")
	if done == nil || len(done.Usage) != 1 {
		t.Fatalf("turn_done = %+v", done)
	}
	u := done.Usage[0]
	if u.Model != "gpt-6.1-sol" || u.Input != 60+50 || u.CacheRead != 140 || u.Output != 40 || u.CostUSD != nil {
		t.Fatalf("codex turn usage = %+v", u)
	}
}

func TestACPPromptUsageOptional(t *testing.T) {
	if got := acpPromptUsage(json.RawMessage(`{"stopReason":"end_turn"}`), "m"); got != nil {
		t.Fatalf("no usage = %+v", got)
	}
	got := acpPromptUsage(json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":2,"thoughtTokens":1,"cachedReadTokens":3}}`), "m")
	if len(got) != 1 || got[0].Output != 3 || got[0].CacheRead != 3 || got[0].Model != "m" {
		t.Fatalf("usage = %+v", got)
	}
}
