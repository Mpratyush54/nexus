package agentsetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func browseFixture(t *testing.T) (BrowseOptions, string, string) {
	t.Helper()
	h, w := fixture(t)
	put(t, filepath.Join(h, ".claude", "agents", "reviewer.md"), "---\nname: reviewer\ndescription: Reviews diffs\n---\nBe strict.\n")
	put(t, filepath.Join(h, ".claude", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Ships it\n---\nSteps. Use API_KEY="+plantedSecret+"1 here.\n")
	put(t, filepath.Join(h, ".codex", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Ships it\n---\nSteps.\n")
	put(t, filepath.Join(h, ".claude.json"), `{"numStartups": 3, "mcpServers": {
		"nexus": {"type": "http", "url": "https://nx.example/v1/agent/mcp", "headers": {"Authorization": "Bearer nxs_`+plantedSecret+`"}},
		"github": {"command": "npx", "args": ["gh-mcp", "--token", "`+plantedSecret+`"], "env": {"GITHUB_TOKEN": "`+plantedSecret+`", "LOG": "debug"}}
	}}`)
	put(t, filepath.Join(h, ".codex", "config.toml"), "model = \"o3\"\n\n[mcp_servers.nexus]\nurl = \"https://nx.example/v1/agent/mcp\"\n\n[mcp_servers.nexus.http_headers]\nAuthorization = \"Bearer "+plantedSecret+"\"\n\n[mcp_servers.other]\ncommand = \"other-mcp\"\n")
	put(t, filepath.Join(h, ".codeium", "windsurf", "mcp_config.json"), `{"mcpServers": {"remote": {"serverUrl": "https://u:`+plantedSecret+`@ws.example/mcp?api_key=`+plantedSecret+`", "headers": {"X-Api-Key": "`+plantedSecret+`"}}}}`)
	put(t, filepath.Join(h, ".codeium", "windsurf", "memories", "global_rules.md"), "# Windsurf rules\nUse tabs.\n")
	put(t, filepath.Join(h, ".kimi", "mcp.json"), `{"mcpServers": {"nexus": {"url": "https://nx.example/v1/agent/mcp", "transport": "http", "headers": {"Authorization": "Bearer x"}}}}`)
	put(t, filepath.Join(w, ".github", "agents", "planner.agent.md"), "---\ndescription: Plans work\n---\nPlan.\n")
	put(t, filepath.Join(w, "AGENTS.md"), "# Project rules\nKeep it small.\n")
	return BrowseOptions{Home: h, Workspace: w, LookPath: noLook}, h, w
}

func findItem(t *testing.T, a AgentSetup, kind, name string) Item {
	t.Helper()
	for _, l := range lists(&a) {
		for _, i := range *l {
			if i.Kind == kind && i.Name == name {
				return i
			}
		}
	}
	t.Fatalf("%s %s %s missing", a.Agent, kind, name)
	return Item{}
}

func TestBrowseListsEveryHarnessWithStatus(t *testing.T) {
	o, _, _ := browseFixture(t)
	inv := Browse(o)
	if len(inv.Agents) != len(harnesses) {
		t.Fatalf("agents = %d", len(inv.Agents))
	}
	claude := agent(t, inv, "claude")
	if !claude.Installed || !claude.NexusMCP {
		t.Fatalf("claude: %+v", claude)
	}
	if d := findItem(t, claude, "subagent", "reviewer").Description; d != "Reviews diffs" {
		t.Fatalf("frontmatter description = %q", d)
	}
	if s := findItem(t, claude, "skill", "deploy").SharedWith; len(s) != 1 || s[0] != "codex" {
		t.Fatalf("shared_with = %v", s)
	}
	gh := findItem(t, claude, "mcp", "github")
	if strings.Join(gh.EnvKeys, ",") != "GITHUB_TOKEN,LOG" || gh.Transport != "stdio" {
		t.Fatalf("github: %+v", gh)
	}
	if nx := findItem(t, claude, "mcp", "nexus"); strings.Join(nx.HeaderKeys, ",") != "Authorization" {
		t.Fatalf("header keys: %+v", nx)
	}
	ws := agent(t, inv, "windsurf")
	if !ws.Installed || ws.NexusMCP {
		t.Fatalf("windsurf: %+v", ws)
	}
	if r := findItem(t, ws, "mcp", "remote"); r.Transport != "http" || r.URL != "https://ws.example/mcp" {
		t.Fatalf("windsurf url: %+v", r)
	}
	findItem(t, ws, "rules", "global_rules.md")
	if k := agent(t, inv, "kimi"); !k.NexusMCP || findItem(t, k, "mcp", "nexus").Transport != "http" {
		t.Fatalf("kimi: %+v", k)
	}
	if !strings.Contains(strings.Join(findItem(t, agent(t, inv, "kimi"), "rules", "AGENTS.md").SharedWith, ","), "codex") {
		t.Fatal("project AGENTS.md should be shared with codex")
	}
	findItem(t, agent(t, inv, "copilot"), "subagent", "planner")
	if a := agent(t, inv, "antigravity"); a.Installed {
		t.Fatal("antigravity is not installed in the fixture")
	}
	noSecrets(t, inv)
}

func TestReadItemMasksSecretsAndRefusesUnlisted(t *testing.T) {
	o, h, _ := browseFixture(t)
	claudeJSON := filepath.Join(h, ".claude.json")
	for _, name := range []string{"nexus", "github"} {
		c, err := ReadItem(o, "claude", "mcp", name, claudeJSON)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(c.Content, plantedSecret) || strings.Contains(c.Content, "numStartups") || c.Masked == 0 || c.Format != "json" {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	gh, _ := ReadItem(o, "claude", "mcp", "github", claudeJSON)
	if !strings.Contains(gh.Content, "GITHUB_TOKEN") || !strings.Contains(gh.Content, `"gh-mcp"`) {
		t.Fatalf("names and plain args must stay visible: %s", gh.Content)
	}
	c, err := ReadItem(o, "codex", "mcp", "nexus", filepath.Join(h, ".codex", "config.toml"))
	if err != nil || strings.Contains(c.Content, plantedSecret) || !strings.Contains(c.Content, "Authorization") {
		t.Fatalf("codex: %v %+v", err, c)
	}
	c, err = ReadItem(o, "windsurf", "mcp", "remote", filepath.Join(h, ".codeium", "windsurf", "mcp_config.json"))
	if err != nil || strings.Contains(c.Content, plantedSecret) {
		t.Fatalf("windsurf: %v %+v", err, c)
	}
	skill, err := ReadItem(o, "claude", "skill", "deploy", filepath.Join(h, ".claude", "skills", "deploy", "SKILL.md"))
	if err != nil || skill.Format != "markdown" || !strings.Contains(skill.Content, "description: Ships it") || strings.Contains(skill.Content, plantedSecret) {
		t.Fatalf("skill: %v %+v", err, skill)
	}
	// Paths Browse did not return are refused, even inside home.
	put(t, filepath.Join(h, ".ssh", "id_rsa"), "PRIVATE")
	if _, err := ReadItem(o, "claude", "rules", "id_rsa", filepath.Join(h, ".ssh", "id_rsa")); err == nil {
		t.Fatal("read an unlisted file")
	}
	if _, err := ReadItem(o, "claude", "skill", "deploy", claudeJSON); err == nil {
		t.Fatal("kind/path mismatch accepted")
	}
}

func TestMaskText(t *testing.T) {
	n := 0
	in := "Authorization: Bearer abcdefgh12345678\nOPENAI_API_KEY=sk-abcdefghijklmnopqrstu\nghp_abcdefghijklmnopqrstuvwxyz12\npassword: required\nhttps://me:pw123@host/x?token=abc123&page=2\n"
	out := maskText(in, &n)
	for _, leak := range []string{"abcdefgh12345678", "sk-abcdef", "ghp_abc", "pw123", "abc123"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "password: required") || !strings.Contains(out, "page=2") || n < 5 {
		t.Fatalf("over-masked or miscounted (%d):\n%s", n, out)
	}
}

func TestRemoveMCPKeepsEverythingElse(t *testing.T) {
	o, h, _ := browseFixture(t)
	claudeJSON := filepath.Join(h, ".claude.json")
	before := string(readTest(t, claudeJSON))
	dry, err := RemoveMCP(o, "claude", "github", claudeJSON, true)
	if err != nil || dry.Action != "will remove" || string(readTest(t, claudeJSON)) != before {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	res, err := RemoveMCP(o, "claude", "github", claudeJSON, false)
	if err != nil || res.Action != "removed" || res.Nexus {
		t.Fatalf("remove: %v %+v", err, res)
	}
	if string(readTest(t, res.Backup)) != before {
		t.Fatal("backup differs from the original")
	}
	claude := agent(t, Browse(o), "claude")
	if len(claude.MCPServers) != 1 || claude.MCPServers[0].Name != "nexus" {
		t.Fatalf("after: %+v", claude.MCPServers)
	}
	if !strings.Contains(string(readTest(t, claudeJSON)), `"numStartups": 3`) {
		t.Fatal("other settings lost")
	}

	codexToml := filepath.Join(h, ".codex", "config.toml")
	res, err = RemoveMCP(o, "codex", "nexus", codexToml, false)
	if err != nil || !res.Nexus {
		t.Fatalf("codex: %v %+v", err, res)
	}
	var m map[string]any
	if _, err := toml.Decode(string(readTest(t, codexToml)), &m); err != nil {
		t.Fatal(err)
	}
	servers := object(m["mcp_servers"])
	if servers["nexus"] != nil || servers["other"] == nil || m["model"] != "o3" {
		t.Fatalf("codex config after removal: %v", m)
	}

	if _, err := RemoveMCP(o, "claude", "missing", claudeJSON, false); err == nil {
		t.Fatal("removed an unlisted server")
	}
	if _, err := os.Stat(claudeJSON); err != nil {
		t.Fatal(err)
	}
}

func TestBrowseListsClaudePluginSubagents(t *testing.T) {
	o, h, _ := browseFixture(t)
	dir := filepath.Join(h, ".claude", "plugins", "cache", "market", "toolkit", "1.0.0")
	put(t, filepath.Join(dir, "agents", "architect.md"), "---\nname: architect\ndescription: Designs systems\n---\nThink.\n")
	put(t, filepath.Join(dir, "skills", "ship", "SKILL.md"), "---\nname: ship\ndescription: Ships\n---\nGo.\n")
	other := filepath.Join(h, ".claude", "plugins", "cache", "market", "elsewhere", "1.0.0")
	put(t, filepath.Join(other, "agents", "hidden.md"), "---\nname: hidden\n---\nNo.\n")
	manifest := map[string]any{"version": 2, "plugins": map[string]any{
		"toolkit@market":   []any{map[string]any{"scope": "user", "installPath": dir}},
		"elsewhere@market": []any{map[string]any{"scope": "project", "projectPath": filepath.Join(h, "other-project"), "installPath": other}},
	}}
	b, _ := json.Marshal(manifest)
	put(t, filepath.Join(h, ".claude", "plugins", "installed_plugins.json"), string(b))

	claude := agent(t, Browse(o), "claude")
	sub := findItem(t, claude, "subagent", "toolkit:architect")
	if sub.Scope != "plugin" || sub.Description != "Designs systems" {
		t.Fatalf("plugin sub-agent = %+v", sub)
	}
	findItem(t, claude, "skill", "toolkit:ship")
	for _, s := range claude.SubAgents {
		if strings.Contains(s.Name, "hidden") {
			t.Fatalf("project plugin from another folder listed: %+v", s)
		}
	}
	if c, err := ReadItem(o, "claude", "subagent", sub.Name, sub.Path); err != nil || !strings.Contains(c.Content, "Think.") {
		t.Fatalf("read plugin sub-agent: %v %+v", err, c)
	}
}
