//go:build nexuscorelib

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/agentsetup"
)

func TestAgentsBrowseReadRemove(t *testing.T) {
	home := installTestHome(t)
	cfg := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":{"nexus":{"url":"https://nx.example/v1/agent/mcp","headers":{"Authorization":"Bearer nxs_supersecretvalue"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) (any, error) {
		raw, _ := json.Marshal(args)
		return agentsBrowseCall(name, raw)
	}
	out, err := call("agents.setup.browse", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	inv := out.(agentsetup.Inventory)
	found := false
	for _, a := range inv.Agents {
		if a.Agent == "cursor" {
			found = a.Installed && a.NexusMCP && len(a.MCPServers) == 1
		}
	}
	if !found {
		t.Fatalf("cursor not reported: %+v", inv)
	}
	read, err := call("agents.setup.read", map[string]any{"agent": "cursor", "kind": "mcp", "name": "nexus", "path": cfg})
	if err != nil {
		t.Fatal(err)
	}
	if c := read.(agentsetup.ItemContent); strings.Contains(c.Content, "supersecret") || !strings.Contains(c.Content, "Authorization") {
		t.Fatalf("read: %+v", c)
	}
	if _, err := call("agents.setup.read", map[string]any{"agent": "cursor", "kind": "rules", "name": "x", "path": filepath.Join(home, "secret.txt")}); err == nil {
		t.Fatal("read an unlisted path")
	}
	if _, err := call("agents.setup.mcp_remove", map[string]any{"agent": "cursor", "name": "nexus", "path": cfg}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); strings.Contains(string(b), "nexus") {
		t.Fatalf("not removed: %s", b)
	}
}
