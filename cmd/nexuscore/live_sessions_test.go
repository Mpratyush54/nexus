//go:build nexuscorelib

package main

import (
	"testing"

	"central-memory/internal/continuex"
)

// ACP agents (Cursor, OpenCode) send only deltas; their reply must survive.
func TestLiveAnswerKeepsDeltaOnlyReplies(t *testing.T) {
	e := &liveEntry{}
	for _, part := range []string{"Hello", ", ", "world"} {
		e.track(continuex.LiveEvent{Kind: "text_delta", Text: part})
	}
	e.mu.Lock()
	e.flushStreamedLocked()
	got := e.answer.String()
	e.mu.Unlock()
	if got != "Hello, world" {
		t.Fatalf("answer = %q", got)
	}
}

// Claude/Codex send deltas and then the full block; it must not be doubled.
func TestLiveAnswerDoesNotDuplicateFullBlocks(t *testing.T) {
	e := &liveEntry{}
	e.track(continuex.LiveEvent{Kind: "text_delta", Text: "Hel"})
	e.track(continuex.LiveEvent{Kind: "text_delta", Text: "lo"})
	e.track(continuex.LiveEvent{Kind: "text", Text: "Hello"})
	e.track(continuex.LiveEvent{Kind: "tool_start", ToolID: "t1", ToolName: "Bash"})
	e.track(continuex.LiveEvent{Kind: "text_delta", Text: "Done"})
	e.mu.Lock()
	e.flushStreamedLocked()
	got := e.answer.String()
	e.mu.Unlock()
	if got != "Hello\n\nDone" {
		t.Fatalf("answer = %q", got)
	}
}
