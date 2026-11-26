package agentrun

import (
	"testing"

	"central-memory/internal/continuex"
)

// ACP agents (Cursor, OpenCode) send only deltas; their reply must survive.
func TestTurnKeepsDeltaOnlyReplies(t *testing.T) {
	tr := &turn{}
	for _, part := range []string{"Hello", ", ", "world"} {
		tr.track(continuex.LiveEvent{Kind: "text_delta", Text: part}, "")
	}
	tr.flushStreamed()
	if got := tr.answer.String(); got != "Hello, world" {
		t.Fatalf("answer = %q", got)
	}
}

// Claude/Codex send deltas and then the full block; it must not be doubled.
func TestTurnDoesNotDuplicateFullBlocks(t *testing.T) {
	tr := &turn{}
	tr.track(continuex.LiveEvent{Kind: "text_delta", Text: "Hel"}, "")
	tr.track(continuex.LiveEvent{Kind: "text_delta", Text: "lo"}, "")
	tr.track(continuex.LiveEvent{Kind: "text", Text: "Hello"}, "")
	tr.track(continuex.LiveEvent{Kind: "tool_start", ToolID: "t1", ToolName: "Bash"}, "")
	tr.track(continuex.LiveEvent{Kind: "text_delta", Text: "Done"}, "")
	tr.flushStreamed()
	if got := tr.answer.String(); got != "Hello\n\nDone" {
		t.Fatalf("answer = %q", got)
	}
}
