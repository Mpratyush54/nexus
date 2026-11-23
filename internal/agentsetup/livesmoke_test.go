//go:build livesmoke

package agentsetup

import (
	"path/filepath"
	"testing"
)

func TestScanThisMachine(t *testing.T) {
	r := resolve(ScanOptions{Workspace: `D:\cm-ui`})
	for _, rel := range []string{".claude.json", ".codex/config.toml", ".cursor/mcp.json"} {
		m, e := r.config(filepath.Join(r.home, filepath.FromSlash(rel)))
		if e != nil {
			t.Logf("config %s: unavailable or malformed", rel)
			continue
		}
		t.Logf("config %s: valid, MCP entries=%d", rel, len(mcpMap(m, map[string]string{".claude.json": "claude", ".codex/config.toml": "codex", ".cursor/mcp.json": "cursor"}[rel], "user", "")))
	}
	inv := Scan(ScanOptions{Workspace: `D:\cm-ui`})
	for _, a := range inv.Agents {
		t.Logf("%s installed=%v subagents=%d skills=%d commands=%d mcp=%d rules=%d", a.Agent, a.Installed, len(a.SubAgents), len(a.Skills), len(a.Commands), len(a.MCPServers), len(a.Rules))
		for _, items := range [][]Item{a.SubAgents, a.Skills, a.Commands, a.MCPServers, a.Rules} {
			for _, i := range items {
				t.Logf("  %s [%s] %s", i.Kind, i.Scope, i.Name)
			}
		}
	}
}
