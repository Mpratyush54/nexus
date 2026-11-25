package agentsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// BrowseOptions control Browse, ReadItem and RemoveMCP.
type BrowseOptions struct {
	Home, Workspace string
	// LookPath finds agent CLIs for install detection. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
}

func (o BrowseOptions) scan() ScanOptions { return ScanOptions{Home: o.Home, Workspace: o.Workspace} }

// Browse is Scan widened to every harness Nexus can install into (Kimi,
// Copilot, Windsurf, Antigravity too), with install detection, whether the
// Nexus MCP entry is present and, per item, which other agents hold an item
// of the same kind and name. Read-only; secrets are never returned.
func Browse(o BrowseOptions) Inventory {
	r := resolve(o.scan())
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	// Scan (itself concurrent per agent), the extra harnesses and the
	// install checks all run at once; the result keeps harness order.
	var scanned Inventory
	var plugins []Item
	agents := make([]AgentSetup, len(harnesses))
	installed := make([]bool, len(harnesses))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scanned = Scan(o.scan()) }()
	go func() { defer wg.Done(); plugins = r.claudePlugins() }()
	for i, h := range harnesses {
		wg.Add(1)
		go func(i int, h harnessSpec) {
			defer wg.Done()
			installed[i] = h.installed(r, look)
			if !scanCovers(h.id) {
				a := AgentSetup{Agent: h.id, Name: h.name, SubAgents: []Item{}, Skills: []Item{}, Commands: []Item{}, MCPServers: []Item{}, Rules: []Item{}}
				r.extra(&a)
				agents[i] = a
			}
		}(i, h)
	}
	wg.Wait()
	byID := map[string]AgentSetup{}
	for _, a := range scanned.Agents {
		byID[a.Agent] = a
	}
	inv := Inventory{Agents: []AgentSetup{}}
	for i, h := range harnesses {
		a, ok := byID[h.id]
		if !ok {
			a = agents[i]
		}
		if h.id == "claude" {
			for _, it := range plugins {
				a.add(it)
			}
		}
		a.Name = h.name
		a.Installed = a.Installed || installed[i]
		for _, m := range a.MCPServers {
			if m.Name == NexusServerName {
				a.NexusMCP = true
			}
		}
		inv.Agents = append(inv.Agents, a)
	}
	markShared(inv.Agents)
	rememberInventory(o, inv)
	return inv
}

func scanCovers(id string) bool {
	switch id {
	case "claude", "codex", "cursor", "opencode", "gemini":
		return true
	}
	return false
}

// claudePlugins adds the sub-agents, skills and commands that installed
// Claude Code plugins provide (~/.claude/plugins/installed_plugins.json ->
// <installPath>/agents|skills|commands). They are listed with scope
// "plugin" and Claude Code's "plugin:name" naming; they are read-only here
// (the share flow only copies user or project items).
func (r roots) claudePlugins() []Item {
	m, e := r.config(filepath.Join(r.home, ".claude", "plugins", "installed_plugins.json"))
	if e != nil {
		return nil
	}
	type install struct{ plugin, dir string }
	var installs []install
	for _, key := range keys(object(m["plugins"])) {
		plugin := key
		if i := strings.Index(plugin, "@"); i > 0 {
			plugin = plugin[:i]
		}
		entries, _ := object(m["plugins"])[key].([]any)
		for _, raw := range entries {
			entry := object(raw)
			dir, scope := str(entry["installPath"]), str(entry["scope"])
			if dir == "" {
				continue
			}
			if scope != "" && scope != "user" {
				pp := str(entry["projectPath"])
				if r.workspace == "" || pp == "" || !strings.EqualFold(filepath.Clean(pp), filepath.Clean(r.workspace)) {
					continue
				}
			}
			installs = append(installs, install{plugin, dir})
			break // one install per plugin
		}
	}
	found := make([]AgentSetup, len(installs))
	var wg sync.WaitGroup
	for i, in := range installs {
		wg.Add(1)
		go func(p *AgentSetup, in install) {
			defer wg.Done()
			r.docs(p, "subagent", "plugin", filepath.Join(in.dir, "agents"), false, ".md")
			r.skills(p, "plugin", filepath.Join(in.dir, "skills"))
			r.docs(p, "command", "plugin", filepath.Join(in.dir, "commands"), true, ".md")
		}(&found[i], in)
	}
	wg.Wait()
	var out []Item
	for i := range found {
		for _, l := range lists(&found[i]) {
			for _, it := range *l {
				it.Name = installs[i].plugin + ":" + it.Name
				out = append(out, it)
			}
		}
	}
	return out
}

// The last inventory per workspace, so opening one item does not rescan
// every agent. RemoveMCP always rescans.
var (
	invMu    sync.Mutex
	invCache = map[string]cachedInventory{}
)

type cachedInventory struct {
	inv Inventory
	at  time.Time
}

func inventoryKey(o BrowseOptions) string {
	return strings.ToLower(o.Home + "|" + o.Workspace)
}

func rememberInventory(o BrowseOptions, inv Inventory) {
	if o.LookPath != nil {
		return // tests with fake PATHs never share the cache
	}
	invMu.Lock()
	invCache[inventoryKey(o)] = cachedInventory{inv, time.Now()}
	invMu.Unlock()
}

func recentInventory(o BrowseOptions) Inventory {
	if o.LookPath == nil {
		invMu.Lock()
		c, ok := invCache[inventoryKey(o)]
		invMu.Unlock()
		if ok && time.Since(c.at) < 2*time.Minute {
			return c.inv
		}
	}
	return Browse(o)
}

// extra scans the harnesses Scan does not cover. Paths follow each tool's
// documented locations; anything missing is simply absent.
func (r roots) extra(a *AgentSetup) {
	h, w := r.home, r.workspace
	switch a.Agent {
	case "kimi":
		r.mcps(a, "user", filepath.Join(h, ".kimi", "mcp.json"), false)
		if w != "" {
			r.rule(a, "project", filepath.Join(w, "AGENTS.md"))
		}
	case "copilot":
		d := filepath.Join(h, ".copilot")
		r.mcps(a, "user", filepath.Join(d, "mcp-config.json"), false)
		r.docs(a, "subagent", "user", filepath.Join(d, "agents"), false, ".md")
		r.rule(a, "user", filepath.Join(d, "copilot-instructions.md"))
		if w != "" {
			g := filepath.Join(w, ".github")
			r.docs(a, "subagent", "project", filepath.Join(g, "agents"), false, ".md")
			r.docs(a, "command", "project", filepath.Join(g, "prompts"), false, ".md")
			r.rule(a, "project", filepath.Join(g, "copilot-instructions.md"))
			r.docs(a, "rules", "project", filepath.Join(g, "instructions"), false, ".md")
			r.rule(a, "project", filepath.Join(w, "AGENTS.md"))
		}
		// Copilot names files foo.agent.md / foo.prompt.md / foo.instructions.md.
		for _, items := range []*[]Item{&a.SubAgents, &a.Commands, &a.Rules} {
			for i := range *items {
				it := &(*items)[i]
				for _, suf := range []string{".agent", ".prompt", ".instructions"} {
					it.Name = strings.TrimSuffix(it.Name, suf)
				}
			}
		}
	case "windsurf":
		d := filepath.Join(h, ".codeium", "windsurf")
		r.mcps(a, "user", filepath.Join(d, "mcp_config.json"), false)
		r.rule(a, "user", filepath.Join(d, "memories", "global_rules.md"))
		if w != "" {
			r.rule(a, "project", filepath.Join(w, ".windsurfrules"))
			r.docs(a, "rules", "project", filepath.Join(w, ".windsurf", "rules"), false, ".md")
			r.docs(a, "command", "project", filepath.Join(w, ".windsurf", "workflows"), false, ".md")
		}
	case "antigravity":
		d := filepath.Join(h, ".gemini", "antigravity")
		r.mcps(a, "user", filepath.Join(d, "mcp_config.json"), false)
		r.rule(a, "user", filepath.Join(h, ".gemini", "GEMINI.md"))
		if w != "" {
			r.docs(a, "rules", "project", filepath.Join(w, ".agent", "rules"), false, ".md")
			r.docs(a, "command", "project", filepath.Join(w, ".agent", "workflows"), false, ".md")
		}
	}
}

func lists(a *AgentSetup) []*[]Item {
	return []*[]Item{&a.SubAgents, &a.Skills, &a.Commands, &a.MCPServers, &a.Rules}
}

// markShared fills SharedWith: other agents with an item of the same kind
// and name (case-insensitive), or the very same file.
func markShared(agents []AgentSetup) {
	holders := map[string]map[string]bool{}
	add := func(k, agent string) {
		if holders[k] == nil {
			holders[k] = map[string]bool{}
		}
		holders[k][agent] = true
	}
	for i := range agents {
		for _, l := range lists(&agents[i]) {
			for _, it := range *l {
				add(it.Kind+"|"+strings.ToLower(it.Name), agents[i].Agent)
				if it.Kind == "rules" {
					add("path|"+strings.ToLower(filepath.Clean(it.Path)), agents[i].Agent)
				}
			}
		}
	}
	for i := range agents {
		self := agents[i].Agent
		for _, l := range lists(&agents[i]) {
			for j := range *l {
				it := &(*l)[j]
				set := map[string]bool{}
				for a := range holders[it.Kind+"|"+strings.ToLower(it.Name)] {
					set[a] = true
				}
				if it.Kind == "rules" {
					for a := range holders["path|"+strings.ToLower(filepath.Clean(it.Path))] {
						set[a] = true
					}
				}
				delete(set, self)
				it.SharedWith = nil
				for a := range set {
					it.SharedWith = append(it.SharedWith, a)
				}
				sort.Strings(it.SharedWith)
			}
		}
	}
}

// find returns the listed item, or false. Only paths Browse returned can
// ever be read or changed through ReadItem / RemoveMCP.
func find(inv Inventory, agent, kind, name, path string) (Item, bool) {
	clean := filepath.Clean(path)
	for i := range inv.Agents {
		a := &inv.Agents[i]
		if a.Agent != agent {
			continue
		}
		for _, l := range lists(a) {
			for _, it := range *l {
				if it.Kind == kind && it.Name == name && strings.EqualFold(filepath.Clean(it.Path), clean) {
					return it, true
				}
			}
		}
	}
	return Item{}, false
}

// ItemContent is what the detail pane shows for one item.
type ItemContent struct {
	Path string `json:"path"`
	// Format is markdown, toml, json or text.
	Format    string `json:"format"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
	// Masked counts values replaced with the mask.
	Masked int `json:"masked"`
}

// Mask replaces every secret value in ReadItem output.
const Mask = "••••••"

// maxShow bounds the text returned for one file.
const maxShow = 256 << 10

// ReadItem returns one listed item's content, read-only and masked. MCP
// servers (and agents defined inside a config file) return only their own
// entry with every env, header and credential-like value masked, never the
// surrounding config file. Paths not returned by Browse are refused.
func ReadItem(o BrowseOptions, agent, kind, name, path string) (ItemContent, error) {
	r := resolve(o.scan())
	it, ok := find(recentInventory(o), agent, kind, name, path)
	if !ok {
		// The cached inventory may predate a new file: rescan once.
		if it, ok = find(Browse(o), agent, kind, name, path); !ok {
			return ItemContent{}, fmt.Errorf("that item is not in the agent inventory")
		}
	}
	out := ItemContent{Path: it.Path}
	ext := strings.ToLower(filepath.Ext(it.Path))
	if kind == "mcp" || ext == ".json" || ext == ".jsonc" || (ext == ".toml" && kind != "command") {
		m, e := r.config(it.Path)
		if e != nil {
			return out, fmt.Errorf("cannot read %s", filepath.Base(it.Path))
		}
		var raw map[string]any
		if kind == "mcp" {
			ws := ""
			if agent == "claude" && it.Scope == "project" && filepath.Base(it.Path) == ".claude.json" {
				ws = r.workspace
			}
			raw = object(mcpMap(m, agent, it.Scope, ws)[name])
		} else if kind == "subagent" {
			raw = object(object(m["agent"])[name])
		}
		if raw == nil {
			return out, fmt.Errorf("%s is no longer in %s", name, filepath.Base(it.Path))
		}
		masked := maskValue(raw, "", &out.Masked)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if e := enc.Encode(map[string]any{name: masked}); e != nil {
			return out, fmt.Errorf("cannot show entry")
		}
		out.Format, out.Content = "json", buf.String()
		return out, nil
	}
	b, e := r.read(it.Path)
	if e != nil {
		return out, fmt.Errorf("cannot read %s", filepath.Base(it.Path))
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	if len(b) > maxShow {
		b, out.Truncated = b[:maxShow], true
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	out.Content = maskText(text, &out.Masked)
	switch ext {
	case ".md", ".mdc":
		out.Format = "markdown"
	case ".toml":
		out.Format = "toml"
	default:
		out.Format = "text"
	}
	return out, nil
}

var (
	secretKey      = regexp.MustCompile(`(?i)(token|secret|passw|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credential|auth|cookie|session)`)
	bearerRe       = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=\-]{8,}`)
	knownTokenRe   = regexp.MustCompile(`\b(sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9\-]{10,}|nxs_[A-Za-z0-9_\-]{8,}|glpat-[A-Za-z0-9_\-]{16,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_\-]{30,}|eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{5,})`)
	assignRe       = regexp.MustCompile(`(?i)\b([A-Z0-9_\-]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|ACCESS_?KEY|PRIVATE_?KEY|CREDENTIALS?)[A-Z0-9_\-]*["']?\s*[:=]\s*["']?)([^\s"',;&#]{4,})`)
	urlUserinfoRe  = regexp.MustCompile(`(://)[^/@\s:]+:[^/@\s]+@`)
	urlQuerySecret = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_\-]*(?:token|key|secret|sig|signature|password|auth)[A-Za-z0-9_\-]*=)[^&#\s"']+`)
)

// maskText replaces token-shaped substrings: Authorization schemes,
// well-known key prefixes, NAME=value assignments whose name looks like a
// credential, URL userinfo and credential-like query parameters.
func maskText(s string, n *int) string {
	count := func(re *regexp.Regexp, repl string) {
		*n += len(re.FindAllStringIndex(s, -1))
		s = re.ReplaceAllString(s, repl)
	}
	count(bearerRe, "${1}${2}"+Mask)
	count(knownTokenRe, Mask)
	s = assignRe.ReplaceAllStringFunc(s, func(m string) string {
		g := assignRe.FindStringSubmatch(m)
		// Prose ("password: required") stays; values that look generated go.
		if len(g[2]) < 8 || (!strings.ContainsAny(g[2], "0123456789") && len(g[2]) < 20) {
			return m
		}
		*n++
		return g[1] + Mask
	})
	count(urlUserinfoRe, "${1}"+Mask+"@")
	count(urlQuerySecret, "${1}"+Mask)
	return s
}

func isContainer(k string) bool {
	l := strings.ToLower(k)
	return l == "env" || l == "environment" || strings.Contains(l, "header")
}

// maskValue copies a config entry, masking every value under env/headers,
// every value whose key looks like a credential, the value after a
// --token/--api-key style argument, and token-shaped text anywhere else.
func maskValue(v any, key string, n *int) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if isContainer(k) || secretKey.MatchString(k) {
				out[k] = maskAll(val, n)
				continue
			}
			out[k] = maskValue(val, k, n)
		}
		return out
	case []any:
		out := make([]any, len(x))
		prevSecret := false
		for i, val := range x {
			s, isStr := val.(string)
			if isStr && prevSecret && !strings.HasPrefix(s, "-") {
				out[i] = Mask
				*n++
				prevSecret = false
				continue
			}
			out[i] = maskValue(val, key, n)
			prevSecret = isStr && strings.HasPrefix(s, "-") && !strings.Contains(s, "=") && secretKey.MatchString(s)
		}
		return out
	case string:
		return maskText(x, n)
	}
	return v
}

// maskAll keeps structure (so names stay visible) and masks every scalar.
func maskAll(v any, n *int) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = maskAll(val, n)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = maskAll(val, n)
		}
		return out
	case nil:
		return nil
	case bool:
		return x
	}
	*n++
	return Mask
}

// RemoveResult reports what RemoveMCP changed or would change.
type RemoveResult struct {
	Agent  string `json:"agent"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Backup string `json:"backup,omitempty"`
	// Action is "removed" (or "will remove" for a dry run).
	Action string `json:"action"`
	Nexus  bool   `json:"nexus"`
}

// RemoveMCP deletes one listed MCP server entry from one agent config,
// backing the file up first. Everything else in the file is kept. DryRun
// only reports.
func RemoveMCP(o BrowseOptions, agent, name, path string, dryRun bool) (RemoveResult, error) {
	res := RemoveResult{Agent: agent, Name: name, Path: path, Nexus: name == NexusServerName}
	r := resolve(o.scan())
	it, ok := find(Browse(o), agent, "mcp", name, path)
	if !ok {
		return res, fmt.Errorf("that MCP server is not in the agent inventory")
	}
	res.Path = it.Path
	if !r.safe(it.Path) {
		return res, fmt.Errorf("config path is outside the home and project folders")
	}
	old, e := r.read(it.Path)
	if e != nil {
		return res, fmt.Errorf("cannot read %s", filepath.Base(it.Path))
	}
	var data []byte
	if agent == "codex" && strings.HasSuffix(it.Path, ".toml") {
		data = cutTable(old, name)
		var check map[string]any
		if _, e := toml.Decode(string(data), &check); e != nil || object(object(check["mcp_servers"])[name]) != nil {
			return res, fmt.Errorf("cannot safely remove %s from %s", name, filepath.Base(it.Path))
		}
	} else {
		doc, e := r.config(it.Path)
		if e != nil {
			return res, fmt.Errorf("%s is not valid; left untouched", filepath.Base(it.Path))
		}
		ws := ""
		if agent == "claude" && it.Scope == "project" && filepath.Base(it.Path) == ".claude.json" {
			ws = r.workspace
		}
		servers := mcpMap(doc, agent, it.Scope, ws)
		if _, ok := servers[name]; !ok {
			return res, fmt.Errorf("%s is no longer in %s", name, filepath.Base(it.Path))
		}
		delete(servers, name) // the map is shared with doc
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if e := enc.Encode(doc); e != nil {
			return res, fmt.Errorf("cannot encode %s", filepath.Base(it.Path))
		}
		data = buf.Bytes()
	}
	backup, e := backupPath(it.Path)
	if e != nil {
		return res, e
	}
	if dryRun {
		res.Action = "will remove"
		return res, nil
	}
	if e := r.commit(pendingWrite{path: it.Path, data: data, update: true}, backup); e != nil {
		return res, e
	}
	res.Action, res.Backup = "removed", backup
	return res, nil
}
