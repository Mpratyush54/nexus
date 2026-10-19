package continuex

import (
	"strings"
	"testing"
)

func TestReadableAgentOutputKeepsAssistantTextOnly(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"command_execution","command":"secret-command"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Codex reply"}}`,
		`{"type":"text","part":{"text":"OpenCode reply"}}`,
		`{"type":"result","result":"Claude reply"}`,
	}, "\n")
	got := readableAgentOutput(raw)
	if strings.Contains(got, "secret-command") || !strings.Contains(got, "Codex reply") || !strings.Contains(got, "OpenCode reply") || !strings.Contains(got, "Claude reply") {
		t.Fatalf("unexpected displayed output: %q", got)
	}
}

func TestCappedOutputReportsTruncation(t *testing.T) {
	var output cappedOutput
	input := strings.Repeat("a", maxHeadlessOutput+10)
	if n, err := output.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("write: %d, %v", n, err)
	}
	if !output.truncated || output.Len() != maxHeadlessOutput {
		t.Fatalf("cap: truncated=%v, bytes=%d", output.truncated, output.Len())
	}
}
