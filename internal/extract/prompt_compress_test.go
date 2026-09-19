package extract

import (
	"strings"
	"testing"
)

func TestBuildCompressPromptRequiresActions(t *testing.T) {
	prompt := BuildCompressPrompt("proj", "sess-1", nil, []Turn{
		{Speaker: "user", Content: "Please edit internal/server/agent_v1.go and add rate limits.", SessionID: "sess-1"},
		{Speaker: "assistant", Content: "Edited agent_v1.go; CheckAgentToolAllowed now gates memory_write.", SessionID: "sess-1"},
	})
	for _, needle := range []string{
		"Actions taken",
		"files created/edited",
		"Outcomes",
		"session_summary",
		"internal/server/agent_v1.go",
		"sess-1",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("compress prompt missing %q\n%s", needle, prompt)
		}
	}
}

func TestBuildPromptKeepsActions(t *testing.T) {
	prompt := BuildPrompt("proj", nil, []Turn{
		{Speaker: "assistant", Content: "Created frontend/src/pages/AgentsPage.tsx for Mint MCP."},
	})
	if !strings.Contains(prompt, "Actions:") {
		t.Fatalf("prompt should require actions:\n%s", prompt)
	}
	if strings.Contains(prompt, "Prefer empty memories[] over low-signal") {
		t.Fatal("old prefer-empty wording should be gone")
	}
}

func TestSessionIDFromTurns(t *testing.T) {
	if got := SessionIDFromTurns([]Turn{{Content: "a"}, {Content: "b", SessionID: "x"}}); got != "x" {
		t.Fatalf("got %q", got)
	}
}

func TestClampMemoryContent(t *testing.T) {
	long := strings.Repeat("a", 2500)
	got := ClampMemoryContent(long)
	if len(got) > 2000 {
		t.Fatalf("len=%d", len(got))
	}
}
