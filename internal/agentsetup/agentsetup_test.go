package agentsetup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const plantedSecret = "nexus-test-secret-938427"

func fixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	h := filepath.Join(root, "home")
	w := filepath.Join(root, "work")
	for _, p := range []string{h, w} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	return h, w
}
func put(t *testing.T, p, s string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func readTest(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func agent(t *testing.T, inv Inventory, id string) AgentSetup {
	t.Helper()
	for _, a := range inv.Agents {
		if a.Agent == id {
			return a
		}
	}
	t.Fatal("agent missing", id)
	return AgentSetup{}
}
func noSecrets(t *testing.T, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte(plantedSecret)) {
		t.Fatal("secret leaked in public output")
	}
}
func shareTest(t *testing.T, h, w, from, to, kind string, item Item, scope string, dry bool) ShareResult {
	t.Helper()
	res, e := Share(ShareOptions{Home: h, Workspace: w, From: from, To: []string{to}, Item: item, Scope: scope, DryRun: dry})
	if e != nil {
		t.Fatal(e)
	}
	noSecrets(t, res)
	return res
}
func TestDiscovery(t *testing.T) {
	h, w := fixture(t)
	fixtures := map[string]string{
		"home/.claude/agents/reviewer.md":     "---\nname: review\ndescription: Code review\ntools: [Read, Bash]\nmodel: fast\n---\nReview body",
		"work/.claude/agents/project.md":      "Project agent",
		"home/.claude/skills/design/SKILL.md": "---\nname: design\ndescription: Design things\n---\nBody",
		"work/.claude/skills/local/SKILL.md":  "Local skill",
		"home/.claude/commands/team/check.md": "---\ndescription: Check things\n---\nCheck body",
		"work/.claude/commands/run.md":        "Run command",
		"home/.claude/CLAUDE.md":              "Global Claude rules", "work/CLAUDE.md": "Project Claude rules", "work/.claude/CLAUDE.md": "Additional rules",
		"work/.mcp.json":                `{"mcpServers":{"project":{"command":"node"}}}`,
		"home/.codex/config.toml":       "model = \"x\"\n[mcp_servers.node]\ncommand = \"node\"\nargs = [\"server\"]\n[mcp_servers.node.env]\nTOKEN = \"" + plantedSecret + "\"\n[mcp_servers.remote]\nurl = \"https://example.com/mcp\"\n",
		"home/.codex/prompts/review.md": "Review prompt", "home/.codex/skills/code/SKILL.md": "Code skill", "home/.codex/AGENTS.md": "User Codex rules", "work/AGENTS.md": "Shared project rules",
		"home/.cursor/mcp.json":        `{"mcpServers":{"http":{"url":"https://example.com/mcp?token=` + plantedSecret + `","headers":{"Authorization":"` + plantedSecret + `"}}}}`,
		"work/.cursor/mcp.json":        `{"mcpServers":{"sse":{"type":"sse","url":"https://example.com/sse"}}}`,
		"home/.cursor/commands/run.md": "Cursor global", "work/.cursor/commands/run.md": "Cursor project", "work/.cursorrules": "Legacy rules", "work/.cursor/rules/check.mdc": "---\ndescription: Check code\n---\nBody", "work/.cursor/rules/other.md": "Other rules",
		"home/.config/opencode/opencode.jsonc": "{ // a comment\n\"mcp\":{\"local\":{\"type\":\"local\",\"command\":[\"node\",\"serve\"],\"environment\":{\"SECRET\":\"" + plantedSecret + "\"}},\"remote\":{\"type\":\"remote\",\"url\":\"https://example.com/a//b\"}},\"agent\":{\"inline\":{\"description\":\"Inline agent\"}}}",
		"home/.config/opencode/agent/old.md":   "Old agent", "home/.config/opencode/agents/new.md": "New agent", "work/.opencode/agent/project.md": "Project agent", "work/opencode.json": `{"mcp":{"workspace":{"type":"remote","url":"https://example.com"}},"agent":{"project-inline":{"description":"Project inline"}}}`,
		"home/.config/opencode/command/build.md": "Build prompt", "home/.config/opencode/commands/check.md": "Check prompt", "work/.opencode/command/project.md": "Project prompt",
		"home/.gemini/settings.json":       `{"mcpServers":{"stdio":{"command":"python","args":["server"],"env":{"TOKEN":"` + plantedSecret + `"}},"http":{"httpUrl":"https://example.com/mcp"}}}`,
		"work/.gemini/settings.json":       `{"mcpServers":{"project":{"url":"https://example.com/sse"}}}`,
		"home/.gemini/commands/check.toml": "description = \"Check things\"\nprompt = \"Check code\"\n", "home/.gemini/GEMINI.md": "Global Gemini", "work/GEMINI.md": "Project Gemini",
	}
	for p, s := range fixtures {
		parts := strings.SplitN(p, "/", 2)
		base := h
		if parts[0] == "work" {
			base = w
		}
		put(t, filepath.Join(base, filepath.FromSlash(parts[1])), s)
	}
	projects, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"user": map[string]any{"command": "node", "env": map[string]string{"SECRET": plantedSecret}}}, "projects": map[string]any{w: map[string]any{"mcpServers": map[string]any{"local": map[string]any{"command": "local"}}}}})
	put(t, filepath.Join(h, ".claude.json"), string(projects))
	inv := Scan(ScanOptions{Home: h, Workspace: w})
	noSecrets(t, inv)
	for _, tt := range []struct {
		id                                string
		sub, skills, commands, mcp, rules int
	}{{"claude", 2, 2, 2, 3, 3}, {"codex", 0, 1, 1, 2, 2}, {"cursor", 0, 0, 2, 2, 3}, {"opencode", 5, 0, 3, 3, 1}, {"gemini", 0, 0, 1, 3, 2}} {
		t.Run(tt.id, func(t *testing.T) {
			a := agent(t, inv, tt.id)
			if !a.Installed || len(a.SubAgents) != tt.sub || len(a.Skills) != tt.skills || len(a.Commands) != tt.commands || len(a.MCPServers) != tt.mcp || len(a.Rules) != tt.rules {
				t.Fatalf("unexpected counts: installed=%v sub=%d skills=%d commands=%d mcp=%d rules=%d", a.Installed, len(a.SubAgents), len(a.Skills), len(a.Commands), len(a.MCPServers), len(a.Rules))
			}
		})
	}
	a := agent(t, inv, "opencode")
	if a.MCPServers[0].Command != "node" || a.MCPServers[0].Transport != "stdio" {
		t.Fatal("local MCP normalization")
	}
	userOnly := Scan(ScanOptions{Home: h})
	for _, a := range userOnly.Agents {
		for _, items := range [][]Item{a.SubAgents, a.Skills, a.Commands, a.MCPServers, a.Rules} {
			for _, i := range items {
				if i.Scope != "user" {
					t.Fatal("project leaked into user-only scan")
				}
			}
		}
	}
}
func TestMalformedAndBounded(t *testing.T) {
	for _, tt := range []struct{ p, s string }{{".claude.json", "{"}, {".cursor/mcp.json", `{"mcpServers":`}, {".codex/config.toml", "[mcp_servers.bad\ncommand = ["}, {".claude/agents/bad.md", "---\nname: [\n---\nbody"}, {".gemini/commands/bad.toml", "prompt = ["}, {".claude/commands/huge.md", strings.Repeat("x", maxRead+1)}} {
		t.Run(tt.p, func(t *testing.T) {
			h, _ := fixture(t)
			put(t, filepath.Join(h, tt.p), tt.s)
			a := Scan(ScanOptions{Home: h})
			for _, x := range a.Agents {
				if len(x.MCPServers)+len(x.SubAgents)+len(x.Commands) > 0 {
					t.Fatal("malformed file inventoried")
				}
			}
		})
	}
}
func TestMCPShareTargets(t *testing.T) {
	for _, to := range []string{"claude", "codex", "cursor", "opencode", "gemini"} {
		t.Run(to, func(t *testing.T) {
			h, w := fixture(t)
			put(t, filepath.Join(h, ".cursor/mcp.json"), `{"mcpServers":{"shared":{"command":"node","args":["serve"],"env":{"API_KEY":"`+plantedSecret+`"},"headers":{"Authorization":"`+plantedSecret+`"}}}}`)
			source := agent(t, Scan(ScanOptions{Home: h}), "cursor").MCPServers[0]
			// Cursor uses a project target so source and destination differ.
			scope := "user"
			if to == "cursor" {
				scope = "project"
			}
			r := resolve(ScanOptions{Home: h, Workspace: w})
			p, _ := targetPath(r, to, "mcp", scope, "shared")
			if to == "codex" {
				put(t, p, "# preserve this comment\nmodel = \"existing\"\n[mcp_servers.other]\ncommand = \"other\"\n")
			} else {
				key := "mcpServers"
				if to == "opencode" {
					key = "mcp"
				}
				put(t, p, `{"unrelated":{"keep":true,"large":9007199254740993},"`+key+`":{"other":{"command":"other"}}}`)
			}
			before := readTest(t, p)
			dry := shareTest(t, h, w, "cursor", to, "mcp", source, scope, true)
			if len(dry.Writes) != 1 || dry.Writes[0].Action != "update" || !bytes.Equal(before, readTest(t, p)) {
				t.Fatal("dry run mutated target or invalid plan")
			}
			if matches, _ := filepath.Glob(p + ".nexus-backup-*"); len(matches) != 0 {
				t.Fatal("dry run backup")
			}
			res := shareTest(t, h, w, "cursor", to, "mcp", source, scope, false)
			if len(res.Writes) != 1 {
				t.Fatalf("writes=%v skipped=%v", res.Writes, res.Skipped)
			}
			if !bytes.Equal(readTest(t, res.Writes[0].Backup), before) {
				t.Fatal("backup differs")
			}
			m, e := r.config(p)
			if e != nil {
				t.Fatal(e)
			}
			if to == "codex" {
				if str(m["model"]) != "existing" || !bytes.Contains(readTest(t, p), []byte("# preserve this comment")) {
					t.Fatal("TOML content lost")
				}
			} else {
				if object(m["unrelated"])["keep"] != true {
					t.Fatal("unrelated config lost")
				}
				if !bytes.Contains(readTest(t, p), []byte("9007199254740993")) {
					t.Fatal("JSON number precision lost")
				}
			}
			servers := mcpMap(m, to, scope, "")
			if object(servers["other"]) == nil {
				t.Fatal("other server lost")
			}
			n := normalized(object(servers["shared"]), to)
			if str(object(n["env"])["API_KEY"]) != plantedSecret || str(object(n["headers"])["Authorization"]) != plantedSecret {
				t.Fatal("credentials not copied")
			}
			stable := readTest(t, p)
			res = shareTest(t, h, w, "cursor", to, "mcp", source, scope, false)
			if len(res.Writes) != 0 || !bytes.Equal(stable, readTest(t, p)) {
				t.Fatal("MCP re-share not idempotent")
			}
		})
	}
}
func TestTOMLTableReplacement(t *testing.T) {
	old := []byte("# start\nmodel = 'keep'\n[mcp_servers.'a.b']\ncommand='old'\n[mcp_servers.'a.b'.env]\nX='old'\n[other]\nflag=true\n[mcp_servers.sibling]\nurl='https://example.com'\n")
	raw := map[string]any{"command": "new", "env": map[string]any{"X": plantedSecret}}
	b, e := replaceTable(old, "a.b", raw)
	if e != nil {
		t.Fatal(e)
	}
	b2, e := replaceTable(b, "a.b", raw)
	if e != nil || !bytes.Equal(b, b2) {
		t.Fatal("table replacement not idempotent")
	}
	var m map[string]any
	if _, e := toml.Decode(string(b), &m); e != nil || object(m["other"])["flag"] != true || object(object(m["mcp_servers"])["sibling"]) == nil {
		t.Fatal("table replacement damaged siblings")
	}
}
func TestSkillsAndConflicts(t *testing.T) {
	h, w := fixture(t)
	src := filepath.Join(h, ".claude/skills/design/SKILL.md")
	put(t, src, "---\nname: design\n---\nDesign skill")
	put(t, filepath.Join(filepath.Dir(src), "assets/icon.txt"), "asset")
	item := agent(t, Scan(ScanOptions{Home: h}), "claude").Skills[0]
	dry := shareTest(t, h, w, "claude", "codex", "skill", item, "user", true)
	if len(dry.Writes) != 2 {
		t.Fatal("skill dry plan")
	}
	if _, e := os.Stat(filepath.Join(h, ".codex")); !os.IsNotExist(e) {
		t.Fatal("dry run created directories")
	}
	res := shareTest(t, h, w, "claude", "codex", "skill", item, "user", false)
	if len(res.Writes) != 2 {
		t.Fatal(res)
	}
	if string(readTest(t, filepath.Join(h, ".codex/skills/design/assets/icon.txt"))) != "asset" {
		t.Fatal("asset lost")
	}
	res = shareTest(t, h, w, "claude", "codex", "skill", item, "user", false)
	if len(res.Writes) != 0 {
		t.Fatal("identical skill copied")
	}
	put(t, filepath.Join(h, ".codex/skills/design/assets/icon.txt"), "different")
	res = shareTest(t, h, w, "claude", "codex", "skill", item, "user", false)
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "a different skill named design") {
		t.Fatal("skill conflict not refused")
	}
}
func TestSubagentMapping(t *testing.T) {
	for _, from := range []string{"claude", "opencode"} {
		t.Run(from, func(t *testing.T) {
			h, w := fixture(t)
			to := "opencode"
			path := filepath.Join(h, ".claude/agents/review.md")
			tools := "tools: [Read, Bash]"
			if from == "opencode" {
				to = "claude"
				path = filepath.Join(h, ".config/opencode/agent/review.md")
				tools = "tools:\n  Read: true\n  Bash: true\n  Write: false"
			}
			put(t, path, "---\nname: review\ndescription: Review code\nmodel: fast\nunknown: discard\n"+tools+"\n---\nBody stays.\n")
			i := agent(t, Scan(ScanOptions{Home: h}), from).SubAgents[0]
			res := shareTest(t, h, w, from, to, "subagent", i, "user", false)
			if len(res.Writes) != 1 {
				t.Fatal(res)
			}
			m, body, e := markdown(readTest(t, res.Writes[0].Path))
			if e != nil || body != "Body stays.\n" || m["unknown"] != nil || str(m["model"]) != "fast" {
				t.Fatal("mapping lost body or fields")
			}
			if to == "opencode" {
				if object(m["tools"])["Read"] != true {
					t.Fatal("missing tool map")
				}
			} else {
				if len(stringsOf(m["tools"])) != 2 {
					t.Fatal("missing tool list")
				}
			}
		})
	}
}
func TestRulesSharing(t *testing.T) {
	h, w := fixture(t)
	put(t, filepath.Join(w, "CLAUDE.md"), "# Source\n\n## Body heading\nSource instructions\n")
	put(t, filepath.Join(w, "AGENTS.md"), "# Existing\nKeep this\n")
	i := agent(t, Scan(ScanOptions{Home: h, Workspace: w}), "claude").Rules[0]
	res := shareTest(t, h, w, "claude", "codex", "rules", i, "project", false)
	p := res.Writes[0].Path
	b := readTest(t, p)
	if !bytes.Contains(b, []byte("Keep this")) || !bytes.Contains(b, []byte("## Shared from claude (Nexus)")) {
		t.Fatal("rules merge")
	}
	res = shareTest(t, h, w, "claude", "codex", "rules", i, "project", false)
	if len(res.Writes) != 0 || !bytes.Equal(b, readTest(t, p)) {
		t.Fatal("rules re-share not idempotent")
	}
	put(t, i.Path, "Changed source\n")
	res = shareTest(t, h, w, "claude", "codex", "rules", i, "project", false)
	b = readTest(t, p)
	if len(res.Writes) != 1 || bytes.Count(b, []byte("## Shared from claude (Nexus)")) != 1 || bytes.Contains(b, []byte("Source instructions")) {
		t.Fatal("rules replacement failed")
	}
}
func TestCommandConversions(t *testing.T) {
	for _, from := range []string{"claude", "codex", "cursor", "opencode"} {
		for _, to := range []string{"claude", "codex", "cursor", "opencode", "gemini"} {
			if from == to {
				continue
			}
			t.Run(from+"-"+to, func(t *testing.T) {
				h, w := fixture(t)
				r := resolve(ScanOptions{Home: h, Workspace: w})
				p, e := targetPath(r, from, "command", "user", "check")
				if e != nil {
					t.Fatal(e)
				}
				put(t, p, "---\ndescription: Check code\n---\nCheck $ARGUMENTS\n")
				i := agent(t, Scan(ScanOptions{Home: h}), from).Commands[0]
				res := shareTest(t, h, w, from, to, "command", i, "user", false)
				if len(res.Writes) != 1 {
					t.Fatal(res)
				}
				if to == "gemini" {
					m, e := r.config(res.Writes[0].Path)
					if e != nil || str(m["description"]) != "Check code" || str(m["prompt"]) != "Check $ARGUMENTS\n" {
						t.Fatal("Gemini TOML command")
					}
				} else {
					if !bytes.Equal(readTest(t, p), readTest(t, res.Writes[0].Path)) {
						t.Fatal("markdown changed")
					}
					put(t, res.Writes[0].Path, "Different")
					res = shareTest(t, h, w, from, to, "command", i, "user", false)
					if len(res.Skipped) != 1 || len(res.Writes) != 0 {
						t.Fatal("command conflict")
					}
				}
			})
		}
	}
}
func TestPathSafety(t *testing.T) {
	h, w := fixture(t)
	outside := t.TempDir()
	put(t, filepath.Join(outside, "leak.md"), "outside")
	put(t, filepath.Join(h, ".claude/commands/good.md"), "Good")
	i := agent(t, Scan(ScanOptions{Home: h}), "claude").Commands[0]
	fake := i
	fake.Path = filepath.Join(outside, "leak.md")
	if _, e := Share(ShareOptions{Home: h, Workspace: w, From: "claude", To: []string{"cursor"}, Item: fake, Scope: "user"}); e == nil {
		t.Fatal("forged source accepted")
	}
	if e := os.Symlink(outside, filepath.Join(h, ".cursor")); e != nil {
		t.Log("symlink test unavailable:", e)
		return
	}
	inv := Scan(ScanOptions{Home: h})
	if agent(t, inv, "cursor").Installed {
		t.Fatal("external symlink treated as installed")
	}
	if _, e := Share(ShareOptions{Home: h, Workspace: w, From: "claude", To: []string{"cursor"}, Item: i, Scope: "user"}); e == nil {
		t.Fatal("external symlink write accepted")
	}
}
func TestMCPConflict(t *testing.T) {
	h, w := fixture(t)
	put(t, filepath.Join(h, ".claude.json"), `{"mcpServers":{"same":{"command":"one","env":{"KEY":"`+plantedSecret+`"}}}}`)
	put(t, filepath.Join(h, ".cursor/mcp.json"), `{"mcpServers":{"same":{"command":"two"}}}`)
	i := agent(t, Scan(ScanOptions{Home: h}), "claude").MCPServers[0]
	res := shareTest(t, h, w, "claude", "cursor", "mcp", i, "user", false)
	if len(res.Skipped) != 1 || len(res.Writes) != 0 {
		t.Fatal("MCP conflict")
	}
}

func TestBOMAndNestedCommand(t *testing.T) {
	h, w := fixture(t)
	put(t, filepath.Join(h, ".cursor/mcp.json"), "\xef\xbb\xbf"+`{"mcpServers":{"remote":{"url":"https://example.com","headers":{"Authorization":"`+plantedSecret+`"}}}}`)
	a := agent(t, Scan(ScanOptions{Home: h}), "cursor")
	if len(a.MCPServers) != 1 {
		t.Fatal("Windows UTF-8 BOM not handled")
	}
	noSecrets(t, a)
	put(t, filepath.Join(h, ".claude/commands/team/check.md"), "Check team")
	i := agent(t, Scan(ScanOptions{Home: h}), "claude").Commands[0]
	res := shareTest(t, h, w, "claude", "opencode", "command", i, "user", false)
	if len(res.Writes) != 1 || !strings.HasSuffix(filepath.ToSlash(res.Writes[0].Path), "command/team/check.md") {
		t.Fatal("nested command not copied", res)
	}
}

func TestMCPSourceFormats(t *testing.T) {
	for _, tt := range []struct{ from, p, s string }{
		{"opencode", ".config/opencode/opencode.json", `{"mcp":{"remote":{"type":"remote","url":"https://example.com","headers":{"Authorization":"` + plantedSecret + `"}}}}`},
		{"gemini", ".gemini/settings.json", `{"mcpServers":{"remote":{"httpUrl":"https://example.com","headers":{"Authorization":"` + plantedSecret + `"}}}}`},
		{"codex", ".codex/config.toml", "[mcp_servers.remote]\nurl = 'https://example.com'\n[mcp_servers.remote.http_headers]\nAuthorization = '" + plantedSecret + "'\n"},
	} {
		t.Run(tt.from, func(t *testing.T) {
			h, w := fixture(t)
			put(t, filepath.Join(h, filepath.FromSlash(tt.p)), tt.s)
			inv := Scan(ScanOptions{Home: h})
			noSecrets(t, inv)
			i := agent(t, inv, tt.from).MCPServers[0]
			res := shareTest(t, h, w, tt.from, "claude", "mcp", i, "user", false)
			if len(res.Writes) != 1 {
				t.Fatal(res)
			}
			m, e := resolve(ScanOptions{Home: h}).config(res.Writes[0].Path)
			if e != nil {
				t.Fatal(e)
			}
			server := object(object(m["mcpServers"])["remote"])
			if str(server["url"]) != "https://example.com" || str(object(server["headers"])["Authorization"]) != plantedSecret {
				t.Fatal("remote config translation lost credentials")
			}
		})
	}
}

func TestInvalidTargetUnchanged(t *testing.T) {
	for _, to := range []string{"claude", "codex", "cursor", "opencode", "gemini"} {
		t.Run(to, func(t *testing.T) {
			h, w := fixture(t)
			put(t, filepath.Join(h, ".cursor/mcp.json"), `{"mcpServers":{"server":{"command":"node"}}}`)
			i := agent(t, Scan(ScanOptions{Home: h}), "cursor").MCPServers[0]
			r := resolve(ScanOptions{Home: h, Workspace: w})
			scope := "user"
			if to == "cursor" {
				scope = "project"
			}
			p, _ := targetPath(r, to, "mcp", scope, "server")
			put(t, p, "malformed [[{")
			before := readTest(t, p)
			res, e := Share(ShareOptions{Home: h, Workspace: w, From: "cursor", To: []string{to}, Item: i, Scope: scope})
			if e == nil || len(res.Writes) != 0 || !bytes.Equal(before, readTest(t, p)) {
				t.Fatal("malformed target modified")
			}
			backups, _ := filepath.Glob(p + ".nexus-backup-*")
			if len(backups) != 0 {
				t.Fatal("malformed target backed up")
			}
		})
	}
}
