package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// MCP server editing: add, edit, turn on/off. Each agent's config is
// written in its own format; only the one server entry changes, unknown
// keys on the entry are kept, the file is backed up and written atomically.

// ServerSpec is one MCP server in a format-neutral shape.
type ServerSpec struct {
	Name string `json:"name"`
	// Transport is stdio (run a command), http or sse (connect to a URL).
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
}

// EditableAgents are the agents whose MCP config Nexus can write.
var EditableAgents = []string{"claude", "codex", "cursor", "gemini", "opencode"}

func editable(agent string) bool {
	for _, a := range EditableAgents {
		if a == agent {
			return true
		}
	}
	return false
}

var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)

func checkName(name string) error {
	if name == NexusServerName {
		return fmt.Errorf("\"nexus\" is the Nexus memory server. Use Connect or Disconnect at the top of this page for it")
	}
	if !serverNameRe.MatchString(name) {
		return fmt.Errorf("Use a short name with letters, numbers, dashes or dots (no spaces), like \"github\" or \"my-files\"")
	}
	return nil
}

func agentLabelOf(agent string) string {
	for _, h := range harnesses {
		if h.id == agent {
			return h.name
		}
	}
	return agent
}

// mcpPaths are the config files an agent's MCP servers live in, by scope.
// "local" is a Claude Code server stored for one project in ~/.claude.json.
func mcpPaths(r roots, agent string) map[string]string {
	out := map[string]string{}
	for _, scope := range []string{"user", "project"} {
		if scope == "project" && r.workspace == "" {
			continue
		}
		p, err := targetPath(r, agent, "mcp", scope, "")
		if err != nil {
			continue
		}
		if agent == "opencode" {
			if _, e := os.Lstat(p); os.IsNotExist(e) {
				if _, e := os.Lstat(p + "c"); e == nil {
					p += "c"
				}
			}
		}
		out[scope] = p
	}
	if agent == "claude" && r.workspace != "" {
		out["local"] = filepath.Join(r.home, ".claude.json")
	}
	return out
}

// mcpTarget checks (agent, scope, path) and returns the path to write and
// the scope it really is. Browse reports Claude's per-project servers in
// ~/.claude.json with scope "project"; they are "local" here.
func mcpTarget(r roots, agent, scope, path string) (string, string, error) {
	if !editable(agent) {
		return "", "", fmt.Errorf("Nexus can't change %s's tool servers yet. Open its own settings to change them", agentLabelOf(agent))
	}
	paths := mcpPaths(r, agent)
	if scope == "" {
		scope = "user"
	}
	if agent == "claude" && scope == "project" && path != "" && strings.EqualFold(filepath.Base(path), ".claude.json") {
		scope = "local"
	}
	want, ok := paths[scope]
	if !ok {
		if scope == "project" {
			if r.workspace == "" {
				return "", "", fmt.Errorf("Pick a project first to add a server to just that project")
			}
			return "", "", fmt.Errorf("%s only reads tool servers from your user settings, not from a project folder", agentLabelOf(agent))
		}
		return "", "", fmt.Errorf("unknown scope %q", scope)
	}
	if path != "" && !strings.EqualFold(filepath.Clean(path), filepath.Clean(want)) {
		return "", "", fmt.Errorf("That settings file isn't one %s reads tool servers from", agentLabelOf(agent))
	}
	if !r.safe(want) {
		return "", "", fmt.Errorf("Nexus only changes settings inside your user folder or this project")
	}
	return want, scope, nil
}

// container is the key path of the servers map inside the document.
func container(r roots, agent, scope string, doc map[string]any) ([]string, error) {
	switch agent {
	case "codex":
		return []string{"mcp_servers"}, nil
	case "opencode":
		return []string{"mcp"}, nil
	}
	if scope == "local" {
		for p := range object(doc["projects"]) {
			if abs, e := filepath.Abs(p); e == nil && strings.EqualFold(filepath.Clean(abs), filepath.Clean(r.workspace)) {
				return []string{"projects", p, "mcpServers"}, nil
			}
		}
		return nil, fmt.Errorf("Claude Code has no settings saved for this project yet")
	}
	return []string{"mcpServers"}, nil
}

// serversAt returns the map at the key path; create makes missing maps.
func serversAt(doc map[string]any, path []string, create bool) (map[string]any, error) {
	cur := doc
	for _, k := range path {
		v, ok := cur[k]
		if !ok || v == nil {
			if !create {
				return nil, nil
			}
			m := map[string]any{}
			cur[k] = m
			cur = m
			continue
		}
		m := object(v)
		if m == nil {
			return nil, fmt.Errorf("the %q setting isn't laid out the way Nexus expects, so it was left as it was", k)
		}
		cur = m
	}
	return cur, nil
}

// loadConfig reads a config (missing or blank is an empty document).
func loadConfig(r roots, path string) ([]byte, map[string]any, bool, error) {
	old, exists, err := readMaybe(r, path)
	if err != nil {
		return nil, nil, false, err
	}
	doc := map[string]any{}
	if exists && len(bytes.TrimSpace(old)) > 0 {
		m, e := r.config(path)
		if e != nil {
			return nil, nil, false, fmt.Errorf("%s has a mistake in it, so Nexus left it as it was. Fix the file (or open the agent once to repair it), then try again", filepath.Base(path))
		}
		doc = m
	}
	return old, doc, exists, nil
}

func stringMap(v any) map[string]string {
	m := object(v)
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		switch x := val.(type) {
		case string:
			out[k] = x
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

func normalTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "stdio", "local", "command":
		return "stdio"
	case "sse":
		return "sse"
	default: // http, streamable-http, streamableHttp, remote
		return "http"
	}
}

// specFrom reads a raw entry in an agent's own format.
func specFrom(raw map[string]any, agent, name string) ServerSpec {
	n := normalized(raw, agent)
	s := ServerSpec{Name: name, Command: str(n["command"]), Args: stringsOf(n["args"]), URL: str(n["url"]),
		Env: stringMap(n["env"]), Headers: stringMap(n["headers"]), Enabled: entryEnabled(raw)}
	s.Transport = normalTransport(str(n["type"]))
	if s.URL == "" && s.Command != "" {
		s.Transport = "stdio"
	}
	if s.URL != "" && s.Command == "" && s.Transport == "stdio" {
		s.Transport = "http"
	}
	return s
}

// encodeSpec writes a spec in an agent's own format.
func encodeSpec(s ServerSpec, agent string) map[string]any {
	m := map[string]any{}
	strs := func(v []string) []any {
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	}
	kv := func(v map[string]string) map[string]any {
		out := map[string]any{}
		for k, x := range v {
			out[k] = x
		}
		return out
	}
	if s.Transport == "stdio" {
		if agent == "opencode" {
			m["type"] = "local"
			m["command"] = strs(append([]string{s.Command}, s.Args...))
			if len(s.Env) > 0 {
				m["environment"] = kv(s.Env)
			}
			return m
		}
		if agent == "claude" {
			m["type"] = "stdio"
		}
		m["command"] = s.Command
		if len(s.Args) > 0 {
			m["args"] = strs(s.Args)
		}
		if len(s.Env) > 0 {
			m["env"] = kv(s.Env)
		}
		return m
	}
	headerKey := "headers"
	switch agent {
	case "claude":
		m["type"] = s.Transport
		m["url"] = s.URL
	case "opencode":
		m["type"] = "remote"
		m["url"] = s.URL
	case "gemini":
		if s.Transport == "http" {
			m["httpUrl"] = s.URL
		} else {
			m["url"] = s.URL
		}
	case "codex":
		m["url"] = s.URL
		headerKey = "http_headers"
	default:
		m["url"] = s.URL
		if s.Transport == "sse" {
			m["type"] = "sse"
		}
	}
	if len(s.Headers) > 0 {
		m[headerKey] = kv(s.Headers)
	}
	return m
}

// cleanSpec trims and validates a spec for an agent.
func cleanSpec(s ServerSpec, agent string) (ServerSpec, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Transport = normalTransport(s.Transport)
	s.Command = strings.TrimSpace(s.Command)
	s.URL = strings.TrimSpace(s.URL)
	args := []string{}
	for _, a := range s.Args {
		if a != "" {
			args = append(args, a)
		}
	}
	s.Args = args
	clean := func(m map[string]string, what string) (map[string]string, error) {
		out := map[string]string{}
		for k, v := range m {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if strings.ContainsAny(k, "\r\n") {
				return nil, fmt.Errorf("%s names can't contain line breaks", what)
			}
			out[k] = v
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	var err error
	if s.Env, err = clean(s.Env, "Environment variable"); err != nil {
		return s, err
	}
	if s.Headers, err = clean(s.Headers, "Header"); err != nil {
		return s, err
	}
	if err := checkName(s.Name); err != nil {
		return s, err
	}
	if s.Transport == "stdio" {
		if s.Command == "" {
			return s, fmt.Errorf("Enter the command that starts the server, like npx or uvx")
		}
		s.URL, s.Headers = "", nil
		return s, nil
	}
	u, e := url.Parse(s.URL)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return s, fmt.Errorf("Enter the server's full web address, starting with https://")
	}
	if agent == "codex" && s.Transport == "sse" {
		return s, fmt.Errorf("Codex can't connect to older SSE servers. Use the server's HTTP address instead, if it has one")
	}
	s.Command, s.Args, s.Env = "", nil, nil
	return s, nil
}

// mergeEntry keeps every key of the existing entry Nexus doesn't own
// (timeouts, tool filters, trust, enabled...) and replaces the rest.
func mergeEntry(prev, next map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range prev {
		out[k] = v
	}
	for _, k := range ownedKeys {
		delete(out, k)
	}
	for k, v := range next {
		out[k] = v
	}
	return out
}

// maskSpec hides every env and header value and token-shaped arguments.
func maskSpec(s ServerSpec) ServerSpec {
	n := 0
	out := s
	if len(s.Env) > 0 {
		out.Env = map[string]string{}
		for k, v := range s.Env {
			out.Env[k] = Mask
			if v == "" {
				out.Env[k] = ""
			}
		}
	}
	if len(s.Headers) > 0 {
		out.Headers = map[string]string{}
		for k, v := range s.Headers {
			out.Headers[k] = Mask
			if v == "" {
				out.Headers[k] = ""
			}
		}
	}
	out.Args = maskArgs(s.Args, &n)
	out.URL = maskText(s.URL, &n)
	return out
}

func maskArgs(args []string, n *int) []string {
	if len(args) == 0 {
		return nil
	}
	raw := make([]any, len(args))
	for i, a := range args {
		raw[i] = a
	}
	masked := maskValue(raw, "args", n).([]any)
	out := make([]string, len(masked))
	for i, v := range masked {
		out[i] = str(v)
	}
	return out
}

// unmaskSpec puts back the hidden values the user left unchanged.
func unmaskSpec(s, prev ServerSpec) (ServerSpec, error) {
	fill := func(m, old map[string]string, what string) (map[string]string, error) {
		if len(m) == 0 {
			return m, nil
		}
		out := map[string]string{}
		for k, v := range m {
			if strings.Contains(v, Mask) {
				o, ok := old[k]
				if !ok || (v != Mask) {
					return nil, fmt.Errorf("Enter the value for %s %s", what, k)
				}
				v = o
			}
			out[k] = v
		}
		return out, nil
	}
	var err error
	if s.Env, err = fill(s.Env, prev.Env, "environment variable"); err != nil {
		return s, err
	}
	if s.Headers, err = fill(s.Headers, prev.Headers, "header"); err != nil {
		return s, err
	}
	n := 0
	maskedPrev := maskArgs(prev.Args, &n)
	for i, a := range s.Args {
		if !strings.Contains(a, Mask) {
			continue
		}
		if i < len(prev.Args) && maskedPrev[i] == a {
			s.Args[i] = prev.Args[i]
			continue
		}
		return s, fmt.Errorf("Argument %d still has a hidden value. Type it again in full", i+1)
	}
	if strings.Contains(s.URL, Mask) {
		if maskText(prev.URL, &n) != s.URL {
			return s, fmt.Errorf("The address still has a hidden value. Type the full address again")
		}
		s.URL = prev.URL
	}
	return s, nil
}

// CommandLine is what Nexus will run (or connect to), with secrets hidden.
func CommandLine(s ServerSpec) string {
	m := maskSpec(s)
	if normalTransport(s.Transport) != "stdio" {
		return "Connect to " + m.URL
	}
	parts := []string{quoteArg(m.Command)}
	for _, a := range m.Args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(a string) string {
	if a == "" || strings.ContainsAny(a, " \t\"'") {
		return "\"" + strings.ReplaceAll(a, "\"", "\\\"") + "\""
	}
	return a
}

// ServerEdit is one server opened for editing (secret values hidden).
type ServerEdit struct {
	Agent       string     `json:"agent"`
	Scope       string     `json:"scope"`
	Path        string     `json:"path"`
	Spec        ServerSpec `json:"spec"`
	CommandLine string     `json:"command_line"`
	// Kept lists the entry's other settings, which saving keeps.
	Kept []string `json:"kept"`
	// Parked: turned off by Nexus (kept aside in Nexus's own settings).
	Parked bool `json:"parked"`
	// CanToggle: the server can be turned off and on.
	CanToggle bool `json:"can_toggle"`
	// JSON is the entry in this agent's own format, secrets hidden.
	JSON string `json:"json"`
}

func nativeToggle(agent string) bool { return agent == "codex" || agent == "opencode" }

// rawServer returns the live or parked entry for (agent, scope, path, name).
func (o EditOptions) rawServer(agent, scope, path, name string) (map[string]any, string, string, bool, error) {
	r := o.roots()
	p, sc, err := mcpTarget(r, agent, scope, path)
	if err != nil {
		return nil, "", "", false, err
	}
	_, doc, _, err := loadConfig(r, p)
	if err != nil {
		return nil, "", "", false, err
	}
	if c, e := container(r, agent, sc, doc); e == nil {
		if servers, e := serversAt(doc, c, false); e == nil {
			if raw := object(servers[name]); raw != nil {
				return raw, p, sc, false, nil
			}
		}
	}
	if ps, ok := o.findParked(agent, p, name, sc); ok {
		return ps.Entry, p, sc, true, nil
	}
	return nil, "", "", false, fmt.Errorf("%s is no longer in %s. Refresh to see the current list", name, filepath.Base(p))
}

// GetMCP opens one server for editing.
func GetMCP(o EditOptions, agent, scope, path, name string) (ServerEdit, error) {
	raw, p, sc, parked, err := o.rawServer(agent, scope, path, name)
	if err != nil {
		return ServerEdit{}, err
	}
	spec := specFrom(raw, agent, name)
	if parked {
		spec.Enabled = false
	}
	out := ServerEdit{Agent: agent, Scope: sc, Path: p, Spec: maskSpec(spec), CommandLine: CommandLine(spec), Parked: parked, Kept: []string{},
		CanToggle: nativeToggle(agent) || strings.TrimSpace(o.StateDir) != ""}
	owned := map[string]bool{}
	for _, k := range ownedKeys {
		owned[k] = true
	}
	for _, k := range keys(raw) {
		if !owned[k] && k != "enabled" && k != "disabled" {
			out.Kept = append(out.Kept, k)
		}
	}
	n := 0
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{name: maskValue(raw, "", &n)})
	out.JSON = buf.String()
	return out, nil
}

// SaveMCPRequest adds (OriginalName empty) or edits one server.
type SaveMCPRequest struct {
	Agent        string     `json:"agent"`
	Scope        string     `json:"scope"`
	Path         string     `json:"path"`
	OriginalName string     `json:"original_name"`
	Spec         ServerSpec `json:"spec"`
	DryRun       bool       `json:"dry_run"`
}

// SaveMCPResult reports what was (or, for a dry run, would be) written.
type SaveMCPResult struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	// Action is added, updated or unchanged (prefixed "will " when dry).
	Action      string `json:"action"`
	Backup      string `json:"backup,omitempty"`
	CommandLine string `json:"command_line"`
	// JSON is the new entry in the agent's format, secrets hidden.
	JSON string `json:"json"`
}

// SaveMCP writes one server into one agent's config.
func SaveMCP(o EditOptions, req SaveMCPRequest) (SaveMCPResult, error) {
	r := o.roots()
	res := SaveMCPResult{Agent: req.Agent, Name: strings.TrimSpace(req.Spec.Name)}
	p, sc, err := mcpTarget(r, req.Agent, req.Scope, req.Path)
	if err != nil {
		return res, err
	}
	res.Path = p
	if req.OriginalName != "" && req.OriginalName == NexusServerName {
		return res, checkName(NexusServerName)
	}
	old, doc, exists, err := loadConfig(r, p)
	if err != nil {
		return res, err
	}
	c, err := container(r, req.Agent, sc, doc)
	if err != nil {
		return res, err
	}
	servers, err := serversAt(doc, c, false)
	if err != nil {
		return res, err
	}
	var prev map[string]any
	if req.OriginalName != "" {
		prev = object(servers[req.OriginalName])
		if prev == nil {
			if _, parked := o.findParked(req.Agent, p, req.OriginalName, sc); parked {
				return res, fmt.Errorf("Turn %s on before editing it", req.OriginalName)
			}
			return res, fmt.Errorf("%s is no longer in %s. Refresh to see the current list", req.OriginalName, filepath.Base(p))
		}
	}
	spec := req.Spec
	if prev != nil {
		if spec, err = unmaskSpec(spec, specFrom(prev, req.Agent, req.OriginalName)); err != nil {
			return res, err
		}
	} else if strings.Contains(fmt.Sprint(spec.Env, spec.Headers, spec.Args, spec.URL), Mask) {
		return res, fmt.Errorf("Some values are still hidden (••••••). Type them in full")
	}
	if spec, err = cleanSpec(spec, req.Agent); err != nil {
		return res, err
	}
	res.Name = spec.Name
	if spec.Name != req.OriginalName {
		if servers[spec.Name] != nil {
			return res, fmt.Errorf("%s already has a server named %q. Pick another name, or edit that one", agentLabelOf(req.Agent), spec.Name)
		}
		if _, parked := o.findParked(req.Agent, p, spec.Name, sc); parked {
			return res, fmt.Errorf("%s already has a server named %q (turned off). Pick another name", agentLabelOf(req.Agent), spec.Name)
		}
	}
	entry := mergeEntry(prev, encodeSpec(spec, req.Agent))
	if prev == nil && req.Agent == "opencode" {
		entry["enabled"] = true
	}
	res.CommandLine = CommandLine(spec)
	n := 0
	if b, e := json.MarshalIndent(map[string]any{spec.Name: maskValue(entry, "", &n)}, "", "  "); e == nil {
		res.JSON = string(b)
	}
	ops := []serverOp{}
	if req.OriginalName != "" && req.OriginalName != spec.Name {
		ops = append(ops, serverOp{name: req.OriginalName})
	}
	ops = append(ops, serverOp{name: spec.Name, entry: entry})
	res.Action = "added"
	if prev != nil {
		res.Action = "updated"
	}
	if req.DryRun {
		res.Action = map[string]string{"added": "will add", "updated": "will update"}[res.Action]
		return res, nil
	}
	backup, changed, err := o.applyOps(r, req.Agent, p, c, old, doc, exists, ops)
	if err != nil {
		return res, err
	}
	if !changed {
		res.Action = "unchanged"
	}
	res.Backup = backup
	return res, nil
}

type serverOp struct {
	name  string
	entry map[string]any // nil removes the server
}

// applyOps edits the servers map of one config file and writes it.
func (o EditOptions) applyOps(r roots, agent, path string, c []string, old []byte, doc map[string]any, exists bool, ops []serverOp) (string, bool, error) {
	var data []byte
	if agent == "codex" {
		data = append([]byte{}, old...)
		for _, op := range ops {
			if op.entry == nil {
				data = cutTable(data, op.name)
				continue
			}
			d, err := setTable(data, op.name, op.entry)
			if err != nil {
				return "", false, fmt.Errorf("Nexus couldn't safely change %s in %s, so it was left as it was", op.name, filepath.Base(path))
			}
			data = d
		}
		var check map[string]any
		if _, err := toml.Decode(string(data), &check); err != nil {
			return "", false, fmt.Errorf("Nexus couldn't safely change %s, so it was left as it was", filepath.Base(path))
		}
		got := object(check["mcp_servers"])
		for _, op := range ops {
			if (op.entry == nil) != (got[op.name] == nil) {
				return "", false, fmt.Errorf("Nexus couldn't safely change %s in %s (it may be written in a way Nexus doesn't edit). It was left as it was", op.name, filepath.Base(path))
			}
		}
	} else {
		servers, err := serversAt(doc, c, true)
		if err != nil {
			return "", false, err
		}
		hasText := exists && len(bytes.TrimSpace(old)) > 0
		data = old
		patched := hasText
		for _, op := range ops {
			if op.entry == nil {
				delete(servers, op.name)
			} else {
				servers[op.name] = op.entry
			}
			if patched {
				d, e := patchNested(data, c, op.name, op.entry, doc)
				if e != nil {
					patched = false
					continue
				}
				data = d
			}
		}
		if !patched {
			if hasText && hasJSONComments(old) {
				return "", false, fmt.Errorf("Nexus couldn't safely edit %s without losing its comments, so it was left as it was", filepath.Base(path))
			}
			unit := "  "
			if hasText {
				unit = indentUnit(old)
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", unit)
			if err := enc.Encode(doc); err != nil {
				return "", false, fmt.Errorf("Nexus couldn't write %s", filepath.Base(path))
			}
			data = buf.Bytes()
			if hasText && bytes.Contains(old, []byte("\r\n")) {
				data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
			}
		}
	}
	if exists && bytes.Equal(old, data) {
		return "", false, nil
	}
	backup := ""
	if exists {
		b, err := o.backupCopy(path, old)
		if err != nil {
			return "", false, err
		}
		backup = b
	}
	if err := writeAtomic(path, data); err != nil {
		return "", false, err
	}
	return backup, true, nil
}

// ── Turning servers off and on ─────────────────────────────────────────

type parkedServer struct {
	Agent     string         `json:"agent"`
	Scope     string         `json:"scope"`
	Path      string         `json:"path"`
	Container []string       `json:"container"`
	Name      string         `json:"name"`
	Entry     map[string]any `json:"entry"`
	At        string         `json:"at"`
}

type parkedStore struct {
	Servers []parkedServer `json:"servers"`
}

func (o EditOptions) parkedFile() string {
	return filepath.Join(o.StateDir, "disabled-mcp.json")
}

func (o EditOptions) loadParked() (parkedStore, error) {
	var s parkedStore
	if strings.TrimSpace(o.StateDir) == "" {
		return s, nil
	}
	b, err := os.ReadFile(o.parkedFile())
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("Nexus couldn't read its list of turned-off servers")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&s); err != nil {
		return parkedStore{}, fmt.Errorf("Nexus's list of turned-off servers is damaged")
	}
	return s, nil
}

func (o EditOptions) saveParked(s parkedStore) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(o.parkedFile(), b)
}

func samePark(p parkedServer, agent, path, name, scope string) bool {
	return p.Agent == agent && p.Name == name && strings.EqualFold(filepath.Clean(p.Path), filepath.Clean(path)) && (scope == "" || p.Scope == scope)
}

func (o EditOptions) findParked(agent, path, name, scope string) (parkedServer, bool) {
	s, err := o.loadParked()
	if err != nil {
		return parkedServer{}, false
	}
	for _, p := range s.Servers {
		if samePark(p, agent, path, name, scope) {
			return p, true
		}
	}
	return parkedServer{}, false
}

// ToggleResult reports SetMCPEnabled.
type ToggleResult struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
	Backup  string `json:"backup,omitempty"`
}

// SetMCPEnabled turns one server off or on. Codex and OpenCode have their
// own "enabled" setting. The other agents have none, so Nexus takes the
// entry out of the config and keeps it in StateDir until it is turned on.
func SetMCPEnabled(o EditOptions, agent, scope, path, name string, enabled bool) (ToggleResult, error) {
	r := o.roots()
	res := ToggleResult{Name: name, Enabled: enabled}
	if name == NexusServerName {
		return res, checkName(name)
	}
	p, sc, err := mcpTarget(r, agent, scope, path)
	if err != nil {
		return res, err
	}
	res.Path = p
	old, doc, exists, err := loadConfig(r, p)
	if err != nil {
		return res, err
	}
	c, err := container(r, agent, sc, doc)
	if err != nil {
		return res, err
	}
	servers, err := serversAt(doc, c, false)
	if err != nil {
		return res, err
	}
	live := object(servers[name])
	parked, isParked := o.findParked(agent, p, name, sc)

	// The entry's own setting (Codex, OpenCode, or a "disabled" key).
	if live != nil && (nativeToggle(agent) || !entryEnabled(live)) {
		if entryEnabled(live) == enabled {
			return res, nil
		}
		entry := map[string]any{}
		for k, v := range live {
			entry[k] = v
		}
		delete(entry, "disabled")
		if enabled {
			if agent == "opencode" {
				entry["enabled"] = true
			} else {
				delete(entry, "enabled")
			}
		} else {
			entry["enabled"] = false
		}
		res.Backup, _, err = o.applyOps(r, agent, p, c, old, doc, exists, []serverOp{{name: name, entry: entry}})
		return res, err
	}
	if enabled {
		if live != nil {
			return res, nil // already on
		}
		if !isParked {
			return res, fmt.Errorf("%s is no longer in %s. Refresh to see the current list", name, filepath.Base(p))
		}
		res.Backup, _, err = o.applyOps(r, agent, p, c, old, doc, exists, []serverOp{{name: name, entry: parked.Entry}})
		if err != nil {
			return res, err
		}
		return res, o.forget(agent, p, name, sc)
	}
	if live == nil {
		if isParked {
			return res, nil // already off
		}
		return res, fmt.Errorf("%s is no longer in %s. Refresh to see the current list", name, filepath.Base(p))
	}
	if strings.TrimSpace(o.StateDir) == "" {
		return res, fmt.Errorf("%s has no off switch for tool servers", agentLabelOf(agent))
	}
	store, err := o.loadParked()
	if err != nil {
		return res, err
	}
	keep := store.Servers[:0]
	for _, s := range store.Servers {
		if !samePark(s, agent, p, name, sc) {
			keep = append(keep, s)
		}
	}
	store.Servers = append(keep, parkedServer{Agent: agent, Scope: sc, Path: p, Container: c, Name: name, Entry: live, At: o.now().UTC().Format("2006-01-02T15:04:05Z")})
	if err := o.saveParked(store); err != nil {
		return res, err
	}
	res.Backup, _, err = o.applyOps(r, agent, p, c, old, doc, exists, []serverOp{{name: name}})
	if err != nil {
		_ = o.forget(agent, p, name, sc) // keep the store in step with the file
		return res, err
	}
	return res, nil
}

func (o EditOptions) forget(agent, path, name, scope string) error {
	store, err := o.loadParked()
	if err != nil {
		return err
	}
	keep := []parkedServer{}
	for _, s := range store.Servers {
		if !samePark(s, agent, path, name, scope) {
			keep = append(keep, s)
		}
	}
	store.Servers = keep
	return o.saveParked(store)
}

// ForgetParked deletes a turned-off server for good.
func ForgetParked(o EditOptions, agent, scope, path, name string) error {
	r := o.roots()
	p, sc, err := mcpTarget(r, agent, scope, path)
	if err != nil {
		return err
	}
	if _, ok := o.findParked(agent, p, name, sc); !ok {
		return fmt.Errorf("%s is not turned off", name)
	}
	return o.forget(agent, p, name, sc)
}

// ParkedItems lists servers Nexus turned off that belong to the home
// folder or the current project, as inventory items (no secret values).
func ParkedItems(o EditOptions) map[string][]Item {
	r := o.roots()
	out := map[string][]Item{}
	store, err := o.loadParked()
	if err != nil {
		return out
	}
	for _, p := range store.Servers {
		switch p.Scope {
		case "project":
			if r.workspace == "" || !within(r.workspace, p.Path) {
				continue
			}
		case "local":
			if r.workspace == "" || len(p.Container) < 2 {
				continue
			}
			abs, e := filepath.Abs(p.Container[1])
			if e != nil || !strings.EqualFold(filepath.Clean(abs), filepath.Clean(r.workspace)) {
				continue
			}
		default:
			if !within(r.home, p.Path) {
				continue
			}
		}
		scope := p.Scope
		if scope == "local" {
			scope = "project"
		}
		it := mcpItem(p.Entry, p.Agent, scope, p.Path, p.Name)
		it.Disabled, it.Parked = true, true
		out[p.Agent] = append(out[p.Agent], it)
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].Name < out[k][j].Name })
	}
	return out
}

// ── Templates and pasted JSON ──────────────────────────────────────────

// TemplateInput is one value a template asks for.
type TemplateInput struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// Folder: a folder path (the app offers a folder picker).
	Folder bool `json:"folder,omitempty"`
}

// MCPTemplate is a ready-made server in the add gallery. Values like
// {folder} in args, env, headers or the URL come from Inputs.
type MCPTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Needs       string          `json:"needs,omitempty"`
	Spec        ServerSpec      `json:"spec"`
	Inputs      []TemplateInput `json:"inputs"`
}

// MCPTemplates is the small built-in gallery.
func MCPTemplates() []MCPTemplate {
	folder := TemplateInput{Key: "folder", Label: "Folder", Placeholder: "C:\\code\\my-project", Help: "The server can read and change files only inside this folder.", Required: true, Folder: true}
	return []MCPTemplate{
		{ID: "filesystem", Name: "Files", Description: "Read and edit files in a folder you choose.", Needs: "Node.js",
			Spec:   ServerSpec{Name: "filesystem", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "{folder}"}},
			Inputs: []TemplateInput{folder}},
		{ID: "github", Name: "GitHub", Description: "Issues, pull requests and code search on GitHub.",
			Spec:   ServerSpec{Name: "github", Transport: "http", URL: "https://api.githubcopilot.com/mcp/", Headers: map[string]string{"Authorization": "Bearer {token}"}},
			Inputs: []TemplateInput{{Key: "token", Label: "GitHub personal access token", Placeholder: "github_pat_…", Help: "Create one at github.com → Settings → Developer settings → Personal access tokens.", Secret: true, Required: true}}},
		{ID: "playwright", Name: "Browser (Playwright)", Description: "Open web pages, click and fill forms in a real browser.", Needs: "Node.js",
			Spec: ServerSpec{Name: "playwright", Transport: "stdio", Command: "npx", Args: []string{"-y", "@playwright/mcp@latest"}}, Inputs: []TemplateInput{}},
		{ID: "fetch", Name: "Fetch web pages", Description: "Download a web page and read it as text.", Needs: "uv (Python)",
			Spec: ServerSpec{Name: "fetch", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-fetch"}}, Inputs: []TemplateInput{}},
		{ID: "context7", Name: "Library docs (Context7)", Description: "Up-to-date documentation for popular libraries.",
			Spec: ServerSpec{Name: "context7", Transport: "http", URL: "https://mcp.context7.com/mcp"}, Inputs: []TemplateInput{}},
		{ID: "memory", Name: "Notes graph", Description: "A small knowledge graph the agent can save notes to.", Needs: "Node.js",
			Spec: ServerSpec{Name: "memory", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-memory"}}, Inputs: []TemplateInput{}},
		{ID: "sequential-thinking", Name: "Step-by-step thinking", Description: "Helps the agent plan hard problems in steps.", Needs: "Node.js",
			Spec: ServerSpec{Name: "sequential-thinking", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-sequential-thinking"}}, Inputs: []TemplateInput{}},
		{ID: "git", Name: "Git", Description: "Read history, diffs and branches of a repository.", Needs: "uv (Python)",
			Spec:   ServerSpec{Name: "git", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-git", "--repository", "{folder}"}},
			Inputs: []TemplateInput{{Key: "folder", Label: "Repository folder", Placeholder: "C:\\code\\my-project", Required: true, Folder: true}}},
	}
}

// FromTemplate fills a template's inputs into a spec.
func FromTemplate(id string, values map[string]string) (ServerSpec, error) {
	for _, t := range MCPTemplates() {
		if t.ID != id {
			continue
		}
		fill := func(s string) string {
			for _, in := range t.Inputs {
				s = strings.ReplaceAll(s, "{"+in.Key+"}", strings.TrimSpace(values[in.Key]))
			}
			return s
		}
		for _, in := range t.Inputs {
			if in.Required && strings.TrimSpace(values[in.Key]) == "" {
				return ServerSpec{}, fmt.Errorf("Enter the %s", strings.ToLower(in.Label[:1])+in.Label[1:])
			}
		}
		s := t.Spec
		s.Enabled = true
		s.URL = fill(s.URL)
		args := []string{}
		for _, a := range s.Args {
			args = append(args, fill(a))
		}
		s.Args = args
		mapFill := func(m map[string]string) map[string]string {
			if m == nil {
				return nil
			}
			out := map[string]string{}
			for k, v := range m {
				out[k] = fill(v)
			}
			return out
		}
		s.Env, s.Headers = mapFill(s.Env), mapFill(s.Headers)
		return s, nil
	}
	return ServerSpec{}, fmt.Errorf("unknown template %q", id)
}

func looksLikeServer(m map[string]any) bool {
	for _, k := range []string{"command", "url", "httpUrl", "serverUrl"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// ParseMCP reads servers from pasted text: a whole config
// ({"mcpServers": {...}}, VS Code's "servers", OpenCode's "mcp", Codex
// TOML), a map of name -> server, or a single server entry.
func ParseMCP(text string) ([]ServerSpec, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("Paste the server's JSON first")
	}
	var doc map[string]any
	if strings.HasPrefix(text, "{") {
		m, err := decodeJSONC([]byte(text))
		if err != nil {
			// A bare `"name": {...}` member pasted from a bigger file.
			if m2, e2 := decodeJSONC([]byte("{" + text + "}")); e2 == nil {
				m, err = m2, nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("That isn't valid JSON. Check for a missing comma or bracket")
		}
		doc = m
	} else if strings.HasPrefix(text, "\"") {
		m, err := decodeJSONC([]byte("{" + strings.TrimSuffix(text, ",") + "}"))
		if err != nil {
			return nil, fmt.Errorf("That isn't valid JSON. Check for a missing comma or bracket")
		}
		doc = m
	} else {
		if _, err := toml.Decode(text, &doc); err != nil || doc == nil {
			return nil, fmt.Errorf("Nexus couldn't read that. Paste the JSON from the server's instructions (it usually starts with { and mentions \"mcpServers\")")
		}
	}
	agent := ""
	var servers map[string]any
	switch {
	case object(doc["mcpServers"]) != nil:
		servers = object(doc["mcpServers"])
	case object(doc["servers"]) != nil:
		servers = object(doc["servers"])
	case object(doc["mcp_servers"]) != nil:
		servers = object(doc["mcp_servers"])
	case object(doc["mcp"]) != nil:
		servers, agent = object(doc["mcp"]), "opencode"
	case looksLikeServer(doc):
		servers = map[string]any{guessName(doc): doc}
	default:
		servers = map[string]any{}
		for k, v := range doc {
			if m := object(v); m != nil && looksLikeServer(m) {
				servers[k] = m
			}
		}
	}
	out := []ServerSpec{}
	for _, name := range keys(servers) {
		raw := object(servers[name])
		if raw == nil || !looksLikeServer(raw) {
			continue
		}
		a := agent
		if t := str(raw["type"]); t == "local" || t == "remote" {
			a = "opencode"
		}
		if _, ok := raw["command"].([]any); ok {
			a = "opencode"
		}
		s := specFrom(raw, a, name)
		s.Enabled = true
		if !serverNameRe.MatchString(s.Name) {
			s.Name = guessName(raw)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("Nexus didn't find a server in that text. It needs a \"command\" or a \"url\"")
	}
	return out, nil
}

// guessName picks a short name from a server's package or address.
func guessName(raw map[string]any) string {
	n := normalized(raw, "")
	cand := ""
	if u, err := url.Parse(str(n["url"])); err == nil && u.Host != "" {
		parts := strings.Split(u.Hostname(), ".")
		cand = parts[0]
		if (cand == "mcp" || cand == "api" || cand == "www") && len(parts) > 2 {
			cand = parts[1]
		}
	}
	for _, a := range stringsOf(n["args"]) {
		if strings.HasPrefix(a, "-") {
			continue
		}
		a = strings.TrimPrefix(a, "@")
		if i := strings.LastIndex(a, "@"); i > 0 {
			a = a[:i]
		}
		segs := strings.Split(a, "/")
		a = segs[len(segs)-1]
		if a == "mcp" && len(segs) > 1 {
			a = segs[len(segs)-2]
		}
		a = strings.TrimPrefix(strings.TrimPrefix(a, "mcp-server-"), "server-")
		a = strings.TrimSuffix(strings.TrimSuffix(a, "-mcp-server"), "-mcp")
		if a == "mcp" || a == "" {
			continue
		}
		if a != "" {
			cand = a
			break
		}
	}
	cand = regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(cand, "-")
	cand = strings.Trim(cand, "-.")
	if cand == "" || !serverNameRe.MatchString(cand) || cand == NexusServerName {
		return "server"
	}
	return strings.ToLower(cand)
}

// setTable replaces [mcp_servers.<name>] in a Codex config with a cleanly
// formatted table at the end of the file: no indentation, one blank line
// before it. Everything else keeps its bytes.
func setTable(b []byte, name string, raw map[string]any) ([]byte, error) {
	var block bytes.Buffer
	enc := toml.NewEncoder(&block)
	enc.Indent = ""
	if err := enc.Encode(map[string]any{"mcp_servers": map[string]any{name: raw}}); err != nil {
		return nil, err
	}
	table := strings.TrimLeft(strings.Replace(block.String(), "[mcp_servers]\n", "", 1), "\n")
	out := strings.TrimRight(string(cutTable(b, name)), "\n")
	if out != "" {
		out += "\n\n"
	}
	out += table
	var check map[string]any
	if _, err := toml.Decode(out, &check); err != nil {
		return nil, err
	}
	return []byte(out), nil
}
