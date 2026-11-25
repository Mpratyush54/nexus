package agentsetup

import (
	"errors"
	"strings"
	"testing"
)

func TestShareReasonsArePlainSentences(t *testing.T) {
	o := ShareOptions{From: "claude", Item: Item{Kind: "skill", Name: "design"}}
	cases := map[string]string{
		"unsafe skill path":                          "outside its folder",
		"skill contains a symlink":                   "outside its folder",
		"project scope requires Workspace":           "Choose a project folder first",
		"Codex MCP sharing supports user scope only": "Choose All projects",
		"source item was not found in inventory":     "Refresh the list",
		"invalid target MCP map":                     "nothing was changed",
	}
	for raw, want := range cases {
		got := plainShareError(errors.New(raw), o).Error()
		if !strings.Contains(got, want) || strings.Contains(strings.ToLower(got), "mcp") {
			t.Errorf("%q -> %q, want it to mention %q", raw, got, want)
		}
	}
	if got := incompatibleSkip("gemini", "claude", "subagent"); got != "Gemini can't use Claude Code sub-agents, so it was skipped." {
		t.Errorf("incompatible: %q", got)
	}
	if got := plainRemoveError(errors.New("that MCP server is not in the agent inventory"), "cursor", "github").Error(); got != "github is no longer in Cursor's settings. Refresh the list." {
		t.Errorf("remove: %q", got)
	}
	if plainShareError(nil, o) != nil || plainRemoveError(nil, "claude", "x") != nil {
		t.Error("nil must stay nil")
	}
}
