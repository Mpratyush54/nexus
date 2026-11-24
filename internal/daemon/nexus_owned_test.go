package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeTranscriptTitleNamesTheChat(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"fix the login bug please"},"timestamp":"2026-11-22T10:00:00Z"}`,
		`{"type":"ai-title","aiTitle":"Login bug fix","sessionId":"s"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"timestamp":"2026-11-22T10:01:00Z"}`,
	}, "\n")
	turns, err := ParseTurns(strings.NewReader(in))
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns = %d, %v (title lines are not turns)", len(turns), err)
	}
	for _, tr := range turns {
		if tr.SessionTitle != "Login bug fix" {
			t.Fatalf("title = %q", tr.SessionTitle)
		}
	}
	turns, _ = ParseTurns(strings.NewReader(in + "\n" + `{"type":"custom-title","customTitle":"My name","sessionId":"s"}`))
	if turns[0].SessionTitle != "My name" {
		t.Fatalf("a person's own title must win, got %q", turns[0].SessionTitle)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

const (
	titleUserLine      = `{"type":"user","message":{"role":"user","content":"fix the login bug please"},"timestamp":"2026-11-22T10:00:00Z"}`
	titleAssistantLine = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"timestamp":"2026-11-22T10:01:00Z"}`
	titleLaterLine     = `{"type":"user","message":{"role":"user","content":"thanks, also add a test"},"timestamp":"2026-11-22T10:02:00Z"}`
)

// Claude appends ai-title after the turns it names, so the title often
// arrives in a read with no turns. It must still name the next turns.
func TestTailFileTitleArrivingAloneNamesLaterTurns(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	path := filepath.Join(t.TempDir(), "5d727dae-31b4-4c9d-90b9-96b90df255eb.jsonl")
	h := NewHarvester("", nil)

	appendLines(t, path, titleUserLine, titleAssistantLine)
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 2 || turns[0].SessionTitle != "" {
		t.Fatalf("read 1: %d turns, title %q, %v", len(turns), firstTitle(turns), err)
	}

	appendLines(t, path, `{"type":"ai-title","aiTitle":"Login bug fix","sessionId":"s"}`)
	turns, err = h.TailFile(path)
	if err != nil || len(turns) != 0 {
		t.Fatalf("read 2 (title only): %d turns, %v", len(turns), err)
	}

	appendLines(t, path, titleLaterLine)
	turns, err = h.TailFile(path)
	if err != nil || len(turns) != 1 {
		t.Fatalf("read 3: %d turns, %v", len(turns), err)
	}
	if turns[0].SessionTitle != "Login bug fix" {
		t.Fatalf("read 3 title = %q, want the title seen in read 2", turns[0].SessionTitle)
	}
	if got := turnPayload("conversation_turn", "claude", path, turns[0])["session_title"]; got != "Login bug fix" {
		t.Fatalf("payload session_title = %v", got)
	}
}

func TestTailFileCustomTitleWinsAcrossReads(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	path := filepath.Join(t.TempDir(), "c7e18540-31b4-4c9d-90b9-96b90df255eb.jsonl")
	h := NewHarvester("", nil)

	appendLines(t, path, titleUserLine, `{"type":"custom-title","customTitle":"My name","sessionId":"s"}`)
	if _, err := h.TailFile(path); err != nil {
		t.Fatal(err)
	}
	// A later generated title must not replace the person's own name.
	appendLines(t, path, `{"type":"ai-title","aiTitle":"Login bug fix","sessionId":"s"}`, titleAssistantLine)
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 1 {
		t.Fatalf("%d turns, %v", len(turns), err)
	}
	if turns[0].SessionTitle != "My name" {
		t.Fatalf("title = %q, want the custom title", turns[0].SessionTitle)
	}
}

func TestTailFileTitleSurvivesRestoredCursor(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	path := filepath.Join(t.TempDir(), "6a3c732e-c7f9-4d63-9712-89904c6b5314.jsonl")
	appendLines(t, path, titleUserLine, titleAssistantLine, `{"type":"ai-title","aiTitle":"Login bug fix","sessionId":"s"}`)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// A restarted daemon resumes after the title record.
	h := NewHarvester("", nil)
	h.restoreCursors(map[string]int64{path: st.Size()})
	appendLines(t, path, titleLaterLine)
	turns, err := h.TailFile(path)
	if err != nil || len(turns) != 1 {
		t.Fatalf("%d turns, %v", len(turns), err)
	}
	if turns[0].SessionTitle != "Login bug fix" {
		t.Fatalf("title = %q after restoreCursors", turns[0].SessionTitle)
	}
}

func firstTitle(turns []Turn) string {
	if len(turns) == 0 {
		return ""
	}
	return turns[0].SessionTitle
}
