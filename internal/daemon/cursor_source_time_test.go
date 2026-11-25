package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Record shapes copied from real ~/.cursor/projects/<slug>/agent-transcripts
// files (content scrubbed). No record has a time field; user prompts start
// with Cursor's <timestamp> header.
const (
	cursorUserLine1 = `{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Monday, Oct 19, 2026, 10:08 PM (UTC+5:30)</timestamp>\n<user_query>\nplease fix the widget\n</user_query>"}]}}`
	cursorToolLine  = `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"ReadFile","input":{"path":"widget.go"}}]}}`
	cursorReplyLine = `{"role":"assistant","message":{"content":[{"type":"text","text":"The widget is fixed."}]}}`
	cursorUserLine2 = `{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Tuesday, Oct 20, 2026, 12:21 AM (UTC+5:30)</timestamp>\n<user_query>\nnow add a test\n</user_query>"}]}}`
	cursorStatusRec = `{"type":"status","status":"completed"}`
	cursorPlainUser = `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nolder prompt without a header\n</user_query>"}]}}`
)

var (
	cursorPrompt1At = time.Date(2026, 10, 19, 16, 38, 0, 0, time.UTC)
	cursorPrompt2At = time.Date(2026, 10, 19, 18, 51, 0, 0, time.UTC)
)

// serverAcceptsSourceTime mirrors validAgentSourceTime in internal/server.
func serverAcceptsSourceTime(at, now time.Time) bool {
	return !at.IsZero() && at.Year() >= 2000 && !at.After(now.Add(5*time.Minute))
}

func cursorTranscriptPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "projects", "d-demo", "agent-transcripts", "chat-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "chat-1.jsonl")
}

func TestCursorPromptTimeParsesHeader(t *testing.T) {
	cases := []struct {
		text string
		want time.Time
		ok   bool
	}{
		{"<timestamp>Monday, Oct 19, 2026, 10:08 PM (UTC+5:30)</timestamp>\n<user_query>x", cursorPrompt1At, true},
		{"<timestamp>Saturday, Aug 1, 2026, 9:05 AM (UTC-7)</timestamp>", time.Date(2026, 8, 1, 16, 5, 0, 0, time.UTC), true},
		{"<timestamp>Saturday, August 1, 2026, 9:05 AM (UTC)</timestamp>", time.Date(2026, 8, 1, 9, 5, 0, 0, time.UTC), true},
		{"quoted later: <timestamp>Monday, Oct 19, 2026, 10:08 PM (UTC+5:30)</timestamp>", time.Time{}, false},
		{"<timestamp>not a date</timestamp>", time.Time{}, false},
		{"<timestamp>Monday, Oct 19, 2026, 10:08 PM (PST+99)</timestamp>", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := cursorPromptTime(c.text)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("cursorPromptTime(%q) = %v,%v want %v,%v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestCursorTranscriptTurnsCarryPromptTimes(t *testing.T) {
	path := cursorTranscriptPath(t)
	writeLines(t, path, cursorUserLine1, cursorToolLine, cursorReplyLine, cursorStatusRec, cursorUserLine2, cursorReplyLine)
	h, events := collectHarvester(filepath.Dir(path))
	now := time.Date(2026, 11, 22, 15, 13, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 5 {
		t.Fatalf("turns=%d err=%v", len(turns), err)
	}
	want := []time.Time{cursorPrompt1At, cursorPrompt1At, cursorPrompt1At, cursorPrompt2At, cursorPrompt2At}
	for i, turn := range turns {
		if !turn.Timestamp.Equal(want[i]) || !serverAcceptsSourceTime(turn.Timestamp, now) {
			t.Fatalf("turn %d (%s) at %v, want %v", i, turn.Speaker, turn.Timestamp, want[i])
		}
		if got, _ := (*events)[i].Payload["timestamp"].(string); got != want[i].Format(time.RFC3339Nano) {
			t.Fatalf("event %d timestamp %q", i, got)
		}
	}

	// A reply appended in a later poll inherits the prompt it answers.
	writeLines(t, path, cursorReplyLine)
	more, err := h.TailFile(path)
	if err != nil || len(more) != 1 || !more[0].Timestamp.Equal(cursorPrompt2At) {
		t.Fatalf("later reply: %+v %v", more, err)
	}
}

func TestCursorResumedCursorRecoversPromptTimeBeforeOffset(t *testing.T) {
	path := cursorTranscriptPath(t)
	writeLines(t, path, cursorUserLine1, cursorReplyLine)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, cursorReplyLine)
	h, _ := collectHarvester(filepath.Dir(path))
	h.now = func() time.Time { return time.Now().UTC() }
	h.restoreCursors(map[string]int64{path: st.Size()})
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 1 || !turns[0].Timestamp.Equal(cursorPrompt1At) {
		t.Fatalf("resumed turn: %+v %v", turns, err)
	}
}

func TestCursorHistoricalTranscriptWithoutHeaderUsesModTime(t *testing.T) {
	path := cursorTranscriptPath(t)
	writeLines(t, path, cursorPlainUser, cursorReplyLine)
	mtime := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	h, _ := collectHarvester(filepath.Dir(path))
	now := time.Date(2026, 11, 22, 15, 13, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns=%d err=%v", len(turns), err)
	}
	for i, turn := range turns {
		if !turn.Timestamp.Equal(mtime) || !serverAcceptsSourceTime(turn.Timestamp, now) {
			t.Fatalf("turn %d at %v, want mtime %v", i, turn.Timestamp, mtime)
		}
	}
}

func TestCursorTranscriptStillBeingWrittenKeepsNoSourceTime(t *testing.T) {
	path := cursorTranscriptPath(t)
	writeLines(t, path, cursorPlainUser, cursorReplyLine)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := collectHarvester(filepath.Dir(path))
	h.now = func() time.Time { return st.ModTime().Add(time.Minute) }
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns=%d err=%v", len(turns), err)
	}
	for i, turn := range turns {
		if !turn.Timestamp.IsZero() {
			t.Fatalf("live turn %d got mtime %v", i, turn.Timestamp)
		}
	}
}

func TestNonCursorTranscriptDoesNotInheritOrUseModTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	writeLines(t, path, `{"role":"user","content":"no source timestamp"}`)
	old := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	h, _ := collectHarvester(root)
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 1 || !turns[0].Timestamp.IsZero() {
		t.Fatalf("non-cursor turn dated: %+v %v", turns, err)
	}
}

func TestSQLiteFallbackUserTurnReadsCursorPromptTime(t *testing.T) {
	text := `{"role":"user","content":"<timestamp>Monday, Oct 19, 2026, 10:08 PM (UTC+5:30)</timestamp>\n<user_query>\nplease fix the widget now\n</user_query>"}`
	turns := turnsFromText(text)
	if len(turns) != 1 || !turns[0].Timestamp.Equal(cursorPrompt1At) {
		t.Fatalf("fallback turns: %+v", turns)
	}
}
