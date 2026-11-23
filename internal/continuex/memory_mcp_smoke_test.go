//go:build livesmoke

package continuex

// Checks that agents started by Nexus can call Nexus memory through the
// injected remote MCP server. Uses the signed-in desktop credentials.
// Run: go test -tags livesmoke -run TestMemoryMCP -v ./internal/continuex/

import (
	"context"
	"strings"
	"testing"
	"time"

	"central-memory/internal/config"
)

func TestMemoryMCP(t *testing.T) {
	token := strings.TrimSpace(config.ResolveToken())
	base := strings.TrimRight(config.ResolveServerURL("https://api-nexus.pratyushes.dev"), "/")
	if token == "" {
		t.Skip("not signed in")
	}
	servers := []MCPServer{{Name: "nexus", URL: base + "/v1/agent/mcp",
		Headers:   map[string]string{"Authorization": "Bearer " + token},
		AutoAllow: []string{"memory_search", "project_knowledge"}}}
	prompt := "Call the nexus memory_search tool once with the query \"database\". Then reply with only the number of results it returned."
	starters := map[string]func(context.Context, LiveOptions) (LiveSession, error){
		"claude": StartClaudeLive,
		"codex":  StartCodexLive,
		"cursor": func(c context.Context, o LiveOptions) (LiveSession, error) { return StartACPLive(c, "cursor", o) },
	}
	for _, name := range []string{"claude", "codex", "cursor"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			s, err := starters[name](ctx, LiveOptions{Workspace: t.TempDir(), MCPServers: servers})
			if err != nil {
				t.Skip(err)
			}
			defer s.Close()
			_ = s.Send(ctx, prompt)
			var reply strings.Builder
			sawMemoryTool, approvals := false, 0
			for ev := range s.Events() {
				switch ev.Kind {
				case "text_delta":
					reply.WriteString(ev.Text)
				case "tool_start", "tool_end":
					if strings.Contains(strings.ToLower(ev.ToolName+string(ev.ToolInput)), "memory_search") {
						sawMemoryTool = true
					}
					t.Logf("%s %s %.160s", ev.Kind, ev.ToolName, ev.ToolOutput)
				case "approval":
					approvals++
					t.Logf("approval requested for %s", ev.ToolName)
					_ = s.Approve(ev.RequestID, true, "")
				case "status", "error":
					t.Logf("%s: %.200s", ev.Kind, ev.Text)
				}
				if ev.Kind == "turn_done" || ev.Kind == "exited" {
					break
				}
			}
			t.Logf("%s: memoryTool=%v approvals=%d reply=%q", name, sawMemoryTool, approvals, strings.TrimSpace(reply.String()))
			if !sawMemoryTool {
				t.Errorf("%s never called the Nexus memory tool", name)
			}
		})
	}
}
