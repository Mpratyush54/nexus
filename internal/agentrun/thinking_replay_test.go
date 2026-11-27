package agentrun

import (
	"sync"
	"testing"
	"time"

	"central-memory/internal/continuex"
)

// A reopened chat replays merged thinking deltas at once; the replay keeps
// when the thinking started ("at") and its last delta ("until"), and the
// reply's first words keep their own time, so the desktop can still say
// "Thought for 7s".
func TestReplayKeepsThinkingTimes(t *testing.T) {
	h := newHarness(t, 6, t.TempDir())
	var mu sync.Mutex
	clock := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	h.m.mu.Lock()
	h.m.cfg.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	h.m.mu.Unlock()
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	start := clock

	h.send("a", "hello", ModeAsk)
	f := h.live("a")
	recv(t, f.sent)
	for _, d := range []string{"Looking ", "at the ", "request"} {
		f.events <- continuex.LiveEvent{Kind: "thinking_delta", Text: d}
		h.waitFor("thinking "+d, func(e map[string]any) bool { return e["kind"] == "thinking_delta" && e["text"] == d })
		advance(3 * time.Second)
	}
	advance(-2 * time.Second) // the words start 7s after the first thought
	f.events <- continuex.LiveEvent{Kind: "text_delta", Text: "Hi"}
	h.waitFor("words", func(e map[string]any) bool { return e["kind"] == "text_delta" })

	events := h.m.Attach("a")["events"].([]map[string]any)
	if len(events) != 2 || events[0]["kind"] != "thinking_delta" || events[1]["kind"] != "text_delta" {
		t.Fatalf("replay %v", events)
	}
	thinking, words := events[0], events[1]
	if thinking["text"] != "Looking at the request" {
		t.Fatalf("merged thinking %q", thinking["text"])
	}
	if thinking["at"] != start.UnixMilli() || thinking["until"] != start.Add(6*time.Second).UnixMilli() {
		t.Fatalf("thinking times at=%v until=%v", thinking["at"], thinking["until"])
	}
	if got := words["at"].(int64) - thinking["at"].(int64); got != 7000 {
		t.Fatalf("thought for %d ms, want 7000", got)
	}
}
