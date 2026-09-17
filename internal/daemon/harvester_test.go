package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedSink records emitted events for assertions.
type capturedSink struct {
	mu       sync.Mutex
	types    []string
	payloads []map[string]any
}

func (c *capturedSink) fn() EventSink {
	return func(eventType string, payload map[string]any) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.types = append(c.types, eventType)
		c.payloads = append(c.payloads, payload)
	}
}

func (c *capturedSink) count(typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.types {
		if t == typ {
			n++
		}
	}
	return n
}

func writeLines(t *testing.T, path string, lines []string) {
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

func TestHarvestParseJSONLTurn(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		wantSpeaker string
		wantContent string
		wantSkip    bool // expect ErrSkippedEntry or parse error
	}{
		{
			name:        "claude human",
			line:        `{"type":"human","message":{"role":"user","content":"Let's use Redis"},"timestamp":"2026-09-17T10:00:00Z"}`,
			wantSpeaker: "user",
			wantContent: "Let's use Redis",
		},
		{
			name:        "claude assistant text parts",
			line:        `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Redis fits."},{"type":"text","text":"Second."}]},"timestamp":"2026-09-17T10:01:00Z"}`,
			wantSpeaker: "assistant",
			wantContent: "Redis fits.\nSecond.",
		},
		{
			name:        "generic role content",
			line:        `{"role":"user","content":"I prefer tabs","created_at":"2026-09-17T10:02:00Z"}`,
			wantSpeaker: "user",
			wantContent: "I prefer tabs",
		},
		{
			name:        "speaker text shape",
			line:        `{"speaker":"assistant","text":"trade-offs below"}`,
			wantSpeaker: "assistant",
			wantContent: "trade-offs below",
		},
		{
			name:        "opencode parts",
			line:        `{"role":"assistant","parts":[{"type":"text","text":"hello there"}]}`,
			wantSpeaker: "assistant",
			wantContent: "hello there",
		},
		{
			name:        "unix timestamp",
			line:        `{"role":"user","content":"hi","timestamp":1758103200}`,
			wantSpeaker: "user",
			wantContent: "hi",
		},
		{
			name:     "tool-only skipped",
			line:     `{"role":"assistant","content":[{"type":"tool_use","name":"read","input":{}}]}`,
			wantSkip: true,
		},
		{
			name:     "tool role skipped",
			line:     `{"role":"tool","content":"output text"}`,
			wantSkip: true,
		},
		{
			name:     "empty content skipped",
			line:     `{"role":"user","content":"   "}`,
			wantSkip: true,
		},
		{
			name:     "metadata line skipped",
			line:     `{"type":"summary","summary":"compacted"}`,
			wantSkip: true,
		},
		{
			name:     "garbage errors",
			line:     `not json at all`,
			wantSkip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, err := ParseJSONLTurn([]byte(tc.line))
			if tc.wantSkip {
				if err == nil {
					t.Fatalf("expected skip/error, got %+v", turn)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if turn.Speaker != tc.wantSpeaker {
				t.Errorf("speaker = %q, want %q", turn.Speaker, tc.wantSpeaker)
			}
			if turn.Content != tc.wantContent {
				t.Errorf("content = %q, want %q", turn.Content, tc.wantContent)
			}
		})
	}

	// Timestamp is extracted when present.
	turn, err := ParseJSONLTurn([]byte(`{"role":"user","content":"hi","timestamp":"2026-09-17T10:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	if turn.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestHarvestTailOffsetResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeLines(t, path, []string{
		`{"role":"user","content":"first"}`,
		`{"role":"assistant","content":"second"}`,
	})

	turns, off, err := TailTurns(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if turns[0].Content != "first" || turns[1].Content != "second" {
		t.Fatalf("unexpected contents: %+v", turns)
	}
	if off <= 0 {
		t.Fatalf("expected positive offset, got %d", off)
	}

	// No re-read: same offset yields nothing and the offset is stable.
	turns, off2, err := TailTurns(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("expected no turns on resume, got %d", len(turns))
	}
	if off2 != off {
		t.Fatalf("offset moved without new data: %d -> %d", off, off2)
	}

	// Append: only the new turn is returned.
	writeLines(t, path, []string{`{"role":"user","content":"third"}`})
	turns, off3, err := TailTurns(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "third" {
		t.Fatalf("expected only the new turn, got %+v", turns)
	}
	if off3 <= off {
		t.Fatalf("offset did not advance: %d -> %d", off, off3)
	}

	// Partial trailing line (no newline) is held back, not lost.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":"user","content":"partial"`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	turns, off4, err := TailTurns(path, off3)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("partial line should be held back, got %+v", turns)
	}
	if off4 != off3 {
		t.Fatalf("offset must not advance past partial line: %d -> %d", off3, off4)
	}

	// Truncation (rotation) re-reads from zero.
	if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"fresh\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	turns, _, err = TailTurns(path, off3)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != "fresh" {
		t.Fatalf("expected re-read after truncation, got %+v", turns)
	}
}

func TestHarvestIdleDetector(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	d := NewIdleDetector(5*time.Minute, clock)

	d.Touch("s1")
	if got := d.IdleSessions(); len(got) != 0 {
		t.Fatalf("fresh session should not be idle: %v", got)
	}
	now = now.Add(4 * time.Minute)
	if got := d.IdleSessions(); len(got) != 0 {
		t.Fatalf("4m should not be idle: %v", got)
	}
	now = now.Add(2 * time.Minute) // 6m total
	got := d.IdleSessions()
	if len(got) != 1 || got[0] != "s1" {
		t.Fatalf("expected s1 idle after 6m, got %v", got)
	}
	// New activity clears idleness.
	d.Touch("s1")
	if got := d.IdleSessions(); len(got) != 0 {
		t.Fatalf("touched session should not be idle: %v", got)
	}
}

func TestHarvestIdleCompleteEmits(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	cap := &capturedSink{}
	h := NewHarvester(t.TempDir(), cap.fn(), WithClock(clock))

	// Register a tracked file directly (it lives outside source dirs).
	dir := t.TempDir()
	path := filepath.Join(dir, "conv.jsonl")
	writeLines(t, path, []string{`{"role":"user","content":"decided on redis"}`})
	tf := TrackedFile{
		Path:      path,
		Agent:     "claude",
		Format:    "jsonl",
		SessionID: SessionIDFor("claude", path),
		Project:   "test-proj",
	}
	h.files[path] = tf
	h.sessFile[tf.SessionID] = tf

	if err := h.processFile(path); err != nil {
		t.Fatal(err)
	}
	if cap.count(EventConversationTurn) != 1 {
		t.Fatalf("expected 1 CONVERSATION_TURN, got %d", cap.count(EventConversationTurn))
	}
	got := cap.payloads[0]
	if got["speaker"] != "user" || got["content"] != "decided on redis" {
		t.Fatalf("unexpected turn payload: %v", got)
	}
	if got["session_id"] != tf.SessionID || got["agent"] != "claude" {
		t.Fatalf("unexpected identity fields: %v", got)
	}

	// Reprocessing without new data emits nothing (offset resume).
	if err := h.processFile(path); err != nil {
		t.Fatal(err)
	}
	if cap.count(EventConversationTurn) != 1 {
		t.Fatalf("reprocess must not re-emit, got %d", cap.count(EventConversationTurn))
	}

	// Still active before 5 minutes.
	now = now.Add(4 * time.Minute)
	if n := h.CheckIdle(); n != 0 {
		t.Fatalf("expected no completion at 4m, got %d", n)
	}

	// Idle past threshold triggers exactly one SESSION_TRANSCRIPT_COMPLETE.
	now = now.Add(2 * time.Minute)
	if n := h.CheckIdle(); n != 1 {
		t.Fatalf("expected 1 completion, got %d", n)
	}
	if cap.count(EventSessionTranscriptComplete) != 1 {
		t.Fatalf("expected 1 complete event, got %d", cap.count(EventSessionTranscriptComplete))
	}
	last := cap.payloads[len(cap.payloads)-1]
	if last["turn_count"] != 1 {
		t.Fatalf("expected turn_count=1, got %v", last["turn_count"])
	}

	// No duplicate completion on the next sweep.
	if n := h.CheckIdle(); n != 0 {
		t.Fatalf("completion must fire once, got %d", n)
	}

	// A resumed session re-arms and can complete again.
	writeLines(t, path, []string{`{"role":"assistant","content":"ack"}`})
	if err := h.processFile(path); err != nil {
		t.Fatal(err)
	}
	if cap.count(EventConversationTurn) != 2 {
		t.Fatalf("expected resumed turn emitted, got %d", cap.count(EventConversationTurn))
	}
	now = now.Add(6 * time.Minute)
	if n := h.CheckIdle(); n != 1 {
		t.Fatalf("expected re-completion after resume, got %d", n)
	}
}

func TestHarvestWorkspaceMatching(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()

	// Transcript whose embedded cwd points at the workspace matches.
	matching := filepath.Join(t.TempDir(), "conv.jsonl")
	cwd := strings.ReplaceAll(ws, `\`, `\\`)
	if err := os.WriteFile(matching,
		[]byte("{\"cwd\":\""+cwd+"\",\"role\":\"user\",\"content\":\"hi\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !WorkspaceMatchesFile(ws, matching, home) {
		t.Error("transcript with workspace cwd should match")
	}

	// Transcript pointing elsewhere does not match.
	other := filepath.Join(t.TempDir(), "other.jsonl")
	if err := os.WriteFile(other,
		[]byte("{\"cwd\":\"C:\\\\unrelated\\\\proj\",\"role\":\"user\",\"content\":\"hi\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if WorkspaceMatchesFile(ws, other, home) {
		t.Error("transcript with foreign cwd should not match")
	}

	// A transcript inside the workspace always matches.
	inside := filepath.Join(ws, ".opencode", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("{\"role\":\"user\",\"content\":\"hi\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !WorkspaceMatchesFile(ws, inside, home) {
		t.Error("transcript inside workspace should match")
	}
}

func TestHarvestTranscriptSources(t *testing.T) {
	home := t.TempDir()
	appData := t.TempDir()
	srcs := TranscriptSources(home, appData)
	want := map[string]string{
		"claude": "jsonl", "opencode": "jsonl",
		"cursor": "sqlite", "copilot": "sqlite", "antigravity": "sqlite",
	}
	if len(srcs) != len(want) {
		t.Fatalf("got %d sources, want %d", len(srcs), len(want))
	}
	for _, s := range srcs {
		wf, ok := want[s.Agent]
		if !ok {
			t.Errorf("unexpected source %q", s.Agent)
			continue
		}
		if s.Format != wf {
			t.Errorf("source %q format = %q, want %q", s.Agent, s.Format, wf)
		}
		if len(s.Dirs) == 0 {
			t.Errorf("source %q has no dirs", s.Agent)
		}
	}
}

func TestHarvestConversationFilesClassifyFilter(t *testing.T) {
	// JSONL transcripts are found; caches/credentials are excluded via
	// adapters.ClassifyPath (fail-closed reuse).
	dir := t.TempDir()
	good := filepath.Join(dir, "projects", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "projects", "cache", "c.jsonl")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := ConversationFiles(TranscriptSource{Agent: "claude", Dirs: []string{dir}, Format: "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != good {
		t.Fatalf("expected only the transcript, got %v", files)
	}
}
