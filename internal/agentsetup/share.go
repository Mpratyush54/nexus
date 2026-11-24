package agentsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

type ShareOptions struct {
	Home, Workspace, AppData string
	Item                     Item
	From                     string
	To                       []string
	Scope                    string
	DryRun                   bool
}
type ShareResult struct {
	Writes  []PlannedWrite `json:"writes"`
	Skipped []string       `json:"skipped"`
}
type PlannedWrite struct {
	Agent, Path, Action string
	Backup              string
}
type pendingWrite struct {
	path   string
	data   []byte
	update bool
}

func targetPath(r roots, agent, kind, scope, name string) (string, error) {
	base := r.home
	if scope == "project" {
		base = r.workspace
		if base == "" {
			return "", fmt.Errorf("project scope requires Workspace")
		}
	}
	d := filepath.Join(base, "."+agent)
	switch kind {
	case "mcp":
		switch agent {
		case "claude":
			if scope == "user" {
				return filepath.Join(base, ".claude.json"), nil
			}
			return filepath.Join(base, ".mcp.json"), nil
		case "codex":
			if scope == "project" {
				return "", fmt.Errorf("Codex MCP sharing supports user scope only")
			}
			return filepath.Join(d, "config.toml"), nil
		case "cursor":
			return filepath.Join(d, "mcp.json"), nil
		case "opencode":
			if scope == "user" {
				return filepath.Join(base, ".config", "opencode", "opencode.json"), nil
			}
			return filepath.Join(base, "opencode.json"), nil
		case "gemini":
			return filepath.Join(d, "settings.json"), nil
		}
	case "skill":
		if agent == "claude" || agent == "codex" {
			return filepath.Join(d, "skills", name, "SKILL.md"), nil
		}
	case "subagent":
		if agent == "claude" {
			return filepath.Join(d, "agents", name+".md"), nil
		}
		if agent == "opencode" {
			if scope == "user" {
				d = filepath.Join(base, ".config", "opencode")
			}
			return filepath.Join(d, "agent", name+".md"), nil
		}
	case "command":
		switch agent {
		case "claude", "cursor":
			return filepath.Join(d, "commands", name+".md"), nil
		case "codex":
			if scope == "project" {
				return "", fmt.Errorf("Codex prompts support user scope only")
			}
			return filepath.Join(d, "prompts", name+".md"), nil
		case "opencode":
			if scope == "user" {
				d = filepath.Join(base, ".config", "opencode")
			}
			return filepath.Join(d, "command", name+".md"), nil
		case "gemini":
			return filepath.Join(d, "commands", name+".toml"), nil
		}
	case "rules":
		switch agent {
		case "claude":
			if scope == "user" {
				base = d
			}
			return filepath.Join(base, "CLAUDE.md"), nil
		case "codex", "opencode":
			if scope == "user" {
				if agent == "opencode" {
					base = filepath.Join(base, ".config", "opencode")
				} else {
					base = d
				}
			}
			return filepath.Join(base, "AGENTS.md"), nil
		case "gemini":
			if scope == "user" {
				base = d
			}
			return filepath.Join(base, "GEMINI.md"), nil
		case "cursor":
			if scope == "user" {
				return "", fmt.Errorf("Cursor rules sharing supports project scope only")
			}
			return filepath.Join(base, ".cursorrules"), nil
		}
	}
	return "", fmt.Errorf("%s has no supported %s conversion", agent, kind)
}
func validName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00\r\n<>\"|?*") {
		return false
	}
	return !strings.HasSuffix(name, ".") && !strings.HasSuffix(name, " ")
}

func validCommandName(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if !validName(part) {
			return false
		}
	}
	return true
}
func compatible(from, to, kind string) bool {
	switch kind {
	case "mcp", "rules":
		return true
	case "skill":
		return (from == "claude" && to == "codex") || (from == "codex" && to == "claude")
	case "subagent":
		return (from == "claude" && to == "opencode") || (from == "opencode" && to == "claude")
	case "command":
		return from != "gemini"
	}
	return false
}
func sourceItem(o ShareOptions) (Item, bool) {
	for _, a := range Scan(ScanOptions{o.Home, o.Workspace, o.AppData}).Agents {
		if a.Agent != o.From {
			continue
		}
		for _, items := range [][]Item{a.SubAgents, a.Skills, a.Commands, a.MCPServers, a.Rules} {
			for _, i := range items {
				if i.Kind == o.Item.Kind && i.Name == o.Item.Name && filepath.Clean(i.Path) == filepath.Clean(o.Item.Path) && i.Scope == o.Item.Scope {
					return i, true
				}
			}
		}
	}
	return Item{}, false
}
func mcpSource(r roots, i Item, agent string) (map[string]any, error) {
	m, e := r.config(i.Path)
	if e != nil {
		return nil, fmt.Errorf("cannot read source configuration")
	}
	ws := ""
	if agent == "claude" && i.Scope == "project" && filepath.Base(i.Path) == ".claude.json" {
		ws = r.workspace
	}
	raw := object(mcpMap(m, agent, i.Scope, ws)[i.Name])
	if raw == nil {
		return nil, fmt.Errorf("source MCP server no longer exists")
	}
	return normalized(raw, agent), nil
}
func encodeMCP(n map[string]any, agent string) map[string]any {
	b, _ := json.Marshal(n)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if agent == "opencode" {
		if str(m["type"]) == "stdio" {
			m["command"] = append([]string{str(m["command"])}, stringsOf(m["args"])...)
			delete(m, "args")
			m["type"] = "local"
		} else {
			m["type"] = "remote"
		}
		if v, ok := m["env"]; ok {
			m["environment"] = v
			delete(m, "env")
		}
	}
	if agent == "codex" {
		delete(m, "type")
		if v, ok := m["headers"]; ok {
			m["http_headers"] = v
			delete(m, "headers")
		}
	}
	if agent == "gemini" {
		if str(m["type"]) == "http" {
			m["httpUrl"] = m["url"]
			delete(m, "url")
		}
		delete(m, "type")
	}
	return m
}

// replaceTable identifies headers using the TOML parser, not string prefixes, so
// quoted names, sibling tables and nested env/header tables remain unambiguous.
func replaceTable(b []byte, name string, raw map[string]any) ([]byte, error) {
	var block bytes.Buffer
	if e := toml.NewEncoder(&block).Encode(map[string]any{"mcp_servers": map[string]any{name: raw}}); e != nil {
		return nil, fmt.Errorf("cannot encode MCP table")
	}
	out := cutTable(b, name)
	// The encoder emits an empty parent table. Repeating that parent on a
	// subsequent share would be invalid TOML, so append only server tables.
	serverBlock := bytes.Replace(block.Bytes(), []byte("[mcp_servers]\n"), nil, 1)
	out = append(out, serverBlock...)
	var check map[string]any
	if _, e := toml.Decode(string(out), &check); e != nil {
		return nil, fmt.Errorf("cannot safely replace MCP table")
	}
	return out, nil
}

// cutTable removes [mcp_servers.<name>] and its sub-tables, keeping every
// other byte, and ends the result with a newline.
func cutTable(b []byte, name string) []byte {
	lines := strings.SplitAfter(string(b), "\n")
	start := make([]int, len(lines))
	offset := 0
	for j, l := range lines {
		start[j] = offset
		offset += len(l)
	}
	type cut struct{ a, b int }
	cuts := []cut{}
	active := -1
	for j, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "[") {
			continue
		}
		var probe map[string]any
		md, e := toml.Decode(t+"\n", &probe)
		if e != nil {
			continue
		}
		ks := md.Keys()
		if len(ks) == 0 {
			continue
		}
		k := ks[0]
		belongs := len(k) >= 2 && k[0] == "mcp_servers" && k[1] == name
		if active >= 0 {
			cuts = append(cuts, cut{active, start[j]})
			active = -1
		}
		if belongs {
			active = start[j]
		}
	}
	if active >= 0 {
		cuts = append(cuts, cut{active, len(b)})
	}
	out := []byte{}
	prev := 0
	for _, c := range cuts {
		out = append(out, b[prev:c.a]...)
		prev = c.b
	}
	out = append(out, b[prev:]...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return out
}
func convertedAgent(b []byte, to string) ([]byte, error) {
	m, body, e := markdown(b)
	if e != nil {
		return nil, e
	}
	out := map[string]any{}
	for _, k := range []string{"name", "description", "model"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if to == "opencode" {
		tools := stringsOf(m["tools"])
		if s := str(m["tools"]); s != "" {
			tools = strings.Split(s, ",")
		}
		tm := map[string]bool{}
		for _, t := range tools {
			if t = strings.TrimSpace(t); t != "" {
				tm[t] = true
			}
		}
		out["tools"] = tm
	} else {
		tools := []string{}
		for k, v := range object(m["tools"]) {
			if v == true {
				tools = append(tools, k)
			}
		}
		sort.Strings(tools)
		out["tools"] = tools
	}
	fm, e := yaml.Marshal(out)
	if e != nil {
		return nil, fmt.Errorf("cannot encode frontmatter")
	}
	return []byte("---\n" + string(fm) + "---\n" + body), nil
}
func rulesContent(old, body []byte, from string) []byte {
	if bytes.Equal(old, body) {
		return old
	}
	heading := "## Shared from " + from + " (Nexus)"
	marker := "<!-- End shared from " + from + " (Nexus) -->"
	s := strings.ReplaceAll(string(old), "\r\n", "\n")
	section := heading + "\n\n" + strings.TrimSpace(string(body)) + "\n" + marker + "\n"
	lines := strings.SplitAfter(s, "\n")
	pos := 0
	start, end := -1, len(s)
	for _, l := range lines {
		trim := strings.TrimSuffix(l, "\n")
		if start < 0 && trim == heading {
			start = pos
		} else if start >= 0 && trim == marker {
			end = pos + len(l)
			break
		} else if start >= 0 && strings.HasPrefix(trim, "## Shared from ") && strings.HasSuffix(trim, " (Nexus)") {
			end = pos
			break
		}
		pos += len(l)
	}
	if start >= 0 {
		return []byte(s[:start] + section + s[end:])
	}
	if strings.TrimSpace(s) == "" {
		return []byte(section)
	}
	return []byte(strings.TrimRight(s, "\n") + "\n\n" + section)
}

// ownRules drops the sections Nexus manages inside a rules file: rules
// shared from other agents and the agentfiles memory section.
func ownRules(b []byte) []byte {
	lines := strings.SplitAfter(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var out strings.Builder
	skipUntil := ""
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if skipUntil != "" {
			if strings.HasPrefix(t, skipUntil) {
				skipUntil = ""
			}
			continue
		}
		switch {
		case strings.HasPrefix(t, "## Shared from ") && strings.HasSuffix(t, " (Nexus)"):
			skipUntil = "<!-- End shared from "
		case strings.HasPrefix(t, "<!-- nexus:memory:start"):
			skipUntil = "<!-- nexus:memory:end"
		default:
			out.WriteString(l)
		}
	}
	return []byte(strings.TrimSpace(out.String()) + "\n")
}
func (r roots) skillWrites(source, target string) ([]pendingWrite, error) {
	src, dst := filepath.Dir(source), filepath.Dir(target)
	writes := []pendingWrite{}
	seen := map[string]bool{}
	e := filepath.WalkDir(src, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return fmt.Errorf("cannot read skill directory")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill contains a symlink")
		}
		if !r.safe(p) {
			return fmt.Errorf("unsafe skill path")
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		b, e := r.read(p)
		if e != nil {
			return fmt.Errorf("cannot read skill file")
		}
		dest := filepath.Join(dst, rel)
		if !r.safe(dest) {
			return fmt.Errorf("unsafe target path")
		}
		seen[filepath.Clean(dest)] = true
		old, e := r.read(dest)
		if e == nil {
			if !bytes.Equal(old, b) {
				return fmt.Errorf("conflict")
			}
			return nil
		}
		if !os.IsNotExist(e) {
			return fmt.Errorf("cannot read target skill")
		}
		writes = append(writes, pendingWrite{dest, b, false})
		return nil
	})
	if e != nil {
		return nil, e
	}
	if _, e := os.Lstat(dst); e == nil {
		if len(writes) != 0 {
			return nil, fmt.Errorf("conflict")
		}
		e = filepath.WalkDir(dst, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return fmt.Errorf("cannot inspect target skill")
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("conflict")
			}
			if !d.IsDir() && !seen[filepath.Clean(p)] {
				return fmt.Errorf("conflict")
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return writes, nil
}

// Share revalidates the source against Scan, preflights each target, and makes
// exclusive backups before updates. Errors intentionally omit config contents.
// As with other filesystem APIs, callers must exclude concurrent hostile path
// replacement while sharing; this function is not a sandbox for untrusted users.
func Share(o ShareOptions) (ShareResult, error) {
	result := ShareResult{Writes: []PlannedWrite{}, Skipped: []string{}}
	r := resolve(ScanOptions{o.Home, o.Workspace, o.AppData})
	if o.Scope != "user" && o.Scope != "project" {
		return result, fmt.Errorf("scope must be user or project")
	}
	if o.Scope == "project" && r.workspace == "" {
		return result, fmt.Errorf("project scope requires Workspace")
	}
	i, ok := sourceItem(o)
	if !ok {
		return result, fmt.Errorf("source item was not found in inventory")
	}
	valid := validName(i.Name)
	if i.Kind == "command" {
		valid = validCommandName(i.Name)
	}
	if i.Kind == "mcp" {
		valid = i.Name != "" && !strings.ContainsAny(i.Name, "\x00\r\n")
	}
	if !valid && i.Kind != "rules" {
		return result, fmt.Errorf("unsafe item name")
	}
	var source []byte
	var n map[string]any
	var e error
	if i.Kind == "mcp" {
		n, e = mcpSource(r, i, o.From)
	} else {
		source, e = r.read(i.Path)
	}
	if e != nil {
		return result, fmt.Errorf("cannot read source item")
	}
	if i.Kind == "rules" {
		// Never re-share sections Nexus wrote (other agents' shared rules,
		// the managed memory section): that would nest them on every share.
		source = ownRules(source)
		if len(bytes.TrimSpace(source)) == 0 {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s rules only contain Nexus-managed sections", o.From))
			return result, nil
		}
	}
	seen := map[string]bool{}
	for _, to := range o.To {
		if seen[to] {
			continue
		}
		seen[to] = true
		if !compatible(o.From, to, i.Kind) {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s has no supported %s conversion from %s", to, i.Kind, o.From))
			continue
		}
		target, e := targetPath(r, to, i.Kind, o.Scope, i.Name)
		if e != nil {
			result.Skipped = append(result.Skipped, e.Error())
			continue
		}
		if filepath.Clean(target) == filepath.Clean(i.Path) {
			continue // e.g. Codex and OpenCode share the project AGENTS.md
		}
		if to == "opencode" && i.Kind == "mcp" {
			if _, e := os.Lstat(target); os.IsNotExist(e) {
				if _, e := os.Lstat(target + "c"); e == nil {
					target += "c"
				}
			}
		}
		if !r.safe(target) {
			return result, fmt.Errorf("target path outside allowed roots")
		}
		conflict := fmt.Sprintf("a different %s named %s already exists in %s", i.Kind, i.Name, to)
		pending := []pendingWrite{}
		if i.Kind == "skill" {
			pending, e = r.skillWrites(i.Path, target)
			if e != nil {
				if e.Error() == "conflict" {
					result.Skipped = append(result.Skipped, conflict)
					continue
				}
				return result, e
			}
		} else {
			old, readErr := r.read(target)
			exists := readErr == nil
			if readErr != nil && !os.IsNotExist(readErr) {
				return result, fmt.Errorf("cannot read target file")
			}
			data := source
			switch i.Kind {
			case "mcp":
				m := map[string]any{}
				if exists {
					m, e = r.config(target)
					if e != nil {
						return result, fmt.Errorf("invalid target configuration")
					}
				}
				key := "mcpServers"
				if to == "codex" {
					key = "mcp_servers"
				}
				if to == "opencode" {
					key = "mcp"
				}
				servers := object(m[key])
				if m[key] != nil && servers == nil {
					return result, fmt.Errorf("invalid target MCP map")
				}
				if servers == nil {
					servers = map[string]any{}
					m[key] = servers
				}
				raw := encodeMCP(n, to)
				if prev, ok := servers[i.Name]; ok {
					if !reflect.DeepEqual(normalized(object(prev), to), normalized(raw, to)) {
						result.Skipped = append(result.Skipped, conflict)
						continue
					}
				}
				servers[i.Name] = raw
				if to == "codex" {
					data, e = replaceTable(old, i.Name, raw)
				} else {
					data, e = json.MarshalIndent(m, "", "  ")
					data = append(data, '\n')
				}
				if e != nil {
					return result, fmt.Errorf("cannot encode target configuration")
				}
			case "subagent":
				if filepath.Ext(i.Path) != ".md" {
					result.Skipped = append(result.Skipped, "OpenCode config agents cannot be shared as markdown")
					continue
				}
				data, e = convertedAgent(source, to)
				if e != nil {
					return result, e
				}
			case "command":
				if to == "gemini" {
					m, body, e := markdown(source)
					if e != nil {
						return result, e
					}
					var b bytes.Buffer
					e = toml.NewEncoder(&b).Encode(map[string]any{"description": description(m, body), "prompt": body})
					if e != nil {
						return result, fmt.Errorf("cannot encode command")
					}
					data = b.Bytes()
				}
			case "rules":
				data = rulesContent(old, source, o.From)
			}
			if exists && bytes.Equal(old, data) {
				continue
			}
			if exists && i.Kind != "mcp" && i.Kind != "rules" {
				result.Skipped = append(result.Skipped, conflict)
				continue
			}
			pending = append(pending, pendingWrite{target, data, exists})
		}
		for _, w := range pending {
			plan := PlannedWrite{Agent: to, Path: w.path, Action: "create"}
			if w.update {
				plan.Action = "update"
				if plan.Backup, e = backupPath(w.path); e != nil {
					return result, e
				}
			}
			if !o.DryRun {
				if e := r.commit(w, plan.Backup); e != nil {
					return result, e
				}
			}
			result.Writes = append(result.Writes, plan)
		}
	}
	return result, nil
}

// backupPath picks an unused "<path>.nexus-backup-<timestamp>" name.
func backupPath(path string) (string, error) {
	backup := path + ".nexus-backup-" + time.Now().Format("20060102-150405")
	for {
		_, e := os.Lstat(backup)
		if os.IsNotExist(e) {
			return backup, nil
		}
		if e != nil {
			return "", fmt.Errorf("cannot inspect backup path")
		}
		backup += "-1"
	}
}

// commit writes one planned file. An update first copies the current
// contents to backup (created exclusively); a create never overwrites.
func (r roots) commit(w pendingWrite, backup string) error {
	if !r.safe(w.path) {
		return fmt.Errorf("unsafe target path")
	}
	if e := os.MkdirAll(filepath.Dir(w.path), 0700); e != nil {
		return fmt.Errorf("cannot create target directory")
	}
	if w.update {
		old, e := r.read(w.path)
		if e != nil {
			return fmt.Errorf("cannot back up target")
		}
		f, e := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return fmt.Errorf("cannot create backup")
		}
		_, we := f.Write(old)
		ce := f.Close()
		if we != nil || ce != nil {
			return fmt.Errorf("cannot write backup")
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if w.update {
		flags = os.O_WRONLY | os.O_TRUNC
	}
	f, e := os.OpenFile(w.path, flags, 0600)
	if e != nil {
		return fmt.Errorf("cannot open target")
	}
	_, we := f.Write(w.data)
	ce := f.Close()
	if we != nil || ce != nil {
		return fmt.Errorf("cannot write target")
	}
	return nil
}
