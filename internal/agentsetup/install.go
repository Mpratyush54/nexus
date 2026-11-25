package agentsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"central-memory/internal/mcpproxy"
)

// NexusServerName is the MCP server key Nexus owns in every harness config.
// Install and uninstall only ever touch this one entry.
const NexusServerName = "nexus"

// PendingToken stands in for a token that a real install would mint. It
// only appears in dry-run previews, which never write.
const PendingToken = "<new agent token>"

// InstallOptions control InstallMCP / UninstallMCP.
type InstallOptions struct {
	Home, Workspace string
	// Scope is "user" (default) or "project" (needs Workspace).
	Scope string
	// ServerURL is the signed-in cloud base; the entry points at
	// ServerURL + /v1/agent/mcp.
	ServerURL string
	// ProjectID, when set, is sent as X-Nexus-Project.
	ProjectID string
	DryRun    bool
	// SessionToken is the signed-in user's own token. It is never written
	// to an agent config nor treated as a reusable agent token.
	SessionToken string
	// Token mints a scoped agent token for one harness. It is called only
	// when a harness is about to be written and has no reusable token
	// already, and never in DryRun.
	Token func(harness string) (string, error)
	// LookPath finds CLIs (install detection, the PATH fallback for
	// Antigravity's proxy). Nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// AppDir is the desktop app folder holding the bundled
	// nexus-mcp-proxy, which Antigravity's entry prefers over any CLI on
	// PATH. Empty skips the bundled lookup.
	AppDir string
}

// HarnessReport is the per-harness outcome of an install or uninstall.
type HarnessReport struct {
	Harness      string   `json:"harness"`
	Name         string   `json:"name"`
	Installed    bool     `json:"installed"`
	Path         string   `json:"path,omitempty"`
	ChangedFiles []string `json:"changed_files"`
	Backups      []string `json:"backups,omitempty"`
	// Action is added, updated, removed, unchanged, skipped or error.
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type harnessSpec struct {
	id, name string
	binaries []string
	markers  []string // relative to home; any existing one means installed
	user     []string // config path relative to home
	project  []string // config path relative to workspace; nil: user scope only
	key      string   // servers map key in the config document
	encode   func(nexusEntry) map[string]any
}

type nexusEntry struct {
	base, url, token, projectID string
	proxy                       []string
}

func (n nexusEntry) headers() map[string]any {
	h := map[string]any{"Authorization": "Bearer " + n.token}
	if n.projectID != "" {
		h["X-Nexus-Project"] = n.projectID
	}
	return h
}

// Harnesses lists every agent InstallMCP can configure, in display order.
var harnesses = []harnessSpec{
	{id: "claude", name: "Claude Code", binaries: []string{"claude"}, markers: []string{".claude", ".claude.json"},
		user: []string{".claude.json"}, project: []string{".mcp.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"type": "http", "url": n.url, "headers": n.headers()}
		}},
	{id: "codex", name: "Codex", binaries: []string{"codex"}, markers: []string{".codex"},
		user: []string{".codex", "config.toml"}, key: "mcp_servers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"url": n.url, "http_headers": n.headers()}
		}},
	{id: "cursor", name: "Cursor", binaries: []string{"cursor-agent", "cursor"}, markers: []string{".cursor"},
		user: []string{".cursor", "mcp.json"}, project: []string{".cursor", "mcp.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"url": n.url, "headers": n.headers()}
		}},
	{id: "gemini", name: "Gemini CLI", binaries: []string{"gemini"},
		markers: []string{filepath.Join(".gemini", "settings.json"), filepath.Join(".gemini", "oauth_creds.json"), filepath.Join(".gemini", "GEMINI.md")},
		user:    []string{".gemini", "settings.json"}, project: []string{".gemini", "settings.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"httpUrl": n.url, "headers": n.headers()}
		}},
	{id: "opencode", name: "OpenCode", binaries: []string{"opencode"}, markers: []string{filepath.Join(".config", "opencode")},
		user: []string{".config", "opencode", "opencode.json"}, project: []string{"opencode.json"}, key: "mcp",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"type": "remote", "url": n.url, "headers": n.headers(), "enabled": true}
		}},
	{id: "kimi", name: "Kimi Code", binaries: []string{"kimi"}, markers: []string{".kimi"},
		user: []string{".kimi", "mcp.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"url": n.url, "transport": "http", "headers": n.headers()}
		}},
	{id: "copilot", name: "GitHub Copilot CLI", binaries: []string{"copilot"}, markers: []string{".copilot"},
		user: []string{".copilot", "mcp-config.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"type": "http", "url": n.url, "headers": n.headers(), "tools": []any{"*"}}
		}},
	{id: "windsurf", name: "Windsurf", binaries: []string{"windsurf"}, markers: []string{filepath.Join(".codeium", "windsurf")},
		user: []string{".codeium", "windsurf", "mcp_config.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			return map[string]any{"serverUrl": n.url, "headers": n.headers()}
		}},
	// Antigravity drops custom MCP headers, so it goes through the stdio
	// proxy (mcpconfig does the same) with the token in the proxy's env.
	{id: "antigravity", name: "Antigravity", binaries: []string{"antigravity"}, markers: []string{filepath.Join(".gemini", "antigravity")},
		user: []string{".gemini", "antigravity", "mcp_config.json"}, key: "mcpServers",
		encode: func(n nexusEntry) map[string]any {
			env := map[string]any{"NEXUS_SERVER": n.base, "NEXUS_TOKEN": n.token}
			if n.projectID != "" {
				env["NEXUS_PROJECT"] = n.projectID
			}
			args := []any{}
			for _, a := range n.proxy[1:] {
				args = append(args, a)
			}
			return map[string]any{"command": n.proxy[0], "args": args, "env": env}
		}},
}

// Keys a Nexus entry may own across formats; switching transport clears them
// so a stale url never sits next to a command. Anything else the user added
// (timeouts, enabled flags, tool filters) is kept.
var ownedKeys = []string{"type", "transport", "url", "httpUrl", "serverUrl", "headers", "http_headers", "command", "args", "env", "environment"}

// HarnessIDs returns every harness InstallMCP knows, in display order.
func HarnessIDs() []string {
	out := []string{}
	for _, h := range harnesses {
		out = append(out, h.id)
	}
	return out
}

// InstalledHarnesses reports which harnesses look installed (config folder
// present or CLI on PATH). Read-only.
func InstalledHarnesses(o InstallOptions) map[string]bool {
	r := resolve(ScanOptions{Home: o.Home, Workspace: o.Workspace})
	out := map[string]bool{}
	for _, h := range harnesses {
		out[h.id] = h.installed(r, o.look())
	}
	return out
}

func (o InstallOptions) look() func(string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath
	}
	return exec.LookPath
}

func (h harnessSpec) installed(r roots, look func(string) (string, error)) bool {
	for _, m := range h.markers {
		if _, e := os.Stat(filepath.Join(r.home, m)); e == nil {
			return true
		}
	}
	for _, b := range h.binaries {
		if _, e := look(b); e == nil {
			return true
		}
	}
	return false
}

func (h harnessSpec) configPath(r roots, scope string) (string, error) {
	var p string
	if scope == "project" {
		if h.project == nil {
			return "", fmt.Errorf("%s can only be connected for all projects, not one project folder", h.name)
		}
		p = filepath.Join(append([]string{r.workspace}, h.project...)...)
	} else {
		p = filepath.Join(append([]string{r.home}, h.user...)...)
	}
	if h.id == "opencode" {
		if _, e := os.Lstat(p); os.IsNotExist(e) {
			if _, e := os.Lstat(p + "c"); e == nil {
				p += "c"
			}
		}
	}
	return p, nil
}

// InstallMCP adds or updates the Nexus MCP server in every installed
// harness. Each changed file is backed up first; DryRun writes nothing and
// mints no token. Errors are per harness and never stop the others.
func InstallMCP(o InstallOptions) ([]HarnessReport, error) { return runMCP(o, false) }

// UninstallMCP removes only the Nexus MCP entry from every harness config.
func UninstallMCP(o InstallOptions) ([]HarnessReport, error) { return runMCP(o, true) }

func runMCP(o InstallOptions, uninstall bool) ([]HarnessReport, error) {
	scope := strings.TrimSpace(o.Scope)
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "project" {
		return nil, fmt.Errorf("scope must be user or project")
	}
	r := resolve(ScanOptions{Home: o.Home, Workspace: o.Workspace})
	if scope == "project" && r.workspace == "" {
		return nil, fmt.Errorf("project scope needs a project folder")
	}
	base := strings.TrimRight(strings.TrimSpace(o.ServerURL), "/")
	if !uninstall && base == "" {
		return nil, fmt.Errorf("Sign in to Nexus first, then connect your agents.")
	}
	out := []HarnessReport{}
	for _, h := range harnesses {
		rep := HarnessReport{Harness: h.id, Name: h.name, ChangedFiles: []string{}}
		rep.Installed = h.installed(r, o.look())
		if !rep.Installed {
			rep.Action, rep.Reason = "skipped", "not installed"
			out = append(out, rep)
			continue
		}
		path, e := h.configPath(r, scope)
		if e != nil {
			rep.Action, rep.Reason = "skipped", e.Error()
			out = append(out, rep)
			continue
		}
		rep.Path = path
		if e := h.apply(r, o, base, path, uninstall, &rep); e != nil {
			rep.Action, rep.Reason = "error", e.Error()
			rep.ChangedFiles = []string{}
			rep.Backups = nil
		}
		out = append(out, rep)
	}
	return out, nil
}

func (h harnessSpec) apply(r roots, o InstallOptions, base, path string, uninstall bool, rep *HarnessReport) error {
	if !r.safe(path) {
		return fmt.Errorf("Nexus only changes settings inside your user folder or project folder, so this agent was skipped")
	}
	old, readErr := r.read(path)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("Nexus couldn't open %s. Close the agent if it's running, then try again", filepath.Base(path))
	}
	doc := map[string]any{}
	if exists && len(bytes.TrimSpace(old)) > 0 {
		m, e := r.config(path)
		if e != nil {
			return fmt.Errorf("%s has a mistake in it, so Nexus left it as it was. Fix the file (or open the agent once to repair it), then try again", filepath.Base(path))
		}
		doc = m
	}
	if doc[h.key] != nil && object(doc[h.key]) == nil {
		return fmt.Errorf("%s is set up in a way Nexus doesn't recognise, so it was left as it was"+" [detail: unexpected %q value]", filepath.Base(path), h.key)
	}
	servers := object(doc[h.key])
	prev, hasPrev := servers[NexusServerName]
	prevEntry := object(prev)
	if hasPrev && prevEntry == nil {
		return fmt.Errorf("This agent already has a different \"nexus\" entry, so Nexus left it as it was")
	}

	var next map[string]any // nil: remove the entry
	if uninstall {
		if !hasPrev {
			rep.Action = "unchanged"
			rep.Reason = "Nexus is not configured"
			return nil
		}
	} else {
		entry := nexusEntry{base: base, url: base + "/v1/agent/mcp", projectID: strings.TrimSpace(o.ProjectID)}
		if h.id == "antigravity" {
			proxy, e := mcpproxy.Locate(o.AppDir, o.look())
			if e != nil {
				rep.Action = "skipped"
				rep.Reason = e.Error()
				return nil
			}
			entry.proxy = proxy
		}
		// Reuse the agent token already in this harness when it targets the
		// same server, so reinstalling is idempotent and mints nothing.
		entry.token = reusableToken(prevEntry, entry)
		if session := strings.TrimSpace(o.SessionToken); session != "" && entry.token == session {
			entry.token = ""
		}
		if entry.token != "" {
			if same(prevEntry, merged(prevEntry, h.encode(entry))) {
				rep.Action = "unchanged"
				return nil
			}
		} else if o.DryRun {
			entry.token = PendingToken
		} else {
			if o.Token == nil {
				return fmt.Errorf("Sign in to Nexus first, then connect your agents.")
			}
			tok, e := o.Token(h.id)
			if e != nil {
				return fmt.Errorf("Nexus couldn't create a key for this agent. Check your connection, then try again"+" [detail: %v]", e)
			}
			tok = strings.TrimSpace(tok)
			if tok == "" || tok == strings.TrimSpace(o.SessionToken) {
				return fmt.Errorf("Nexus couldn't create a key for this agent. Try again in a moment")
			}
			entry.token = tok
		}
		next = merged(prevEntry, h.encode(entry))
	}

	var data []byte
	if h.key == "mcp_servers" { // TOML (Codex): rewrite only Nexus's tables
		var e error
		if next == nil {
			data = cutTable(old, NexusServerName)
		} else {
			data, e = replaceTable(old, NexusServerName, next)
			if e != nil {
				return e
			}
		}
	} else {
		if servers == nil {
			servers = map[string]any{}
		}
		if next == nil {
			delete(servers, NexusServerName)
		} else {
			servers[NexusServerName] = next
		}
		doc[h.key] = servers
		// Edit only Nexus's entry in place, so the user's comments, key
		// order and formatting stay exactly as they were.
		hasText := exists && len(bytes.TrimSpace(old)) > 0
		patched, perr := []byte(nil), errNotPatchable
		if hasText {
			patched, perr = patchServer(old, h.key, NexusServerName, next, doc)
		}
		if perr == nil {
			data = patched
		} else {
			if hasText && hasJSONComments(old) {
				return fmt.Errorf("Nexus couldn't safely edit %s, so it was left as it was", filepath.Base(path))
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if e := enc.Encode(doc); e != nil {
				return fmt.Errorf("cannot encode %s", filepath.Base(path))
			}
			data = buf.Bytes()
		}
	}

	switch {
	case uninstall:
		rep.Action = "removed"
	case hasPrev:
		rep.Action = "updated"
	default:
		rep.Action = "added"
	}
	if exists && bytes.Equal(old, data) {
		rep.Action = "unchanged"
		return nil
	}
	backup := ""
	if exists {
		b, e := backupPath(path)
		if e != nil {
			return e
		}
		backup = b
	}
	if !o.DryRun {
		if e := r.commit(pendingWrite{path: path, data: data, update: exists}, backup); e != nil {
			return e
		}
	}
	rep.ChangedFiles = append(rep.ChangedFiles, path)
	if backup != "" {
		rep.Backups = append(rep.Backups, backup)
	}
	return nil
}

// reusableToken returns the scoped agent token (nxs_…) already configured
// for this server, or "" when a new one is needed. The user's session token
// is never reused: only API tokens qualify.
func reusableToken(prev map[string]any, want nexusEntry) string {
	if prev == nil {
		return ""
	}
	if want.proxy != nil {
		env := object(prev["env"])
		tok := str(env["NEXUS_TOKEN"])
		if strings.TrimRight(str(env["NEXUS_SERVER"]), "/") == want.base && strings.HasPrefix(tok, "nxs_") {
			return tok
		}
		return ""
	}
	url := ""
	for _, k := range []string{"url", "httpUrl", "serverUrl"} {
		if url = str(prev[k]); url != "" {
			break
		}
	}
	if strings.TrimRight(url, "/") != want.url {
		return ""
	}
	for _, k := range []string{"headers", "http_headers"} {
		for name, v := range object(prev[k]) {
			if strings.EqualFold(name, "Authorization") {
				tok := strings.TrimSpace(strings.TrimPrefix(str(v), "Bearer "))
				if strings.HasPrefix(tok, "nxs_") {
					return tok
				}
			}
		}
	}
	return ""
}

// merged keeps the user's extra keys on the existing entry and replaces the
// transport keys Nexus owns. A tool filter the user set is respected.
func merged(prev, want map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range prev {
		out[k] = v
	}
	for _, k := range ownedKeys {
		delete(out, k)
	}
	for k, v := range want {
		if _, kept := prev[k]; kept && k == "tools" {
			continue
		}
		out[k] = v
	}
	return out
}

// same compares two entries by their canonical JSON (sorted keys), which
// also equates TOML-decoded and freshly built values.
func same(a, b map[string]any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// ShareAllOptions control ShareAll.
type ShareAllOptions struct {
	Home, Workspace string
	Scope           string
	// Agents limits sources and targets; empty means every installed agent
	// Scan knows.
	Agents []string
	DryRun bool
}

// ShareAllResult collects every write ShareAll planned or made.
type ShareAllResult struct {
	Writes  []PlannedWrite `json:"writes"`
	Skipped []string       `json:"skipped"`
}

// ShareAll copies every skill, sub-agent, command and rules file of the
// chosen scope to each installed agent that can take it and lacks it. MCP
// servers are not copied (they may carry other services' credentials).
// Rules use each agent's main rules file only, and Nexus-managed sections
// are never re-shared, so running it again changes nothing.
func ShareAll(o ShareAllOptions) (ShareAllResult, error) {
	res := ShareAllResult{Writes: []PlannedWrite{}, Skipped: []string{}}
	scope := strings.TrimSpace(o.Scope)
	if scope == "" {
		scope = "user"
	}
	r := resolve(ScanOptions{Home: o.Home, Workspace: o.Workspace})
	if scope == "project" && r.workspace == "" {
		return res, fmt.Errorf("project scope needs a project folder")
	}
	allowed := map[string]bool{}
	for _, a := range o.Agents {
		allowed[a] = true
	}
	inv := Scan(ScanOptions{Home: o.Home, Workspace: o.Workspace})
	installed := []AgentSetup{}
	for _, a := range inv.Agents {
		if a.Installed && (len(allowed) == 0 || allowed[a.Agent]) {
			installed = append(installed, a)
		}
	}
	has := map[string]bool{} // agent|kind|name
	for _, a := range installed {
		for _, items := range [][]Item{a.SubAgents, a.Skills, a.Commands} {
			for _, i := range items {
				if i.Scope == scope {
					has[a.Agent+"|"+i.Kind+"|"+i.Name] = true
				}
			}
		}
	}
	skipped := map[string]bool{}
	note := func(s string) {
		if !skipped[s] {
			skipped[s] = true
			res.Skipped = append(res.Skipped, s)
		}
	}
	rulesSources := map[string]bool{}
	for _, a := range installed {
		for _, items := range [][]Item{a.Skills, a.SubAgents, a.Commands, a.Rules} {
			for _, i := range items {
				if i.Scope != scope {
					continue
				}
				if i.Kind == "rules" {
					main, e := targetPath(r, a.Agent, "rules", scope, i.Name)
					clean := filepath.Clean(i.Path)
					if e != nil || clean != filepath.Clean(main) || rulesSources[clean] {
						continue
					}
					rulesSources[clean] = true
				}
				to := []string{}
				for _, t := range installed {
					if t.Agent == a.Agent || !compatible(a.Agent, t.Agent, i.Kind) || has[t.Agent+"|"+i.Kind+"|"+i.Name] {
						continue
					}
					if i.Kind == "rules" {
						if _, e := targetPath(r, t.Agent, "rules", scope, i.Name); e != nil {
							continue
						}
					}
					to = append(to, t.Agent)
				}
				if len(to) == 0 {
					continue
				}
				sort.Strings(to)
				out, e := Share(ShareOptions{Home: o.Home, Workspace: o.Workspace, Item: i, From: a.Agent, To: to, Scope: scope, DryRun: o.DryRun})
				if e != nil {
					note(fmt.Sprintf("%s %s from %s: %v", i.Kind, i.Name, a.Agent, e))
					continue
				}
				res.Writes = append(res.Writes, out.Writes...)
				for _, s := range out.Skipped {
					note(s)
				}
			}
		}
	}
	return res, nil
}
