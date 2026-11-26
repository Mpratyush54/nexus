package continuex

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStreamAgentStatusThinking(t *testing.T) {
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me look"}}}`,
		`{"type":"item.started","item":{"id":"r1","type":"reasoning","text":""}}`,
	} {
		if got := streamAgentStatus(line); got != thinkingStatus {
			t.Errorf("streamAgentStatus(%s) = %q, want %q", line, got, thinkingStatus)
		}
	}
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}}`,
		`{"type":"item.started","item":{"id":"m1","type":"agent_message","text":""}}`,
	} {
		if got := streamAgentStatus(line); got == thinkingStatus {
			t.Errorf("streamAgentStatus(%s) = %q, want no thinking status", line, got)
		}
	}
}

func TestStreamingOutputReportsThinking(t *testing.T) {
	var statuses []string
	out := newStreamingOutput(func(p StreamProgress) {
		if p.Kind == "status" {
			statuses = append(statuses, p.Text)
		}
	}, "claude")
	_, _ = out.WriteString(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"a"}}}` + "\n")
	_, _ = out.WriteString(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"b"}}}` + "\n")
	if len(statuses) != 1 || statuses[0] != thinkingStatus {
		t.Fatalf("statuses = %q, want one %q", statuses, thinkingStatus)
	}
}

func thinkingUpdates(t *testing.T, agent string, lines ...string) []string {
	t.Helper()
	var got []string
	out := newStreamingOutput(func(p StreamProgress) {
		if p.Kind == "thinking" {
			got = append(got, p.Text)
		}
	}, agent)
	for _, line := range lines {
		_, _ = out.WriteString(line + "\n")
	}
	out.Flush()
	return got
}

func TestStreamingOutputCarriesClaudeThinking(t *testing.T) {
	got := thinkingUpdates(t, "claude",
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Looking at "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the request"}}}`,
		// The whole block again in the assistant message: not repeated.
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"Looking at the request"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}}`,
	)
	if len(got) == 0 || got[len(got)-1] != "Looking at the request" {
		t.Fatalf("thinking updates = %q", got)
	}
	// Throttled: the first delta goes at once, the rest when the reply starts.
	if len(got) > 2 {
		t.Fatalf("thinking was not throttled: %d updates %q", len(got), got)
	}
}

func TestStreamingOutputCarriesCodexReasoning(t *testing.T) {
	got := thinkingUpdates(t, "codex",
		`{"type":"item.started","item":{"id":"r1","type":"reasoning","text":""}}`,
		`{"type":"item.completed","item":{"id":"r1","type":"reasoning","text":"**Planning** the fix"}}`,
		`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"ls"}}`,
		`{"type":"item.completed","item":{"id":"r2","type":"reasoning","summary":[{"text":"Checking tests"}]}}`,
		`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"Done"}}`,
	)
	if len(got) == 0 || got[len(got)-1] != "**Planning** the fix\n\nChecking tests" {
		t.Fatalf("thinking updates = %q", got)
	}
}

func TestStreamingOutputWithoutThinkingSendsNone(t *testing.T) {
	if got := thinkingUpdates(t, "claude", `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}}`); len(got) != 0 {
		t.Fatalf("thinking updates = %q, want none", got)
	}
}

func TestThinkingIsCappedToItsLatestPart(t *testing.T) {
	var b thinkingBuffer
	chunk := strings.Repeat("é", 1000) // 2 bytes each
	for i := 0; i < 200; i++ {
		b.add(thinkingPiece{text: chunk, delta: true})
	}
	got := b.String()
	if len(got) > MaxThinkingBytes || !strings.HasPrefix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("capped thinking: %d bytes, prefix %q, valid %v", len(got), got[:6], utf8.ValidString(got))
	}
	if b.size > 4*MaxThinkingBytes+len(chunk) {
		t.Fatalf("buffer kept %d bytes", b.size)
	}
	if CapThinking("short") != "short" {
		t.Fatal("short thinking changed")
	}
}
