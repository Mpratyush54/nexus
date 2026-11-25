package agentsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A settings.json as people really keep it: comments (Gemini CLI reads
// settings through strip-json-comments), other MCP servers, nested keys,
// a trailing comma, and a key order that isn't alphabetical.
const realisticGeminiSettings = `{
  // Gemini CLI settings
  "theme": "GitHub",
  "security": {
    "auth": { "selectedType": "oauth-personal" } /* chosen on first run */
  },
  "mcpServers": {
    "clickup": {
      "httpUrl": "https://mcp.example.com/clickup",
      "headers": { "Authorization": "Bearer other-service-token" }
    },
    // local tools
    "node_repl": { "command": "node", "args": ["repl.js"], "timeout": 30000 },
  },
  "general": { "vimMode": true }
}
`

func geminiHome(t *testing.T, settings string) string {
	t.Helper()
	h, _ := fixture(t)
	put(t, filepath.Join(h, ".gemini", "settings.json"), settings)
	return h
}

func geminiOnly(reps []HarnessReport) HarnessReport {
	for _, r := range reps {
		if r.Harness == "gemini" {
			return r
		}
	}
	return HarnessReport{}
}

func TestGeminiInstallKeepsCommentsOrderAndOtherServers(t *testing.T) {
	h := geminiHome(t, realisticGeminiSettings)
	path := filepath.Join(h, ".gemini", "settings.json")
	m := &minter{}
	opts := InstallOptions{Home: h, ServerURL: testServer, LookPath: noLook, Token: m.mint}

	// Dry run: the preview says "added" and nothing is written.
	opts.DryRun = true
	reps, err := InstallMCP(opts)
	if err != nil {
		t.Fatal(err)
	}
	if r := geminiOnly(reps); r.Action != "added" || r.Reason != "" {
		t.Fatalf("preview: %+v", r)
	}
	if b, _ := os.ReadFile(path); string(b) != realisticGeminiSettings {
		t.Fatal("dry run changed settings.json")
	}

	opts.DryRun = false
	reps, err = InstallMCP(opts)
	if err != nil {
		t.Fatal(err)
	}
	if r := geminiOnly(reps); r.Action != "added" || len(r.Backups) != 1 {
		t.Fatalf("install: %+v", r)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	for _, keep := range []string{"// Gemini CLI settings", "/* chosen on first run */", "// local tools", `"theme": "GitHub"`, `"general": { "vimMode": true }`, "other-service-token", `"timeout": 30000`} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q:\n%s", keep, got)
		}
	}
	if strings.Index(got, `"theme"`) > strings.Index(got, `"security"`) || strings.Index(got, `"mcpServers"`) > strings.Index(got, `"general"`) {
		t.Fatalf("key order changed:\n%s", got)
	}
	entry := nexusEntryAt(t, h, "gemini")
	if entry["httpUrl"] != testServer+"/v1/agent/mcp" || object(entry["headers"])["Authorization"] != "Bearer nxs_gemini_token" {
		t.Fatalf("entry %+v", entry)
	}
	// The file still parses the way Gemini CLI reads it.
	doc, err := decodeJSONC(b)
	if err != nil || len(object(doc["mcpServers"])) != 3 {
		t.Fatalf("doc %v err %v", doc, err)
	}

	// Reinstalling changes nothing and mints nothing.
	reps, _ = InstallMCP(opts)
	if r := geminiOnly(reps); r.Action != "unchanged" || len(m.calls) != 1 {
		t.Fatalf("reinstall: %+v calls %v", r, m.calls)
	}

	// Uninstall removes only Nexus; every comment and setting stays. (The
	// one difference allowed is the optional trailing comma.)
	if _, err := UninstallMCP(InstallOptions{Home: h, LookPath: noLook}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Replace(realisticGeminiSettings, "30000 },\n", "30000 }\n", 1) != string(after) {
		t.Fatalf("uninstall did not restore the original:\n%s", after)
	}
	before, _ := decodeJSONC([]byte(realisticGeminiSettings))
	restored, _ := decodeJSONC(after)
	if !same(before, restored) {
		t.Fatalf("settings differ after uninstall:\n%s", after)
	}
}

func TestGeminiInstallAddsServersSectionWhenMissing(t *testing.T) {
	const settings = "{\n  // only a theme\n  \"theme\": \"dark\"\n}\n"
	h := geminiHome(t, settings)
	m := &minter{}
	reps, err := InstallMCP(InstallOptions{Home: h, ServerURL: testServer, LookPath: noLook, Token: m.mint})
	if err != nil {
		t.Fatal(err)
	}
	if r := geminiOnly(reps); r.Action != "added" {
		t.Fatalf("%+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(h, ".gemini", "settings.json"))
	if !strings.Contains(string(b), "// only a theme") || nexusEntryAt(t, h, "gemini")["httpUrl"] == nil {
		t.Fatalf("got:\n%s", b)
	}
}

func TestGeminiUpdateReplacesOnlyTheNexusEntry(t *testing.T) {
	settings := strings.Replace(realisticGeminiSettings, `"mcpServers": {`,
		`"mcpServers": {
    "nexus": { "httpUrl": "https://old.example/v1/agent/mcp", "headers": { "Authorization": "Bearer nxs_old" }, "trust": true },`, 1)
	h := geminiHome(t, settings)
	m := &minter{}
	reps, err := InstallMCP(InstallOptions{Home: h, ServerURL: testServer, LookPath: noLook, Token: m.mint})
	if err != nil {
		t.Fatal(err)
	}
	if r := geminiOnly(reps); r.Action != "updated" {
		t.Fatalf("%+v", r)
	}
	entry := nexusEntryAt(t, h, "gemini")
	if entry["trust"] != true || entry["httpUrl"] != testServer+"/v1/agent/mcp" {
		t.Fatalf("entry %+v", entry)
	}
	b, _ := os.ReadFile(filepath.Join(h, ".gemini", "settings.json"))
	if !strings.Contains(string(b), "// local tools") || !strings.Contains(string(b), "other-service-token") {
		t.Fatalf("got:\n%s", b)
	}
}

func TestClaudeJSONKeyOrderIsKept(t *testing.T) {
	h, _ := fixture(t)
	os.MkdirAll(filepath.Join(h, ".claude"), 0o700)
	const claude = `{"numStartups": 9007199254740993, "zeta": 1, "mcpServers": {"other": {"command": "x"}}, "alpha": 2}`
	put(t, filepath.Join(h, ".claude.json"), claude)
	m := &minter{}
	if _, err := InstallMCP(InstallOptions{Home: h, ServerURL: testServer, LookPath: noLook, Token: m.mint}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(h, ".claude.json"))
	s := string(b)
	if !strings.HasPrefix(s, `{"numStartups": 9007199254740993, "zeta": 1, "mcpServers": {"other": {"command": "x"},`) || !strings.HasSuffix(strings.TrimSpace(s), `"alpha": 2}`) {
		t.Fatalf("got:\n%s", s)
	}
}

func TestScanReadsCommentedGeminiSettings(t *testing.T) {
	h := geminiHome(t, realisticGeminiSettings)
	inv := Scan(ScanOptions{Home: h})
	for _, a := range inv.Agents {
		if a.Agent != "gemini" {
			continue
		}
		names := map[string]bool{}
		for _, s := range a.MCPServers {
			names[s.Name] = true
		}
		if !names["clickup"] || !names["node_repl"] {
			t.Fatalf("servers %+v", a.MCPServers)
		}
		return
	}
	t.Fatal("no gemini")
}
