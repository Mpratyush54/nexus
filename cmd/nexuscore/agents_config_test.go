//go:build nexuscorelib

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/agentsetup"
)

// Runs against a temp home (installTestHome sets HOME and USERPROFILE).
func TestAgentsConfigRulesAndMCP(t *testing.T) {
	home := installTestHome(t)
	work := t.TempDir()
	state := t.TempDir()
	call := func(name string, args map[string]any) (any, error) {
		args["workspace"] = work
		args["state_dir"] = state
		raw, _ := json.Marshal(args)
		return agentsConfigCall(context.Background(), name, raw)
	}
	claudeMD := filepath.Join(work, "CLAUDE.md")
	tpl, err := call("agents.config.rules_template", map[string]any{"path": claudeMD})
	if err != nil || !strings.HasPrefix(tpl.(map[string]string)["content"], "# ") {
		t.Fatalf("template: %v %v", tpl, err)
	}
	saved, err := call("agents.config.rules_save", map[string]any{"path": claudeMD, "content": "# Rules\n", "etag": ""})
	if err != nil || !saved.(agentsetup.RulesSaved).Created {
		t.Fatalf("save: %v %v", saved, err)
	}
	if _, err := call("agents.config.rules_save", map[string]any{"path": filepath.Join(home, "notes.txt"), "content": "x"}); err == nil {
		t.Fatal("saved an unlisted file")
	}
	spec := map[string]any{"name": "files", "transport": "stdio", "command": "npx", "args": []string{"-y", "pkg"}}
	dry, err := call("agents.config.mcp_save", map[string]any{"agent": "cursor", "scope": "user", "spec": spec, "dry_run": true})
	if err != nil || dry.(agentsetup.SaveMCPResult).CommandLine != "npx -y pkg" {
		t.Fatalf("dry: %v %v", dry, err)
	}
	if _, err := call("agents.config.mcp_save", map[string]any{"agent": "cursor", "scope": "user", "spec": spec}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".cursor", "mcp.json")
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), `"files"`) {
		t.Fatalf("not written: %s", b)
	}
	if _, err := call("agents.config.mcp_set_enabled", map[string]any{"agent": "cursor", "scope": "user", "path": cfg, "name": "files", "enabled": false}); err != nil {
		t.Fatal(err)
	}
	out, err := agentsBrowseCall("agents.setup.browse", json.RawMessage(`{"state_dir":`+mustJSON(state)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range out.(agentsetup.Inventory).Agents {
		for _, m := range a.MCPServers {
			if a.Agent == "cursor" && m.Name == "files" && m.Parked {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("turned-off server not listed")
	}
	parsed, err := call("agents.config.mcp_parse", map[string]any{"text": `{"mcpServers":{"pw":{"command":"npx","args":["@playwright/mcp@latest"]}}}`})
	if err != nil || len(parsed.(map[string]any)["servers"].([]agentsetup.ServerSpec)) != 1 {
		t.Fatalf("parse: %v %v", parsed, err)
	}
	res, _ := call("agents.config.mcp_test", map[string]any{"spec": map[string]any{"name": "x", "transport": "stdio", "command": "definitely-not-a-real-command"}, "timeout_ms": 3000})
	if r := res.(agentsetup.MCPTestResult); r.OK || !strings.Contains(r.Message, "couldn't find") {
		t.Fatalf("test: %+v", r)
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
