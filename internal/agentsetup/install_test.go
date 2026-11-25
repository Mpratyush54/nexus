package agentsetup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/mcpproxy"
)

const testServer = "https://nexus.example.test"

func noLook(string) (string, error) { return "", errors.New("not found") }

// installHome lays out a temp home with every harness "installed" and a
// pre-existing config (with unrelated content) for a few of them.
func installHome(t *testing.T) (string, string) {
	t.Helper()
	h, w := fixture(t)
	for _, d := range []string{".claude", ".codex", ".cursor", ".config/opencode", ".kimi", ".copilot", ".codeium/windsurf", ".gemini/antigravity"} {
		if e := os.MkdirAll(filepath.Join(h, d), 0700); e != nil {
			t.Fatal(e)
		}
	}
	put(t, filepath.Join(h, ".claude.json"), `{"numStartups": 9007199254740993, "mcpServers": {"other": {"command": "x"}}}`)
	put(t, filepath.Join(h, ".codex/config.toml"), "# keep me\nmodel = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"y\"\n")
	put(t, filepath.Join(h, ".gemini/settings.json"), `{"theme": "dark"}`)
	return h, w
}

type minter struct {
	calls []string
}

func (m *minter) mint(h string) (string, error) {
	m.calls = append(m.calls, h)
	return "nxs_" + h + "_token", nil
}

func lookNexus(name string) (string, error) {
	if name == "nexus" {
		return "/usr/bin/nexus", nil
	}
	return "", errors.New("not found")
}

func reportFor(t *testing.T, reps []HarnessReport, id string) HarnessReport {
	t.Helper()
	for _, r := range reps {
		if r.Harness == id {
			return r
		}
	}
	t.Fatal("no report for", id)
	return HarnessReport{}
}

func nexusEntryAt(t *testing.T, h, id string) map[string]any {
	t.Helper()
	r := resolve(ScanOptions{Home: h})
	for _, s := range harnesses {
		if s.id != id {
			continue
		}
		p, _ := s.configPath(r, "user")
		m, e := r.config(p)
		if e != nil {
			t.Fatalf("%s: %v", id, e)
		}
		return object(object(m[s.key])[NexusServerName])
	}
	t.Fatal("unknown harness", id)
	return nil
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func TestInstallMCPAddsEveryFormat(t *testing.T) {
	h, w := installHome(t)
	m := &minter{}
	reps, err := InstallMCP(InstallOptions{Home: h, Workspace: w, ServerURL: testServer + "/", ProjectID: "proj-1", Token: m.mint, LookPath: lookNexus, SessionToken: "session-jwt"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"claude": "added", "codex": "added", "cursor": "added", "gemini": "added", "opencode": "added", "kimi": "added", "copilot": "added", "windsurf": "added", "antigravity": "added"}
	for id, action := range want {
		rep := reportFor(t, reps, id)
		if !rep.Installed || rep.Action != action || len(rep.ChangedFiles) != 1 {
			t.Fatalf("%s: %+v", id, rep)
		}
	}
	if len(m.calls) != len(want) {
		t.Fatalf("minted %v", m.calls)
	}
	url := testServer + "/v1/agent/mcp"
	checkHeaders := func(id string, headers map[string]any) {
		t.Helper()
		if str(headers["Authorization"]) != "Bearer nxs_"+id+"_token" || str(headers["X-Nexus-Project"]) != "proj-1" {
			t.Fatalf("%s headers: %v", id, headers)
		}
	}
	e := nexusEntryAt(t, h, "claude")
	if str(e["type"]) != "http" || str(e["url"]) != url {
		t.Fatalf("claude: %v", e)
	}
	checkHeaders("claude", object(e["headers"]))
	e = nexusEntryAt(t, h, "codex")
	if str(e["url"]) != url {
		t.Fatalf("codex: %v", e)
	}
	checkHeaders("codex", object(e["http_headers"]))
	e = nexusEntryAt(t, h, "gemini")
	if str(e["httpUrl"]) != url {
		t.Fatalf("gemini: %v", e)
	}
	e = nexusEntryAt(t, h, "opencode")
	if str(e["type"]) != "remote" || e["enabled"] != true {
		t.Fatalf("opencode: %v", e)
	}
	e = nexusEntryAt(t, h, "kimi")
	if str(e["transport"]) != "http" {
		t.Fatalf("kimi: %v", e)
	}
	e = nexusEntryAt(t, h, "copilot")
	if str(e["type"]) != "http" || len(stringsOf(e["tools"])) != 1 {
		t.Fatalf("copilot: %v", e)
	}
	e = nexusEntryAt(t, h, "windsurf")
	if str(e["serverUrl"]) != url {
		t.Fatalf("windsurf: %v", e)
	}
	checkHeaders("windsurf", object(e["headers"]))
	e = nexusEntryAt(t, h, "antigravity")
	env := object(e["env"])
	if str(e["command"]) != "/usr/bin/nexus" || stringsOf(e["args"])[0] != "mcp-proxy" || str(env["NEXUS_TOKEN"]) != "nxs_antigravity_token" || str(env["NEXUS_SERVER"]) != testServer || str(env["NEXUS_PROJECT"]) != "proj-1" {
		t.Fatalf("antigravity: %v", e)
	}

	// Unrelated content survives; existing files were backed up.
	claude := readTest(t, filepath.Join(h, ".claude.json"))
	if !bytes.Contains(claude, []byte("9007199254740993")) || !bytes.Contains(claude, []byte(`"other"`)) {
		t.Fatal("claude config lost content")
	}
	codex := readTest(t, filepath.Join(h, ".codex/config.toml"))
	if !bytes.Contains(codex, []byte("# keep me")) || !bytes.Contains(codex, []byte("[mcp_servers.other]")) {
		t.Fatal("codex config lost content")
	}
	if !bytes.Contains(readTest(t, filepath.Join(h, ".gemini/settings.json")), []byte(`"theme": "dark"`)) {
		t.Fatal("gemini settings lost")
	}
	for _, id := range []string{"claude", "codex", "gemini"} {
		rep := reportFor(t, reps, id)
		if len(rep.Backups) != 1 {
			t.Fatalf("%s: no backup %+v", id, rep)
		}
	}
	if got := readTest(t, reportFor(t, reps, "codex").Backups[0]); !bytes.Contains(got, []byte("# keep me")) || bytes.Contains(got, []byte("nexus")) {
		t.Fatal("codex backup is not the original")
	}
	if len(reportFor(t, reps, "cursor").Backups) != 0 {
		t.Fatal("new file should not be backed up")
	}
	for p, s := range snapshot(t, h) {
		if strings.Contains(s, "session-jwt") {
			t.Fatalf("session token written to %s", p)
		}
	}

	// Idempotent: nothing minted, nothing written, no new backups.
	before := snapshot(t, h)
	m2 := &minter{}
	reps, err = InstallMCP(InstallOptions{Home: h, Workspace: w, ServerURL: testServer, ProjectID: "proj-1", Token: m2.mint, LookPath: lookNexus})
	if err != nil {
		t.Fatal(err)
	}
	for id := range want {
		if rep := reportFor(t, reps, id); rep.Action != "unchanged" || len(rep.ChangedFiles) != 0 {
			t.Fatalf("rerun %s: %+v", id, rep)
		}
	}
	if len(m2.calls) != 0 {
		t.Fatalf("rerun minted %v", m2.calls)
	}
	after := snapshot(t, h)
	if len(after) != len(before) {
		t.Fatal("rerun created files")
	}
	for p, s := range before {
		if after[p] != s {
			t.Fatalf("rerun changed %s", p)
		}
	}
}

func TestInstallMCPUpdatesAndReusesToken(t *testing.T) {
	h, w := fixture(t)
	p := filepath.Join(h, ".cursor/mcp.json")
	// A session token written by hand must be replaced by a scoped one.
	original := `{"mcpServers": {"nexus": {"url": "` + testServer + `/v1/agent/mcp", "headers": {"Authorization": "Bearer session-jwt"}, "timeout": 30}}}`
	put(t, p, original)
	m := &minter{}
	opts := InstallOptions{Home: h, Workspace: w, ServerURL: testServer, Token: m.mint, LookPath: noLook, SessionToken: "session-jwt"}
	reps, err := InstallMCP(opts)
	if err != nil {
		t.Fatal(err)
	}
	rep := reportFor(t, reps, "cursor")
	if rep.Action != "updated" || len(rep.Backups) != 1 || len(m.calls) != 1 {
		t.Fatalf("%+v minted=%v", rep, m.calls)
	}
	if string(readTest(t, rep.Backups[0])) != original {
		t.Fatal("backup differs from original")
	}
	e := nexusEntryAt(t, h, "cursor")
	if str(object(e["headers"])["Authorization"]) != "Bearer nxs_cursor_token" || e["timeout"] == nil {
		t.Fatalf("entry %v", e)
	}
	if reportFor(t, reps, "claude").Action != "skipped" || reportFor(t, reps, "claude").Installed {
		t.Fatal("uninstalled harness not skipped")
	}

	// A project change updates the entry but keeps the agent token.
	opts.ProjectID = "proj-2"
	reps, _ = InstallMCP(opts)
	if rep := reportFor(t, reps, "cursor"); rep.Action != "updated" || len(m.calls) != 1 {
		t.Fatalf("%+v minted=%v", rep, m.calls)
	}
	e = nexusEntryAt(t, h, "cursor")
	if str(object(e["headers"])["Authorization"]) != "Bearer nxs_cursor_token" || str(object(e["headers"])["X-Nexus-Project"]) != "proj-2" {
		t.Fatalf("entry %v", e)
	}

	// A different server needs a new token.
	opts.ServerURL = "https://other.example.test"
	reps, _ = InstallMCP(opts)
	if rep := reportFor(t, reps, "cursor"); rep.Action != "updated" || len(m.calls) != 2 {
		t.Fatalf("%+v minted=%v", rep, m.calls)
	}
}

func TestInstallMCPDryRunWritesNothing(t *testing.T) {
	h, w := installHome(t)
	before := snapshot(t, h)
	m := &minter{}
	reps, err := InstallMCP(InstallOptions{Home: h, Workspace: w, ServerURL: testServer, Token: m.mint, LookPath: lookNexus, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 0 {
		t.Fatal("dry run minted a token")
	}
	for _, rep := range reps {
		if rep.Action != "added" || len(rep.ChangedFiles) != 1 {
			t.Fatalf("%+v", rep)
		}
	}
	if len(reportFor(t, reps, "claude").Backups) != 1 {
		t.Fatal("dry run should preview the backup")
	}
	after := snapshot(t, h)
	if len(after) != len(before) {
		t.Fatal("dry run created files")
	}
	for p, s := range before {
		if after[p] != s {
			t.Fatalf("dry run changed %s", p)
		}
	}
}

func TestInstallMCPScopesAndErrors(t *testing.T) {
	h, w := installHome(t)
	reps, err := InstallMCP(InstallOptions{Home: h, Workspace: w, Scope: "project", ServerURL: testServer, Token: (&minter{}).mint, LookPath: noLook})
	if err != nil {
		t.Fatal(err)
	}
	if rep := reportFor(t, reps, "codex"); rep.Action != "skipped" {
		t.Fatalf("%+v", rep)
	}
	if rep := reportFor(t, reps, "claude"); rep.Action != "added" || rep.Path != filepath.Join(w, ".mcp.json") {
		t.Fatalf("%+v", rep)
	}
	if rep := reportFor(t, reps, "opencode"); rep.Path != filepath.Join(w, "opencode.json") {
		t.Fatalf("%+v", rep)
	}
	if rep := reportFor(t, reps, "antigravity"); rep.Action != "skipped" {
		t.Fatalf("%+v", rep)
	}

	// A malformed config is reported and left untouched.
	bad := filepath.Join(h, ".kimi/mcp.json")
	put(t, bad, "{not json")
	failing := func(string) (string, error) { return "", errors.New("offline") }
	reps, _ = InstallMCP(InstallOptions{Home: h, ServerURL: testServer, Token: failing, LookPath: noLook})
	if rep := reportFor(t, reps, "kimi"); rep.Action != "error" || string(readTest(t, bad)) != "{not json" {
		t.Fatalf("%+v", rep)
	}
	if rep := reportFor(t, reps, "cursor"); rep.Action != "error" || !strings.Contains(rep.Reason, "offline") {
		t.Fatalf("%+v", rep)
	}
	if _, e := os.Stat(filepath.Join(h, ".cursor/mcp.json")); !os.IsNotExist(e) {
		t.Fatal("file written without a token")
	}
	if _, err := InstallMCP(InstallOptions{Home: h, LookPath: noLook}); err == nil {
		t.Fatal("install without server URL must fail")
	}
}

func TestUninstallMCPRemovesOnlyNexus(t *testing.T) {
	h, w := installHome(t)
	opts := InstallOptions{Home: h, Workspace: w, ServerURL: testServer, Token: (&minter{}).mint, LookPath: lookNexus}
	if _, err := InstallMCP(opts); err != nil {
		t.Fatal(err)
	}
	dry, err := UninstallMCP(InstallOptions{Home: h, Workspace: w, LookPath: lookNexus, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := reportFor(t, dry, "codex"); rep.Action != "removed" {
		t.Fatalf("%+v", rep)
	}
	if len(nexusEntryAt(t, h, "codex")) == 0 {
		t.Fatal("dry-run uninstall removed the entry")
	}
	reps, err := UninstallMCP(InstallOptions{Home: h, Workspace: w, LookPath: lookNexus})
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range reps {
		if rep.Action != "removed" || len(rep.Backups) != 1 {
			t.Fatalf("%+v", rep)
		}
		if len(nexusEntryAt(t, h, rep.Harness)) != 0 {
			t.Fatalf("%s still has nexus", rep.Harness)
		}
	}
	r := resolve(ScanOptions{Home: h})
	m, _ := r.config(filepath.Join(h, ".codex/config.toml"))
	if object(object(m["mcp_servers"])["other"]) == nil || !bytes.Contains(readTest(t, filepath.Join(h, ".codex/config.toml")), []byte("# keep me")) {
		t.Fatal("codex lost other content")
	}
	m, _ = r.config(filepath.Join(h, ".claude.json"))
	if object(object(m["mcpServers"])["other"]) == nil {
		t.Fatal("claude lost other server")
	}
	reps, _ = UninstallMCP(InstallOptions{Home: h, Workspace: w, LookPath: lookNexus})
	for _, rep := range reps {
		if rep.Action != "unchanged" || len(rep.ChangedFiles) != 0 {
			t.Fatalf("second uninstall %+v", rep)
		}
	}
}

func TestShareAllIsIdempotent(t *testing.T) {
	h, w := fixture(t)
	put(t, filepath.Join(h, ".claude/skills/design/SKILL.md"), "---\nname: design\n---\nDesign skill")
	put(t, filepath.Join(h, ".claude/commands/check.md"), "Check things")
	put(t, filepath.Join(h, ".claude/CLAUDE.md"), "Claude rules\n\n<!-- nexus:memory:start (managed by Nexus; edits inside are replaced) -->\nmemory\n<!-- nexus:memory:end -->\n")
	put(t, filepath.Join(h, ".codex/AGENTS.md"), "Codex rules\n")
	dry, err := ShareAll(ShareAllOptions{Home: h, Workspace: w, Scope: "user", DryRun: true})
	if err != nil || len(dry.Writes) == 0 {
		t.Fatalf("%+v %v", dry, err)
	}
	if _, e := os.Stat(filepath.Join(h, ".codex/skills")); !os.IsNotExist(e) {
		t.Fatal("dry run wrote")
	}
	res, err := ShareAll(ShareAllOptions{Home: h, Workspace: w, Scope: "user"})
	if err != nil || len(res.Writes) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if string(readTest(t, filepath.Join(h, ".codex/skills/design/SKILL.md"))) != "---\nname: design\n---\nDesign skill" {
		t.Fatal("skill not shared")
	}
	codex := readTest(t, filepath.Join(h, ".codex/AGENTS.md"))
	claude := readTest(t, filepath.Join(h, ".claude/CLAUDE.md"))
	if !bytes.Contains(codex, []byte("## Shared from claude (Nexus)")) || bytes.Contains(codex, []byte("nexus:memory")) {
		t.Fatalf("codex rules: %s", codex)
	}
	if !bytes.Contains(claude, []byte("## Shared from codex (Nexus)")) || bytes.Contains(claude, []byte("Shared from claude")) {
		t.Fatalf("claude rules: %s", claude)
	}
	before := snapshot(t, h)
	res, err = ShareAll(ShareAllOptions{Home: h, Workspace: w, Scope: "user"})
	if err != nil || len(res.Writes) != 0 {
		t.Fatalf("second share wrote %+v %v", res.Writes, err)
	}
	after := snapshot(t, h)
	for p, s := range before {
		if after[p] != s {
			t.Fatalf("second share changed %s", p)
		}
	}
}

// The desktop app ships nexus-mcp-proxy next to Nexus.exe, so Antigravity is
// configured with that absolute path even when a nexus CLI is on PATH, and is
// only skipped (with a reason) when neither exists.
func TestInstallMCPAntigravityUsesBundledProxy(t *testing.T) {
	h, w := installHome(t)
	app := t.TempDir()
	bundled := filepath.Join(app, mcpproxy.BinaryName())
	put(t, bundled, "stub")
	reps, err := InstallMCP(InstallOptions{Home: h, Workspace: w, ServerURL: testServer, ProjectID: "proj-1", Token: (&minter{}).mint, LookPath: lookNexus, AppDir: app})
	if err != nil {
		t.Fatal(err)
	}
	if rep := reportFor(t, reps, "antigravity"); rep.Action != "added" {
		t.Fatalf("%+v", rep)
	}
	e := nexusEntryAt(t, h, "antigravity")
	env := object(e["env"])
	if str(e["command"]) != bundled || len(stringsOf(e["args"])) != 0 || str(env["NEXUS_TOKEN"]) != "nxs_antigravity_token" || str(env["NEXUS_SERVER"]) != testServer || str(env["NEXUS_PROJECT"]) != "proj-1" {
		t.Fatalf("antigravity: %v", e)
	}

	// No bundled proxy and no CLI: skipped with a reason, nothing written.
	h2, w2 := installHome(t)
	reps, err = InstallMCP(InstallOptions{Home: h2, Workspace: w2, ServerURL: testServer, Token: (&minter{}).mint, LookPath: noLook, AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rep := reportFor(t, reps, "antigravity")
	if rep.Action != "skipped" || !strings.Contains(rep.Reason, mcpproxy.BinaryName()) || len(rep.ChangedFiles) != 0 {
		t.Fatalf("%+v", rep)
	}
}
