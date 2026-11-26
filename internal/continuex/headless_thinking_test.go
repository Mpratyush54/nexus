package continuex

import "testing"

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
