package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sliceEmitter collects events for assertions.
type sliceEmitter struct {
	evs *[]Event
}

func (s sliceEmitter) Emit(e Event) { *s.evs = append(*s.evs, e) }

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func collectHarvester(workspace string) (*Harvester, *[]Event) {
	var got []Event
	h := NewHarvester(workspace, sliceEmitter{&got})
	return h, &got
}

func TestTailOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	writeLines(t, p,
		`{"role":"user","content":"Let's use Redis","timestamp":"2026-09-17T10:00:00Z"}`,
		`{"role":"assistant","content":"Agreed, pub/sub support wins","timestamp":"2026-09-17T10:01:00Z"}`,
	)
	h, got := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if len(*got) != 2 || (*got)[0].Type != EventConversationTurn {
		t.Fatalf("expected 2 CONVERSATION_TURN events, got %+v", *got)
	}

	// Re-tailing with no new content yields nothing (byte offset held).
	turns, err = h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 || len(*got) != 2 {
		t.Fatalf("re-tail should be empty, got %d turns %d events", len(turns), len(*got))
	}

	// Appended lines are picked up exactly once.
	writeLines(t, p, `{"role":"user","content":"Ship it","timestamp":"2026-09-17T10:02:00Z"}`)
	turns, err = h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "Ship it" {
		t.Fatalf("append tail got %+v", turns)
	}
}

func TestNoiseFiltering(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "noisy.jsonl")
	writeLines(t, p,
		`{"type":"tool_use","role":"assistant","content":"read file x.go"}`,
		`{"type":"tool_result","role":"tool","content":"file bytes..."}`,
		`{"type":"system","content":"session started"}`,
		`{"role":"assistant","content":[{"type":"text","text":"Use Redis for pub/sub"},{"type":"tool_use","input":{"cmd":"ls"}}],"timestamp":"2026-09-17T10:00:00Z"}`,
		`{"role":"user","content":"I prefer tabs","timestamp":"2026-09-17T10:01:00Z"}`,
		``,
		`not json at all`,
	)
	h, got := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2 (tool noise skipped): %+v", len(turns), turns)
	}
	if turns[0].Speaker != "assistant" || turns[0].Content != "Use Redis for pub/sub" {
		t.Fatalf("content-block extraction wrong: %+v", turns[0])
	}
	if turns[1].Speaker != "user" {
		t.Fatalf("speaker normalization wrong: %+v", turns[1])
	}
	if len(*got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(*got))
	}
	if (*got)[0].Payload["speaker"] != "assistant" || (*got)[0].Payload["content"] != "Use Redis for pub/sub" {
		t.Fatalf("event payload wrong: %+v", (*got)[0].Payload)
	}
}

func TestIdleDetection(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "idle.jsonl")
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	h, _ := collectHarvester(dir)
	h.now = func() time.Time { return now }
	h.IdleTimeout = 5 * time.Minute

	writeLines(t, p, `{"role":"user","content":"hello","timestamp":"2026-09-17T10:00:00Z"}`)
	if _, err := h.TailFile(p); err != nil {
		t.Fatal(err)
	}
	// Not idle yet: 4 minutes later, no completion.
	now = now.Add(4 * time.Minute)
	if evs := h.CheckIdle(); len(evs) != 0 {
		t.Fatalf("premature completion: %+v", evs)
	}
	// Idle past the window: exactly one SESSION_TRANSCRIPT_COMPLETE batch.
	now = now.Add(2 * time.Minute)
	evs := h.CheckIdle()
	if len(evs) != 1 || evs[0].Type != EventSessionComplete {
		t.Fatalf("expected 1 SESSION_TRANSCRIPT_COMPLETE, got %+v", evs)
	}
	if evs[0].Payload["turn_count"] != 1 {
		t.Fatalf("batch should carry turn_count=1, got %+v", evs[0].Payload)
	}
	// Already completed: does not refire.
	if evs := h.CheckIdle(); len(evs) != 0 {
		t.Fatalf("completion refired: %+v", evs)
	}
	// New activity re-arms; next idle window fires again.
	now = now.Add(time.Minute)
	writeLines(t, p, `{"role":"user","content":"back again","timestamp":"2026-09-17T10:07:00Z"}`)
	if _, err := h.TailFile(p); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if evs := h.CheckIdle(); len(evs) != 1 {
		t.Fatalf("re-armed completion missing: %+v", evs)
	}
}

func TestMatchesWorkspace(t *testing.T) {
	h := NewHarvester(`D:\central-memory`, nil)
	if !h.MatchesWorkspace(`C:\Users\x\.claude\projects\D---central-memory\abc.jsonl`) {
		t.Fatal("path containing workspace folder name should match")
	}
	if h.MatchesWorkspace(`/home/u/.claude/projects/other-project/abc.jsonl`) {
		t.Fatal("other project's path must not match")
	}
	empty := NewHarvester("", nil)
	if !empty.MatchesWorkspace("/anything/at/all.jsonl") {
		t.Fatal("empty workspace should match all (no filter)")
	}
}

func TestSQLiteTracking(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.vscdb")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	h, _ := collectHarvester(dir)
	h.now = func() time.Time { return now }
	h.IdleTimeout = 5 * time.Minute

	st, _ := os.Stat(p)
	if h.TrackSQLite(p, st) {
		t.Fatal("first sighting is baseline, not a change")
	}
	now = now.Add(time.Minute)
	if err := os.WriteFile(p, []byte("v1-more-rows"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(p)
	if !h.TrackSQLite(p, st) {
		t.Fatal("size/mtime change should report activity")
	}
	// Idle window after last change -> completion hint for sqlite source.
	now = now.Add(6 * time.Minute)
	evs := h.CheckIdle()
	if len(evs) != 1 || evs[0].Type != EventSessionComplete {
		t.Fatalf("expected sqlite completion hint, got %+v", evs)
	}
	if d, _ := evs[0].Payload["detail"].(string); !strings.Contains(d, "sqlite") {
		t.Fatalf("sqlite hint missing from detail payload: %+v", evs[0].Payload)
	}
}

func TestTornWriteHeldBack(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "torn.jsonl")
	writeLines(t, p, `{"role":"user","content":"full line","timestamp":"2026-09-17T10:00:00Z"}`)
	// Append a partial line with no trailing newline (mid-write torn record).
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":"user","content":"part`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	h, _ := collectHarvester(dir)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "full line" {
		t.Fatalf("torn line must be held back, got %+v", turns)
	}
	// Completing the line ("part" + "ial encore" = "partial encore") delivers
	// it on the next tail as one valid turn.
	writeLines(t, p, `ial encore","timestamp":"2026-09-17T10:01:00Z"}`)
	turns, err = h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "partial encore" {
		t.Fatalf("completed torn line should parse, got %+v", turns)
	}
}
