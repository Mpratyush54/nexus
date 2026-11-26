package agentsetup

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// Every test here runs against a temporary home and project folder; none
// reads or writes the real ~/.claude, ~/.codex or similar.

// TestMain lets the test binary act as a fake stdio MCP server.
func TestMain(m *testing.M) {
	if mode := os.Getenv("NX_FAKE_MCP"); mode != "" {
		fakeServer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeServer(mode string) {
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "starting…")
		fmt.Fprintln(os.Stderr, "Error: GITHUB_TOKEN is not set")
		os.Exit(1)
	}
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	fmt.Println("not json: a log line on stdout")
	for in.Scan() {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || mode == "hang" {
			continue
		}
		switch req.Method {
		case "initialize":
			if mode == "needenv" && os.Getenv("FAKE_KEY") != "real-secret-value" {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "bad key"}})
				continue
			}
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info"}})
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1.2"}}})
		case "tools/list":
			if req.Params["cursor"] == "p2" {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "c"}}}})
			} else {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "b"}, map[string]any{"name": "a"}}, "nextCursor": "p2"}})
			}
		}
	}
}

func editOpts(t *testing.T) (EditOptions, string, string) {
	t.Helper()
	h, w := fixture(t)
	return EditOptions{Home: h, Workspace: w, StateDir: filepath.Join(t.TempDir(), "state"), Now: func() time.Time { return time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC) }}, h, w
}

// ── Rules ──────────────────────────────────────────────────────────────

func TestRulesFilesListsCreatableAndExisting(t *testing.T) {
	o, h, w := editOpts(t)
	put(t, filepath.Join(w, ".cursor", "rules", "style.mdc"), "use tabs")
	put(t, filepath.Join(h, ".claude", "CLAUDE.md"), "mine")
	files := RulesFiles(o)
	byPath := map[string]RulesFile{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	for _, want := range []struct {
		p      string
		exists bool
	}{
		{filepath.Join(w, "CLAUDE.md"), false}, {filepath.Join(w, "AGENTS.md"), false}, {filepath.Join(w, "GEMINI.md"), false},
		{filepath.Join(w, ".cursor", "rules", "style.mdc"), true}, {filepath.Join(h, ".claude", "CLAUDE.md"), true},
		{filepath.Join(h, ".codex", "AGENTS.md"), false},
	} {
		f, ok := byPath[want.p]
		if !ok || f.Exists != want.exists {
			t.Fatalf("%s: listed=%v %+v", want.p, ok, f)
		}
	}
	if _, ok := byPath[filepath.Join(w, ".cursorrules")]; ok {
		t.Fatal("a missing optional file was listed")
	}
	if _, err := ReadRules(o, filepath.Join(w, "secret.txt")); err == nil {
		t.Fatal("read an unlisted file")
	}
	if _, err := SaveRules(o, filepath.Join(h, ".ssh", "config"), "x", "", false); err == nil {
		t.Fatal("wrote an unlisted file")
	}
}

func TestRulesCreateEditConflictAndBackup(t *testing.T) {
	o, _, w := editOpts(t)
	p := filepath.Join(w, "CLAUDE.md")
	doc, err := ReadRules(o, p)
	if err != nil || doc.Exists || doc.ETag != "" {
		t.Fatalf("missing doc: %+v %v", doc, err)
	}
	saved, err := SaveRules(o, p, "# Hi\n", "", false)
	if err != nil || !saved.Created || saved.Backup != "" {
		t.Fatalf("create: %+v %v", saved, err)
	}
	// Creating again with the "missing" etag is a conflict, not an overwrite.
	if _, err := SaveRules(o, p, "# Other\n", "", false); err != ErrRulesConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	prev, err := PreviewRules(o, p, "# Hi\n\nMore.\n", saved.ETag)
	if err != nil || prev.Added != 2 || prev.Removed != 0 || prev.Conflict || !strings.Contains(prev.Diff, "+More.") {
		t.Fatalf("preview: %+v %v", prev, err)
	}
	second, err := SaveRules(o, p, "# Hi\n\nMore.\n", saved.ETag, false)
	if err != nil || second.Backup == "" || second.Action != "saved" {
		t.Fatalf("save: %+v %v", second, err)
	}
	if !strings.HasPrefix(second.Backup, o.StateDir) || string(readTest(t, second.Backup)) != "# Hi\n" {
		t.Fatalf("backup %s", second.Backup)
	}
	// Outside change -> the stale etag is refused and the file is untouched.
	put(t, p, "changed elsewhere\n")
	if _, err := SaveRules(o, p, "mine\n", second.ETag, false); err != ErrRulesConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if string(readTest(t, p)) != "changed elsewhere\n" {
		t.Fatal("conflicting save wrote")
	}
	// No temp files are left behind.
	entries, _ := os.ReadDir(w)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".nexus-") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

func TestRulesKeepCRLFAndBOMAndPruneBackups(t *testing.T) {
	o, _, w := editOpts(t)
	p := filepath.Join(w, "AGENTS.md")
	put(t, p, "\xef\xbb\xbfline one\r\nline two\r\n")
	doc, _ := ReadRules(o, p)
	if doc.Content != "line one\nline two\n" {
		t.Fatalf("normalized: %q", doc.Content)
	}
	tag := doc.ETag
	for i := 0; i < keepBackups+5; i++ {
		res, err := SaveRules(o, p, fmt.Sprintf("line one\nline %d\n", i), tag, false)
		if err != nil {
			t.Fatal(err)
		}
		tag = res.ETag
	}
	if got := string(readTest(t, p)); got != "\xef\xbb\xbfline one\r\nline 24\r\n" {
		t.Fatalf("line endings/BOM: %q", got)
	}
	bk, _ := os.ReadDir(filepath.Join(o.StateDir, "backups"))
	if len(bk) != keepBackups {
		t.Fatalf("kept %d backups", len(bk))
	}
	if res, _ := SaveRules(o, p, "line one\nline 24\n", tag, false); res.Action != "unchanged" {
		t.Fatalf("unchanged save: %+v", res)
	}
}

func TestRulesManagedSectionIsProtected(t *testing.T) {
	o, _, w := editOpts(t)
	p := filepath.Join(w, "CLAUDE.md")
	text := "# Mine\n\n<!-- nexus:memory:start (managed by Nexus; edits inside are replaced) -->\n- memory 1\n<!-- nexus:memory:end -->\n\n## Shared from codex (Nexus)\n\nrule\n<!-- End shared from codex (Nexus) -->\n"
	put(t, p, text)
	doc, _ := ReadRules(o, p)
	if len(doc.Managed) != 2 || doc.Managed[0].Start != 3 || doc.Managed[0].End != 5 || doc.Managed[1].Start != 7 || doc.Managed[1].End != 10 {
		t.Fatalf("managed: %+v", doc.Managed)
	}
	// Editing outside is fine.
	ok := strings.Replace(text, "# Mine", "# Mine, edited", 1)
	res, err := SaveRules(o, p, ok, doc.ETag, false)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(ok, "- memory 1", "- my edit", 1)
	if pv, _ := PreviewRules(o, p, bad, res.ETag); !pv.ManagedChanged {
		t.Fatal("preview did not flag the managed edit")
	}
	if _, err := SaveRules(o, p, bad, res.ETag, false); err == nil || !strings.Contains(err.Error(), "lines 3–5") {
		t.Fatalf("managed edit allowed: %v", err)
	}
	if _, err := SaveRules(o, p, bad, res.ETag, true); err != nil {
		t.Fatalf("allow_managed: %v", err)
	}
}

func TestRulesTemplateFindsCommands(t *testing.T) {
	o, _, w := editOpts(t)
	put(t, filepath.Join(w, "package.json"), `{"scripts":{"dev":"vite","test":"vitest"}}`)
	put(t, filepath.Join(w, "pnpm-lock.yaml"), "")
	put(t, filepath.Join(w, "go.mod"), "module x")
	text, err := RulesTemplate(o, filepath.Join(w, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# " + filepath.Base(w), "`pnpm install`", "`pnpm dev`", "`pnpm test`", "`go test ./...`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("template missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "pnpm build") {
		t.Fatal("listed a script that doesn't exist")
	}
	user, _ := RulesTemplate(o, filepath.Join(o.Home, ".claude", "CLAUDE.md"))
	if !strings.HasPrefix(user, "# My preferences") {
		t.Fatal(user)
	}
}

// ── MCP editing ────────────────────────────────────────────────────────

func stdio(name string) ServerSpec {
	return ServerSpec{Name: name, Transport: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"API_KEY": plantedSecret}}
}

func TestSaveMCPWritesEachAgentsFormat(t *testing.T) {
	o, h, w := editOpts(t)
	for _, a := range EditableAgents {
		if _, err := SaveMCP(o, SaveMCPRequest{Agent: a, Scope: "user", Spec: stdio("files")}); err != nil {
			t.Fatalf("%s stdio: %v", a, err)
		}
		if a == "codex" {
			continue
		}
		if _, err := SaveMCP(o, SaveMCPRequest{Agent: a, Scope: "project", Spec: ServerSpec{Name: "web", Transport: "http", URL: "https://x.test/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}}); err != nil {
			t.Fatalf("%s http: %v", a, err)
		}
	}
	js := func(p string) map[string]any {
		m, err := decodeJSONC(readTest(t, p))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	claude := object(object(js(filepath.Join(h, ".claude.json"))["mcpServers"])["files"])
	if claude["type"] != "stdio" || claude["command"] != "npx" || object(claude["env"])["API_KEY"] != plantedSecret {
		t.Fatalf("claude: %v", claude)
	}
	if web := object(object(js(filepath.Join(w, ".mcp.json"))["mcpServers"])["web"]); web["type"] != "http" || web["url"] != "https://x.test/mcp" {
		t.Fatalf("claude project: %v", web)
	}
	var codex map[string]any
	if _, err := toml.Decode(string(readTest(t, filepath.Join(h, ".codex", "config.toml"))), &codex); err != nil {
		t.Fatal(err)
	}
	if c := object(object(codex["mcp_servers"])["files"]); c["command"] != "npx" || c["type"] != nil {
		t.Fatalf("codex: %v", c)
	}
	if g := object(object(js(filepath.Join(w, ".gemini", "settings.json"))["mcpServers"])["web"]); g["httpUrl"] != "https://x.test/mcp" || g["url"] != nil {
		t.Fatalf("gemini: %v", g)
	}
	if c := object(object(js(filepath.Join(h, ".cursor", "mcp.json"))["mcpServers"])["files"]); c["type"] != nil || c["command"] != "npx" {
		t.Fatalf("cursor: %v", c)
	}
	oc := object(object(js(filepath.Join(h, ".config", "opencode", "opencode.json"))["mcp"])["files"])
	if oc["type"] != "local" || len(oc["command"].([]any)) != 3 || object(oc["environment"])["API_KEY"] != plantedSecret || oc["enabled"] != true {
		t.Fatalf("opencode: %v", oc)
	}
	if r := object(object(js(filepath.Join(w, "opencode.json"))["mcp"])["web"]); r["type"] != "remote" {
		t.Fatalf("opencode remote: %v", r)
	}
	// Codex has no SSE and no project file.
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "codex", Scope: "user", Spec: ServerSpec{Name: "old", Transport: "sse", URL: "https://x.test/sse"}}); err == nil {
		t.Fatal("codex sse accepted")
	}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "codex", Scope: "project", Spec: stdio("p")}); err == nil {
		t.Fatal("codex project accepted")
	}
	// The listing shows the new servers without secret values.
	inv := Browse(BrowseOptions{Home: h, Workspace: w, LookPath: noLook})
	noSecrets(t, inv)
	if n := len(agent(t, inv, "cursor").MCPServers); n != 2 {
		t.Fatalf("cursor lists %d servers", n)
	}
}

func TestSaveMCPKeepsCommentsOrderAndUnknownKeys(t *testing.T) {
	o, h, _ := editOpts(t)
	p := filepath.Join(h, ".gemini", "settings.json")
	orig := "{\n  // my theme\n  \"theme\": \"dark\",\n  \"mcpServers\": {\n    \"keep\": {\"command\": \"k\"}, // trailing note\n    \"edit\": {\n      \"command\": \"old\",\n      \"timeout\": 30000,\n      \"trust\": true,\n      \"env\": {\"TOKEN\": \"" + plantedSecret + "\"}\n    }\n  },\n  \"zeta\": 1\n}\n"
	put(t, p, orig)
	got, err := GetMCP(o, "gemini", "user", p, "edit")
	if err != nil {
		t.Fatal(err)
	}
	noSecrets(t, got)
	if got.Spec.Env["TOKEN"] != Mask || strings.Join(got.Kept, ",") != "timeout,trust" {
		t.Fatalf("get: %+v", got)
	}
	spec := got.Spec
	spec.Command = "new"
	spec.Args = []string{"--flag"}
	res, err := SaveMCP(o, SaveMCPRequest{Agent: "gemini", Scope: "user", Path: p, OriginalName: "edit", Spec: spec})
	if err != nil || res.Action != "updated" || res.Backup == "" {
		t.Fatalf("save: %+v %v", res, err)
	}
	noSecrets(t, res)
	text := string(readTest(t, p))
	for _, want := range []string{"// my theme", "// trailing note", `"keep": {"command": "k"}`, `"timeout": 30000`, `"trust": true`, plantedSecret, `"command": "new"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost %q:\n%s", want, text)
		}
	}
	if strings.Index(text, `"theme"`) > strings.Index(text, `"zeta"`) {
		t.Fatal("key order changed")
	}
	// A hidden value that was not saved before can't be "kept".
	spec.Env = map[string]string{"TOKEN": Mask, "NEW": Mask}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "gemini", Scope: "user", Path: p, OriginalName: "edit", Spec: spec}); err == nil {
		t.Fatal("accepted a hidden value with nothing behind it")
	}
}

func TestSaveMCPRenameDuplicateAndNexus(t *testing.T) {
	o, h, _ := editOpts(t)
	p := filepath.Join(h, ".cursor", "mcp.json")
	put(t, p, `{"mcpServers":{"a":{"command":"x"},"b":{"command":"y"},"nexus":{"url":"https://n"}}}`)
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", Spec: ServerSpec{Name: "a", Command: "z"}}); err == nil {
		t.Fatal("duplicate add accepted")
	}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", OriginalName: "a", Spec: ServerSpec{Name: "b", Command: "z"}}); err == nil {
		t.Fatal("rename onto an existing name accepted")
	}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", Spec: ServerSpec{Name: "nexus", Command: "z"}}); err == nil {
		t.Fatal("nexus add accepted")
	}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", OriginalName: "nexus", Spec: ServerSpec{Name: "n2", Command: "z"}}); err == nil {
		t.Fatal("nexus edit accepted")
	}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", Spec: ServerSpec{Name: "has space", Command: "z"}}); err == nil {
		t.Fatal("bad name accepted")
	}
	dry, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", OriginalName: "a", Spec: ServerSpec{Name: "a2", Command: "x", Args: []string{"two words"}}, DryRun: true})
	if err != nil || dry.Action != "will update" || dry.CommandLine != `x "two words"` {
		t.Fatalf("dry: %+v %v", dry, err)
	}
	if strings.Contains(string(readTest(t, p)), "a2") {
		t.Fatal("dry run wrote")
	}
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", OriginalName: "a", Spec: ServerSpec{Name: "a2", Command: "x"}}); err != nil {
		t.Fatal(err)
	}
	m, _ := decodeJSONC(readTest(t, p))
	s := object(m["mcpServers"])
	if s["a"] != nil || s["a2"] == nil || s["b"] == nil || s["nexus"] == nil {
		t.Fatalf("rename: %v", s)
	}
}

func TestCodexEditKeepsOtherTables(t *testing.T) {
	o, h, _ := editOpts(t)
	p := filepath.Join(h, ".codex", "config.toml")
	put(t, p, "# my codex\nmodel = \"gpt-5\"\n\n[mcp_servers.docs]\ncommand = \"old\"\nstartup_timeout_sec = 20\n\n[mcp_servers.docs.env]\nKEY = \""+plantedSecret+"\"\n\n[profiles.fast]\nmodel = \"mini\"\n")
	got, err := GetMCP(o, "codex", "user", p, "docs")
	if err != nil {
		t.Fatal(err)
	}
	spec := got.Spec
	spec.Command = "new"
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "codex", Scope: "user", Path: p, OriginalName: "docs", Spec: spec}); err != nil {
		t.Fatal(err)
	}
	text := string(readTest(t, p))
	var m map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		t.Fatal(err)
	}
	d := object(object(m["mcp_servers"])["docs"])
	if d["command"] != "new" || d["startup_timeout_sec"] == nil || object(d["env"])["KEY"] != plantedSecret {
		t.Fatalf("docs: %v", d)
	}
	if !strings.Contains(text, "\n\n[mcp_servers.docs]\ncommand = \"new\"") {
		t.Fatalf("table not tidy:\n%s", text)
	}
	if !strings.HasPrefix(text, "# my codex\nmodel = \"gpt-5\"") || object(object(m["profiles"])["fast"])["model"] != "mini" {
		t.Fatalf("other settings lost:\n%s", text)
	}
	// Turn off and on with Codex's own setting.
	if _, err := SetMCPEnabled(o, "codex", "user", p, "docs", false); err != nil {
		t.Fatal(err)
	}
	inv := Browse(BrowseOptions{Home: h, LookPath: noLook})
	if s := agent(t, inv, "codex").MCPServers; len(s) != 1 || !s[0].Disabled || s[0].Parked {
		t.Fatalf("codex off: %+v", s)
	}
	if _, err := SetMCPEnabled(o, "codex", "user", p, "docs", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readTest(t, p)), "enabled") {
		t.Fatal("enabled flag left behind")
	}
}

func TestClaudeLocalServerEditKeepsTheRestOfClaudeJSON(t *testing.T) {
	o, h, w := editOpts(t)
	p := filepath.Join(h, ".claude.json")
	wsKey := strings.ReplaceAll(w, `\`, `\\`)
	orig := `{"numStartups": 9007199254740993, "zebra": 1, "projects": {"` + wsKey + `": {"allowedTools": [], "mcpServers": {"loc": {"type": "stdio", "command": "old"}}}}, "alpha": 2}`
	put(t, p, orig)
	inv := Browse(BrowseOptions{Home: h, Workspace: w, LookPath: noLook})
	var item Item
	for _, it := range agent(t, inv, "claude").MCPServers {
		if it.Name == "loc" {
			item = it
		}
	}
	if item.Scope != "project" {
		t.Fatalf("local item: %+v", item)
	}
	got, err := GetMCP(o, "claude", item.Scope, item.Path, "loc")
	if err != nil || got.Scope != "local" {
		t.Fatalf("get: %+v %v", got, err)
	}
	spec := got.Spec
	spec.Command = "new"
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "claude", Scope: item.Scope, Path: item.Path, OriginalName: "loc", Spec: spec}); err != nil {
		t.Fatal(err)
	}
	text := string(readTest(t, p))
	if !strings.Contains(text, "9007199254740993") || strings.Index(text, "zebra") > strings.Index(text, "alpha") || !strings.Contains(text, `"command": "new"`) {
		t.Fatalf("claude.json changed beyond the entry:\n%s", text)
	}
}

func TestToggleParksServersForAgentsWithoutASwitch(t *testing.T) {
	o, h, w := editOpts(t)
	p := filepath.Join(w, ".mcp.json")
	put(t, p, `{"mcpServers": {"gh": {"type": "http", "url": "https://g", "headers": {"Authorization": "Bearer `+plantedSecret+`"}}, "other": {"command": "o"}}}`)
	if _, err := SetMCPEnabled(o, "claude", "project", p, "gh", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readTest(t, p)), "gh") {
		t.Fatal("still in the config")
	}
	inv := Browse(BrowseOptions{Home: h, Workspace: w, StateDir: o.StateDir, LookPath: noLook})
	noSecrets(t, inv)
	var parked *Item
	for _, it := range agent(t, inv, "claude").MCPServers {
		if it.Name == "gh" {
			it := it
			parked = &it
		}
	}
	if parked == nil || !parked.Parked || !parked.Disabled || parked.Scope != "project" {
		t.Fatalf("parked item: %+v", parked)
	}
	// Its entry can still be shown (secrets hidden) while it is off.
	shown, err := ReadItem(BrowseOptions{Home: h, Workspace: w, StateDir: o.StateDir, LookPath: noLook}, "claude", "mcp", "gh", p)
	if err != nil || !strings.Contains(shown.Content, "https://g") || strings.Contains(shown.Content, plantedSecret) {
		t.Fatalf("read parked: %+v %v", shown, err)
	}
	// Another project does not see it.
	other := t.TempDir()
	if items := ParkedItems(EditOptions{Home: h, Workspace: other, StateDir: o.StateDir}); len(items["claude"]) != 0 {
		t.Fatal("parked server leaked to another project")
	}
	// Editing a parked server asks to turn it on first; test still works.
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "claude", Scope: "project", Path: p, OriginalName: "gh", Spec: ServerSpec{Name: "gh", Transport: "http", URL: "https://g"}}); err == nil {
		t.Fatal("edited a parked server")
	}
	if _, err := SetMCPEnabled(o, "claude", "project", p, "gh", true); err != nil {
		t.Fatal(err)
	}
	m, _ := decodeJSONC(readTest(t, p))
	if h := object(object(object(m["mcpServers"])["gh"])["headers"]); h["Authorization"] != "Bearer "+plantedSecret {
		t.Fatalf("restored: %v", m)
	}
	if len(ParkedItems(o)["claude"]) != 0 {
		t.Fatal("store not cleared")
	}
	// Forget removes a parked server for good.
	if _, err := SetMCPEnabled(o, "claude", "project", p, "other", false); err != nil {
		t.Fatal(err)
	}
	if err := ForgetParked(o, "claude", "project", p, "other"); err != nil {
		t.Fatal(err)
	}
	if len(ParkedItems(o)["claude"]) != 0 {
		t.Fatal("forget kept it")
	}
	// Without a state folder there is nowhere to park.
	if _, err := SetMCPEnabled(EditOptions{Home: h, Workspace: w}, "claude", "project", p, "gh", false); err == nil {
		t.Fatal("parked without a state folder")
	}
}

func TestOpenCodeToggleUsesEnabled(t *testing.T) {
	o, h, _ := editOpts(t)
	p := filepath.Join(h, ".config", "opencode", "opencode.json")
	put(t, p, `{"$schema": "https://opencode.ai/config.json", "mcp": {"x": {"type": "local", "command": ["a", "b"], "enabled": true}}}`)
	if _, err := SetMCPEnabled(o, "opencode", "user", p, "x", false); err != nil {
		t.Fatal(err)
	}
	m, _ := decodeJSONC(readTest(t, p))
	if object(object(m["mcp"])["x"])["enabled"] != false || m["$schema"] == nil {
		t.Fatalf("opencode: %v", m)
	}
}

func TestParseMCPVariants(t *testing.T) {
	cases := map[string]string{
		`{"mcpServers": {"pw": {"command": "npx", "args": ["@playwright/mcp@latest"]}}}`:     "pw",
		`{"servers": {"gh": {"type": "http", "url": "https://api.githubcopilot.com/mcp/"}}}`: "gh",
		`{"command": "npx", "args": ["-y", "@playwright/mcp@latest"]}`:                       "playwright",
		`{"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]}`: "filesystem",
		`"fetch": {"command": "uvx", "args": ["mcp-server-fetch"]}`:                          "fetch",
		`{"url": "https://mcp.context7.com/mcp"}`:                                            "context7",
		`{"mcp": {"oc": {"type": "local", "command": ["bun", "x"]}}}`:                        "oc",
		"[mcp_servers.tom]\ncommand = \"t\"\n":                                               "tom",
		`{"mcpServers": {"a": {"command": "x"}, // note
		 "b": {"url": "https://b.test"},}}`: "a",
	}
	for text, name := range cases {
		got, err := ParseMCP(text)
		if err != nil || len(got) == 0 || got[0].Name != name {
			t.Fatalf("%s: %+v %v", text, got, err)
		}
	}
	oc, _ := ParseMCP(`{"mcp": {"oc": {"type": "local", "command": ["bun", "x"]}}}`)
	if oc[0].Command != "bun" || len(oc[0].Args) != 1 || oc[0].Transport != "stdio" {
		t.Fatalf("opencode parse: %+v", oc[0])
	}
	for _, bad := range []string{"", "{nope", `{"theme": "dark"}`} {
		if _, err := ParseMCP(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestTemplates(t *testing.T) {
	for _, tp := range MCPTemplates() {
		values := map[string]string{}
		for _, in := range tp.Inputs {
			values[in.Key] = "v"
		}
		s, err := FromTemplate(tp.ID, values)
		if err != nil {
			t.Fatalf("%s: %v", tp.ID, err)
		}
		if _, err := cleanSpec(s, "claude"); err != nil {
			t.Fatalf("%s: %v", tp.ID, err)
		}
		if b, _ := json.Marshal(s); strings.Contains(string(b), "{folder}") || strings.Contains(string(b), "{token}") {
			t.Fatalf("%s left a placeholder: %+v", tp.ID, s)
		}
	}
	if _, err := FromTemplate("filesystem", nil); err == nil {
		t.Fatal("missing folder accepted")
	}
	s, _ := FromTemplate("github", map[string]string{"token": "abc"})
	if s.Headers["Authorization"] != "Bearer abc" {
		t.Fatal(s)
	}
}

// ── Testing a server ───────────────────────────────────────────────────

func fakeSpec(t *testing.T, mode string) ServerSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ServerSpec{Name: "fake", Transport: "stdio", Command: exe, Env: map[string]string{"NX_FAKE_MCP": mode}}
}

func TestProbeStdio(t *testing.T) {
	res := ProbeMCP(context.Background(), fakeSpec(t, "ok"), 20*time.Second)
	if !res.OK || res.ToolCount != 3 || strings.Join(res.Tools, ",") != "a,b,c" || res.ServerName != "fake" || res.Message != "Connected. 3 tools are ready." {
		t.Fatalf("ok: %+v", res)
	}
	res = ProbeMCP(context.Background(), fakeSpec(t, "crash"), 20*time.Second)
	if res.OK || !strings.Contains(res.Message, "stopped before") || !strings.Contains(res.Message, "GITHUB_TOKEN is not set") {
		t.Fatalf("crash: %+v", res)
	}
	start := time.Now()
	res = ProbeMCP(context.Background(), fakeSpec(t, "hang"), 1500*time.Millisecond)
	if res.OK || !strings.Contains(res.Message, "didn't answer within 1 seconds") || time.Since(start) > 10*time.Second {
		t.Fatalf("hang: %+v after %s", res, time.Since(start))
	}
	res = ProbeMCP(context.Background(), ServerSpec{Name: "x", Transport: "stdio", Command: "npx-not-installed-anywhere"}, 5*time.Second)
	if res.OK || !strings.Contains(res.Message, "couldn't find") {
		t.Fatalf("missing: %+v", res)
	}
	if needs("npx") != "Install Node.js (it includes npx), then try again." || !strings.Contains(needs("uvx.exe"), "uv") {
		t.Fatal("needs")
	}
}

func TestTestMCPUsesSavedSecrets(t *testing.T) {
	o, h, _ := editOpts(t)
	spec := fakeSpec(t, "needenv")
	spec.Env["FAKE_KEY"] = "real-secret-value"
	p := filepath.Join(h, ".cursor", "mcp.json")
	if _, err := SaveMCP(o, SaveMCPRequest{Agent: "cursor", Scope: "user", Spec: spec}); err != nil {
		t.Fatal(err)
	}
	res := TestMCP(context.Background(), o, MCPTestRequest{Agent: "cursor", Scope: "user", Path: p, Name: "fake"})
	if !res.OK {
		t.Fatalf("saved: %+v", res)
	}
	// The editor sends the masked spec; hidden values come from the file.
	got, _ := GetMCP(o, "cursor", "user", p, "fake")
	masked := got.Spec
	res = TestMCP(context.Background(), o, MCPTestRequest{Agent: "cursor", Scope: "user", Path: p, OriginalName: "fake", Spec: &masked})
	if !res.OK {
		t.Fatalf("masked: %+v", res)
	}
	masked.Env["FAKE_KEY"] = "wrong"
	res = TestMCP(context.Background(), o, MCPTestRequest{Agent: "cursor", Scope: "user", Path: p, OriginalName: "fake", Spec: &masked})
	if res.OK || !strings.Contains(res.Message, "bad key") {
		t.Fatalf("wrong key: %+v", res)
	}
}

func mcpHandler(t *testing.T, sse bool, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "nope "+token, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete {
			return
		}
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"serverInfo": map[string]any{"name": "remote"}}
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "s1" {
				http.Error(w, "no session", http.StatusBadRequest)
				return
			}
			result = map[string]any{"tools": []any{map[string]any{"name": "one"}}}
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

func TestProbeHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		srv := httptest.NewServer(mcpHandler(t, sse, "good"))
		res := ProbeMCP(context.Background(), ServerSpec{Transport: "http", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer good"}}, 10*time.Second)
		if !res.OK || res.ToolCount != 1 || res.ServerName != "remote" {
			t.Fatalf("sse=%v: %+v", sse, res)
		}
		res = ProbeMCP(context.Background(), ServerSpec{Transport: "http", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer wrong-token-value"}}, 10*time.Second)
		if res.OK || !strings.Contains(res.Message, "401") || strings.Contains(res.Detail, "wrong-token-value") {
			t.Fatalf("401: %+v", res)
		}
		srv.Close()
	}
	res := ProbeMCP(context.Background(), ServerSpec{Transport: "http", URL: "http://127.0.0.1:1/mcp"}, 5*time.Second)
	if res.OK || !strings.Contains(res.Message, "Nothing is answering") {
		t.Fatalf("refused: %+v", res)
	}
}

func TestProbeLegacySSE(t *testing.T) {
	msgs := make(chan []byte, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "event: endpoint\ndata: /messages?session=1\n\n")
		fl.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case b := <-msgs:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"serverInfo": map[string]any{"name": "legacy"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "x"}, map[string]any{"name": "y"}}}
		default:
			return
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		msgs <- b
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res := ProbeMCP(context.Background(), ServerSpec{Transport: "sse", URL: srv.URL + "/sse"}, 10*time.Second)
	if !res.OK || res.ToolCount != 2 || res.ServerName != "legacy" {
		t.Fatalf("legacy: %+v", res)
	}
}

func TestPatchNestedCreatesMissingServersMap(t *testing.T) {
	old := []byte("{\n  \"projects\": {\n    \"p\": {\n      \"x\": 1\n    }\n  }\n}\n")
	want := map[string]any{"projects": map[string]any{"p": map[string]any{"x": json.Number("1"), "mcpServers": map[string]any{"s": map[string]any{"command": "c"}}}}}
	out, err := patchNested(old, []string{"projects", "p", "mcpServers"}, "s", map[string]any{"command": "c"}, want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\"x\": 1,\n      \"mcpServers\"") {
		t.Fatalf("%s", out)
	}
}
