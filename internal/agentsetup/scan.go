// Package agentsetup inventories and shares coding-agent configuration.
package agentsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// Config files such as ~/.claude.json grow with use; they must be read whole.
const maxRead = 8 << 20

type ScanOptions struct{ Home, Workspace, AppData string }
type Inventory struct {
	Agents []AgentSetup `json:"agents"`
}
type AgentSetup struct {
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	// NexusMCP and Version are filled by Browse only.
	NexusMCP   bool   `json:"nexus_mcp,omitempty"`
	Version    string `json:"version,omitempty"`
	SubAgents  []Item `json:"subagents"`
	Skills     []Item `json:"skills"`
	Commands   []Item `json:"commands"`
	MCPServers []Item `json:"mcp_servers"`
	Rules      []Item `json:"rules"`
}
type Item struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Scope       string   `json:"scope"`
	Path        string   `json:"path"`
	Transport   string   `json:"transport,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	URL         string   `json:"url,omitempty"`
	EnvKeys     []string `json:"env_keys,omitempty"`
	HeaderKeys  []string `json:"header_keys,omitempty"`
	// Disabled: the server is configured but turned off.
	Disabled bool `json:"disabled,omitempty"`
	// Parked: turned off by Nexus, which keeps the entry aside until it
	// is turned on again (agents without an on/off setting).
	Parked bool `json:"parked,omitempty"`
	// SharedWith lists the other agents holding an item of the same kind
	// and name (filled by Browse).
	SharedWith []string `json:"shared_with,omitempty"`
}

type roots struct{ home, workspace string }

func resolve(o ScanOptions) roots {
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	h, _ := filepath.Abs(o.Home)
	w := ""
	if o.Workspace != "" {
		w, _ = filepath.Abs(o.Workspace)
	}
	return roots{h, w}
}
func within(root, p string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Resolve every existing ancestor, including junctions, before accepting a path.
func (r roots) safe(p string) bool {
	p, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	for _, root := range []string{r.home, r.workspace} {
		if !within(root, p) {
			continue
		}
		rr, err := realRoot(root)
		if err != nil {
			continue
		}
		cur := p
		for {
			_, e := os.Lstat(cur)
			if e == nil {
				break
			}
			if !os.IsNotExist(e) {
				return false
			}
			next := filepath.Dir(cur)
			if next == cur {
				return false
			}
			cur = next
		}
		real, err := filepath.EvalSymlinks(cur)
		if err == nil && within(rr, real) {
			return true
		}
	}
	return false
}

// realRoots memoizes EvalSymlinks of the home and workspace roots: safe()
// runs once per file, and the roots themselves do not move while scanning.
var realRoots sync.Map

func realRoot(root string) (string, error) {
	if v, ok := realRoots.Load(root); ok {
		return v.(string), nil
	}
	rr, err := filepath.EvalSymlinks(root)
	if err == nil {
		realRoots.Store(root, rr)
	}
	return rr, err
}

func (r roots) read(p string) ([]byte, error) {
	if !r.safe(p) {
		return nil, fmt.Errorf("path outside allowed roots")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxRead {
		return nil, fmt.Errorf("not a bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if len(b) > maxRead {
		return nil, fmt.Errorf("file too large")
	}
	return b, err
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func str(v any) string            { s, _ := v.(string); return s }
func stringsOf(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, v := range x {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, x...)
	}
	return out
}
func keys(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stripJSONComments is string-aware: URLs and escaped quotes remain intact.
func stripJSONComments(b []byte) []byte {
	out := append([]byte(nil), b...)
	quoted, escaped := false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c == '/' && i+1 < len(out) && out[i+1] == '/' {
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		} else if c == '/' && i+1 < len(out) && out[i+1] == '*' {
			for i < len(out) && !(out[i] == '*' && i+1 < len(out) && out[i+1] == '/') {
				if out[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
			if i+1 < len(out) {
				out[i], out[i+1] = ' ', ' '
				i++
			}
		}
	}
	return out
}
func (r roots) config(p string) (map[string]any, error) {
	b, e := r.read(p)
	if e != nil {
		return nil, e
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	var m map[string]any
	if strings.HasSuffix(p, ".toml") {
		_, e = toml.Decode(string(b), &m)
	} else {
		// Gemini CLI, Cursor and OpenCode all read settings with comments
		// (and tolerate trailing commas), so a commented settings.json is
		// valid, not broken.
		m, e = decodeJSONC(b)
	}
	if e != nil || m == nil {
		return nil, fmt.Errorf("invalid configuration")
	}
	return m, nil
}
func markdown(b []byte) (map[string]any, string, error) {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	m := map[string]any{}
	if strings.HasPrefix(s, "---\n") {
		end := strings.Index(s[4:], "\n---")
		if end < 0 {
			return nil, "", fmt.Errorf("unterminated frontmatter")
		}
		end += 4
		if e := yaml.Unmarshal([]byte(s[4:end]), &m); e != nil {
			return nil, "", fmt.Errorf("invalid frontmatter")
		}
		s = strings.TrimPrefix(s[end+4:], "\n")
	}
	return m, s, nil
}
func description(m map[string]any, body string) string {
	if s := str(m["description"]); s != "" {
		return s
	}
	for _, line := range strings.Split(body, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return strings.TrimSpace(strings.TrimLeft(s, "#"))
		}
	}
	return ""
}
func (r roots) files(dir string, recursive bool, visit func(string)) {
	if !r.safe(dir) {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if p != dir && d.IsDir() && !recursive {
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			visit(p)
		}
		return nil
	})
}
func (r roots) docs(a *AgentSetup, kind, scope, dir string, recursive bool, extensions ...string) {
	r.files(dir, recursive, func(p string) {
		ext := filepath.Ext(p)
		ok := false
		for _, x := range extensions {
			if ext == x {
				ok = true
			}
		}
		if !ok {
			return
		}
		b, e := r.read(p)
		if e != nil {
			return
		}
		m, body, e := markdown(b)
		if e != nil {
			return
		}
		rel, _ := filepath.Rel(dir, p)
		name := strings.TrimSuffix(filepath.ToSlash(rel), ext)
		if s := str(m["name"]); s != "" {
			name = s
		}
		it := Item{Kind: kind, Name: name, Description: description(m, body), Scope: scope, Path: p}
		a.add(it)
	})
}
func (a *AgentSetup) add(i Item) {
	switch i.Kind {
	case "subagent":
		a.SubAgents = append(a.SubAgents, i)
	case "skill":
		a.Skills = append(a.Skills, i)
	case "command":
		a.Commands = append(a.Commands, i)
	case "mcp":
		a.MCPServers = append(a.MCPServers, i)
	case "rules":
		a.Rules = append(a.Rules, i)
	}
}
func (r roots) rule(a *AgentSetup, scope, p string) {
	b, e := r.read(p)
	if e != nil {
		return
	}
	m, body, e := markdown(b)
	if e != nil {
		return
	}
	a.add(Item{Kind: "rules", Name: filepath.Base(p), Description: description(m, body), Scope: scope, Path: p})
}
func (r roots) skills(a *AgentSetup, scope, dir string) {
	ds, e := os.ReadDir(dir)
	if e != nil || !r.safe(dir) {
		return
	}
	for _, d := range ds {
		p := filepath.Join(dir, d.Name(), "SKILL.md")
		b, e := r.read(p)
		if e != nil {
			continue
		}
		m, body, e := markdown(b)
		if e != nil {
			continue
		}
		name := str(m["name"])
		if name == "" {
			name = d.Name()
		}
		a.add(Item{Kind: "skill", Name: name, Description: description(m, body), Scope: scope, Path: p})
	}
}
func mcpMap(m map[string]any, agent, scope, ws string) map[string]any {
	if agent == "opencode" {
		return object(m["mcp"])
	}
	if agent == "codex" {
		return object(m["mcp_servers"])
	}
	if agent == "claude" && scope == "project" && ws != "" {
		for p, v := range object(m["projects"]) {
			if abs, e := filepath.Abs(p); e == nil && strings.EqualFold(filepath.Clean(abs), filepath.Clean(ws)) {
				return object(object(v)["mcpServers"])
			}
		}
		return nil
	}
	return object(m["mcpServers"])
}
func normalized(raw map[string]any, agent string) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(raw)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if agent == "opencode" {
		if str(m["type"]) == "local" {
			c := stringsOf(m["command"])
			if len(c) > 0 {
				m["command"] = c[0]
				m["args"] = c[1:]
			}
			m["type"] = "stdio"
		} else {
			m["type"] = "http"
		}
		if v, ok := m["environment"]; ok {
			m["env"] = v
			delete(m, "environment")
		}
	}
	if v, ok := m["serverUrl"]; ok && str(m["url"]) == "" {
		m["url"] = v
		delete(m, "serverUrl")
	}
	if t := str(m["transport"]); t != "" && str(m["type"]) == "" {
		m["type"] = t
	}
	if v, ok := m["httpUrl"]; ok {
		m["url"] = v
		m["type"] = "http"
		delete(m, "httpUrl")
	}
	if agent == "gemini" && str(m["url"]) != "" && str(m["type"]) == "" {
		m["type"] = "sse"
	}
	if v, ok := m["http_headers"]; ok {
		m["headers"] = v
		delete(m, "http_headers")
	}
	if str(m["type"]) == "" {
		if str(m["url"]) != "" {
			m["type"] = "http"
		} else {
			m["type"] = "stdio"
		}
	}
	return m
}
func secretValues(v any) []string {
	var out []string
	var walk func(any, bool)
	walk = func(v any, secret bool) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				l := strings.ToLower(k)
				walk(v, secret || l == "env" || l == "environment" || strings.Contains(l, "header") || strings.Contains(l, "token") || strings.Contains(l, "secret") || strings.Contains(l, "password") || strings.Contains(l, "api_key"))
			}
		case []any:
			for _, v := range x {
				walk(v, secret)
			}
		case string:
			if secret && x != "" {
				out = append(out, x)
			}
		}
	}
	walk(v, false)
	return out
}
func redact(s string, secrets []string) string {
	for _, v := range secrets {
		// Flags and short values ("1", "true") are not secrets, and
		// replacing them everywhere would garble paths and commands.
		if len(v) < 4 {
			continue
		}
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}
func publicURL(s string) string {
	u, e := url.Parse(s)
	if e != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func (r roots) mcps(a *AgentSetup, scope, p string, projectEntry bool) {
	m, e := r.config(p)
	if e != nil {
		return
	}
	ws := ""
	if projectEntry {
		ws = r.workspace
	}
	for _, name := range keys(mcpMap(m, a.Agent, scope, ws)) {
		raw := object(mcpMap(m, a.Agent, scope, ws)[name])
		if raw == nil {
			continue
		}
		a.add(mcpItem(raw, a.Agent, scope, p, name))
	}
}

// mcpItem lists one MCP server entry without any secret value.
func mcpItem(raw map[string]any, agent, scope, p, name string) Item {
	n := normalized(raw, agent)
	secrets := secretValues(raw)
	i := Item{Kind: "mcp", Name: redact(name, secrets), Scope: scope, Path: p, Transport: str(n["type"]), Command: redact(str(n["command"]), secrets), Args: stringsOf(n["args"]), URL: redact(publicURL(str(n["url"])), secrets), EnvKeys: keys(object(n["env"])), HeaderKeys: keys(object(n["headers"])), Disabled: !entryEnabled(raw)}
	for j := range i.Args {
		i.Args[j] = redact(i.Args[j], secrets)
		if strings.Contains(strings.ToLower(i.Args[j]), "token=") || strings.Contains(strings.ToLower(i.Args[j]), "key=") {
			i.Args[j] = "[redacted]"
		} else if j > 0 {
			prev := strings.ToLower(i.Args[j-1])
			if strings.Contains(prev, "token") || strings.Contains(prev, "password") || strings.Contains(prev, "secret") || strings.Contains(prev, "api-key") {
				i.Args[j] = "[redacted]"
			}
		}
	}
	return i
}

// entryEnabled reads an entry's own on/off setting: "enabled" (Codex,
// OpenCode) or "disabled" (Windsurf and others). Missing means on.
func entryEnabled(raw map[string]any) bool {
	if v, ok := raw["enabled"].(bool); ok && !v {
		return false
	}
	if v, ok := raw["disabled"].(bool); ok && v {
		return false
	}
	return true
}

// Scan tolerates missing, unreadable, malformed and oversized files independently.
// AppData is reserved for future VS Code integrations; Cursor uses ~/.cursor.
// Agents are scanned concurrently; the result keeps the fixed order.
func Scan(opts ScanOptions) Inventory {
	r := resolve(opts)
	ids := []string{"claude", "codex", "cursor", "opencode", "gemini"}
	names := []string{"Claude Code", "Codex", "Cursor", "OpenCode", "Gemini CLI"}
	dirs := []string{".claude", ".codex", ".cursor", filepath.Join(".config", "opencode"), ".gemini"}
	agents := make([]AgentSetup, len(ids))
	var wg sync.WaitGroup
	for idx := range ids {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agents[idx] = r.scanAgent(ids[idx], names[idx], dirs[idx])
		}(idx)
	}
	wg.Wait()
	return Inventory{Agents: agents}
}

func (r roots) scanAgent(id, name, dir string) AgentSetup {
	a := AgentSetup{Agent: id, Name: name, SubAgents: []Item{}, Skills: []Item{}, Commands: []Item{}, MCPServers: []Item{}, Rules: []Item{}}
	u := filepath.Join(r.home, dir)
	if st, e := os.Stat(u); e == nil && st.IsDir() && r.safe(u) {
		a.Installed = true
	}
	for _, scope := range []string{"user", "project"} {
		base := r.home
		if scope == "project" {
			base = r.workspace
			if base == "" {
				continue
			}
		}
		d := filepath.Join(base, "."+id)
		if scope == "user" {
			d = u
		}
		switch id {
		case "claude":
			r.docs(&a, "subagent", scope, filepath.Join(d, "agents"), false, ".md")
			r.skills(&a, scope, filepath.Join(d, "skills"))
			r.docs(&a, "command", scope, filepath.Join(d, "commands"), true, ".md")
			r.rule(&a, scope, filepath.Join(d, "CLAUDE.md"))
			if scope == "user" {
				r.mcps(&a, scope, filepath.Join(base, ".claude.json"), false)
			} else {
				r.rule(&a, scope, filepath.Join(base, "CLAUDE.md"))
				r.mcps(&a, scope, filepath.Join(base, ".mcp.json"), false)
				r.mcps(&a, scope, filepath.Join(r.home, ".claude.json"), true)
			}
		case "codex":
			if scope == "user" {
				r.mcps(&a, scope, filepath.Join(d, "config.toml"), false)
				r.docs(&a, "command", scope, filepath.Join(d, "prompts"), false, ".md")
				r.skills(&a, scope, filepath.Join(d, "skills"))
				r.rule(&a, scope, filepath.Join(d, "AGENTS.md"))
			} else {
				r.rule(&a, scope, filepath.Join(base, "AGENTS.md"))
			}
		case "cursor":
			r.mcps(&a, scope, filepath.Join(d, "mcp.json"), false)
			r.docs(&a, "command", scope, filepath.Join(d, "commands"), false, ".md")
			if scope == "project" {
				r.rule(&a, scope, filepath.Join(base, ".cursorrules"))
				r.docs(&a, "rules", scope, filepath.Join(d, "rules"), false, ".md", ".mdc")
			}
		case "opencode":
			p := filepath.Join(d, "opencode.json")
			if scope == "project" {
				p = filepath.Join(base, "opencode.json")
			}
			if _, e := os.Lstat(p); os.IsNotExist(e) {
				p += "c"
			}
			r.mcps(&a, scope, p, false)
			if m, e := r.config(p); e == nil {
				for _, name := range keys(object(m["agent"])) {
					v := object(object(m["agent"])[name])
					if v != nil {
						a.add(Item{Kind: "subagent", Name: name, Description: redact(str(v["description"]), secretValues(v)), Scope: scope, Path: p})
					}
				}
			}
			for _, sub := range []string{"agent", "agents"} {
				r.docs(&a, "subagent", scope, filepath.Join(d, sub), false, ".md")
			}
			for _, sub := range []string{"command", "commands"} {
				r.docs(&a, "command", scope, filepath.Join(d, sub), true, ".md")
			}
			if scope == "project" {
				r.rule(&a, scope, filepath.Join(base, "AGENTS.md"))
			}
		case "gemini":
			r.mcps(&a, scope, filepath.Join(d, "settings.json"), false)
			if scope == "user" {
				r.files(filepath.Join(d, "commands"), false, func(p string) {
					if filepath.Ext(p) != ".toml" {
						return
					}
					m, e := r.config(p)
					if e != nil || str(m["prompt"]) == "" {
						return
					}
					a.add(Item{Kind: "command", Name: strings.TrimSuffix(filepath.Base(p), ".toml"), Description: str(m["description"]), Scope: scope, Path: p})
				})
				r.rule(&a, scope, filepath.Join(d, "GEMINI.md"))
			} else {
				r.rule(&a, scope, filepath.Join(base, "GEMINI.md"))
			}
		}
	}
	return a
}
