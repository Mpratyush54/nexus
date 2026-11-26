// Package agentrun owns live agent chats for the desktop app: one runner per
// chat that keeps the agent's turn going whatever the UI does, buffers what
// the turn streams so a reopened chat replays it, caps how many turns run at
// once, and answers permission requests from the chat's mode and the
// project's allow rules before asking the person.
package agentrun

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Permission modes, shown in the composer as "Ask every time",
// "Auto-approve edits" and "Full access".
const (
	ModeAsk   = "ask"
	ModeEdits = "edits"
	ModeFull  = "full"
)

// NormalizeMode maps UI and legacy values ("", "acceptEdits",
// "bypassPermissions") to a mode. Unknown values fall back to ask.
func NormalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "edits", "acceptedits", "accept_edits", "auto-edit", "autoedit":
		return ModeEdits
	case "full", "bypasspermissions", "bypass", "yolo", "never":
		return ModeFull
	}
	return ModeAsk
}

// Action kinds a permission request can be about.
const (
	KindEdit    = "edit"
	KindCommand = "command"
	KindRead    = "read"
	KindFetch   = "fetch"
	KindMCP     = "mcp"
	KindOther   = "other"
)

// Action is a permission request in plain terms.
type Action struct {
	Kind    string   `json:"kind"`
	Tool    string   `json:"tool"`
	Command string   `json:"command,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	// Summary is what the card says: "Run `npm test`", "Edit 3 files".
	Summary string `json:"summary"`
	// Rule is what "Always allow" would remember; RuleLabel names it.
	Rule      Rule   `json:"rule"`
	RuleLabel string `json:"rule_label"`
	// Detail is the readable input: the diff for edits, the command, or
	// the tool's arguments.
	Detail string `json:"detail,omitempty"`
}

// Rule allows a tool, or a command prefix when Tool is "command".
type Rule struct {
	Tool   string `json:"tool"`
	Prefix string `json:"prefix,omitempty"`
}

func (r Rule) Label() string {
	if r.Tool == KindCommand {
		return "`" + r.Prefix + "` commands"
	}
	if r.Tool == KindEdit {
		return "file edits"
	}
	return r.Tool
}

// Classify turns an agent's permission request into an Action.
// toolKind is the agent's own category when it has one (ACP's "edit",
// "execute", ...); input is the tool's raw JSON input.
func Classify(toolName, toolKind string, input json.RawMessage, workspace string) Action {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	name := strings.TrimSpace(toolName)
	kind := strings.ToLower(strings.TrimSpace(toolKind))
	a := Action{Tool: name, Kind: KindOther}
	switch {
	case name == "Bash" || name == "PowerShell" || kind == "execute" || name == "shell" || name == "run_shell_command":
		a.Kind = KindCommand
		a.Command = commandText(in)
	case isEditTool(name) || kind == "edit" || kind == "delete" || kind == "move":
		a.Kind = KindEdit
		a.Paths = editPaths(in)
	case name == "Read" || name == "Glob" || name == "Grep" || name == "LS" || kind == "read" || kind == "search":
		a.Kind = KindRead
		if p := str("file_path", "path", "pattern"); p != "" {
			a.Paths = []string{p}
		}
	case name == "WebFetch" || name == "WebSearch" || kind == "fetch":
		a.Kind = KindFetch
	case strings.HasPrefix(name, "mcp__"):
		a.Kind = KindMCP
	}
	for i, p := range a.Paths {
		a.Paths[i] = relPath(workspace, p)
	}
	switch a.Kind {
	case KindCommand:
		a.Rule = Rule{Tool: KindCommand, Prefix: CommandPrefix(a.Command)}
		short := oneLine(a.Command, 120)
		if short == "" {
			short = "a command"
			a.Summary = "Run a command"
		} else {
			a.Summary = "Run `" + short + "`"
		}
		a.Detail = a.Command
	case KindEdit:
		a.Rule = Rule{Tool: KindEdit}
		switch len(a.Paths) {
		case 0:
			a.Summary = "Edit files"
		case 1:
			a.Summary = "Edit " + a.Paths[0]
		default:
			a.Summary = fmt.Sprintf("Edit %d files", len(a.Paths))
		}
		a.Detail = editDetail(in)
	case KindRead:
		a.Rule = Rule{Tool: name}
		a.Summary = "Read " + strings.Join(a.Paths, ", ")
		if len(a.Paths) == 0 {
			a.Summary = "Read files"
		}
	case KindFetch:
		a.Rule = Rule{Tool: name}
		if u := str("url", "query"); u != "" {
			a.Summary = "Fetch " + oneLine(u, 100)
		} else {
			a.Summary = "Use the web"
		}
	case KindMCP:
		a.Rule = Rule{Tool: name}
		parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
		a.Summary = "Use " + strings.ReplaceAll(parts[len(parts)-1], "_", " ")
		if len(parts) == 2 {
			a.Summary += " (" + parts[0] + ")"
		}
	default:
		a.Rule = Rule{Tool: name}
		a.Summary = "Use " + nonEmpty(name, "a tool")
	}
	if a.Detail == "" && len(input) > 0 && string(input) != "{}" && string(input) != "null" {
		a.Detail = prettyJSON(input)
	}
	a.RuleLabel = a.Rule.Label()
	return a
}

func isEditTool(name string) bool {
	switch name {
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "apply_patch", "write_file", "replace", "edit_file":
		return true
	}
	return false
}

func commandText(in map[string]any) string {
	switch v := in["command"].(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		// Codex sends argv; ["bash","-lc","npm test"] means the script.
		parts := make([]string, 0, len(v))
		for _, p := range v {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) == 3 && (parts[1] == "-lc" || parts[1] == "-c" || strings.EqualFold(parts[1], "-Command")) {
			return strings.TrimSpace(parts[2])
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	for _, k := range []string{"cmd", "script"} {
		if s, ok := in[k].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func editPaths(in map[string]any) []string {
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, e := range out {
			if e == p {
				return
			}
		}
		out = append(out, p)
	}
	for _, k := range []string{"file_path", "notebook_path", "path", "absolute_path"} {
		if s, ok := in[k].(string); ok {
			add(s)
		}
	}
	switch c := in["changes"].(type) {
	case map[string]any: // Codex: path -> change
		for p := range c {
			add(p)
		}
	case []any:
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if s, ok := m["path"].(string); ok {
					add(s)
				}
			}
		}
	}
	return out
}

func editDetail(in map[string]any) string {
	var b strings.Builder
	if s, ok := in["diff"].(string); ok {
		b.WriteString(s)
	}
	if old, ok := in["old_string"].(string); ok {
		newS, _ := in["new_string"].(string)
		for _, l := range strings.Split(old, "\n") {
			b.WriteString("- " + l + "\n")
		}
		for _, l := range strings.Split(newS, "\n") {
			b.WriteString("+ " + l + "\n")
		}
	} else if content, ok := in["content"].(string); ok {
		for _, l := range strings.Split(content, "\n") {
			b.WriteString("+ " + l + "\n")
		}
	}
	appendChange := func(path string, m map[string]any) {
		if d, ok := m["unified_diff"].(string); ok {
			b.WriteString("--- " + path + "\n" + d + "\n")
		} else if d, ok := m["diff"].(string); ok {
			b.WriteString("--- " + path + "\n" + d + "\n")
		} else if c, ok := m["content"].(string); ok {
			b.WriteString("+++ " + path + "\n" + c + "\n")
		}
	}
	switch c := in["changes"].(type) {
	case map[string]any:
		for p, v := range c {
			if m, ok := v.(map[string]any); ok {
				appendChange(p, m)
			}
		}
	case []any:
		for _, v := range c {
			if m, ok := v.(map[string]any); ok {
				p, _ := m["path"].(string)
				appendChange(p, m)
			}
		}
	}
	out := b.String()
	if len(out) > 24000 {
		out = out[:24000] + "\n…"
	}
	return out
}

// CommandPrefix is the part of a command "Always allow" remembers: the
// program and, for tools with subcommands, the subcommand ("npm test",
// "git status", "go test"). A single word stays a single word.
func CommandPrefix(command string) string {
	first := firstSegment(command)
	words := strings.Fields(first)
	if len(words) == 0 {
		return ""
	}
	prog := words[0]
	if len(words) == 1 {
		return prog
	}
	second := words[1]
	if strings.HasPrefix(second, "-") || strings.ContainsAny(second, `/\.:="'$*`) {
		return prog
	}
	return prog + " " + second
}

func firstSegment(command string) string {
	segs := splitCommand(command)
	if len(segs) == 0 {
		return ""
	}
	return segs[0]
}

// splitCommand splits on && || ; | and newlines. It returns nil for
// commands that substitute other commands (backticks, $( ), which a prefix
// can't vouch for.
func splitCommand(command string) []string {
	if strings.Contains(command, "`") || strings.Contains(command, "$(") || strings.Contains(command, "<(") || strings.Contains(command, ">(") {
		return nil
	}
	var segs []string
	var cur strings.Builder
	quote := rune(0)
	runes := []rune(command)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			cur.WriteRune(r)
		case ';', '\n', '|', '&':
			flush()
			if i+1 < len(runes) && (runes[i+1] == r) && (r == '|' || r == '&') {
				i++
			}
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil
	}
	flush()
	return segs
}

// commandMatches reports whether every part of a chained command starts
// with an allowed prefix (on a word boundary). Output redirection to a file
// is a write and never matches.
func commandMatches(command string, rules []Rule) bool {
	segs := splitCommand(command)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if strings.ContainsAny(seg, "<>") {
			return false
		}
		ok := false
		for _, r := range rules {
			if r.Tool != KindCommand || strings.TrimSpace(r.Prefix) == "" {
				continue
			}
			p := strings.Join(strings.Fields(r.Prefix), " ")
			s := strings.Join(strings.Fields(seg), " ")
			if s == p || strings.HasPrefix(s, p+" ") {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Allowed reports whether rules cover the action.
func Allowed(a Action, rules []Rule) bool {
	if a.Kind == KindCommand {
		return a.Command != "" && commandMatches(a.Command, rules)
	}
	for _, r := range rules {
		if r.Tool == KindCommand {
			continue
		}
		if r.Tool == a.Rule.Tool && r.Tool != "" {
			return true
		}
	}
	return false
}

// AutoApprove decides whether a request runs without asking: full access
// allows everything; auto-approve edits allows file edits inside the
// project folder; otherwise the chat's and project's allow rules decide.
func AutoApprove(mode string, a Action, workspace string, rules []Rule) bool {
	switch NormalizeMode(mode) {
	case ModeFull:
		return true
	case ModeEdits:
		if a.Kind == KindEdit && len(a.Paths) > 0 && allInside(workspace, a.Paths) {
			return true
		}
	}
	if a.Kind == KindEdit && len(a.Paths) > 0 && !allInside(workspace, a.Paths) {
		return false // a project rule never covers edits outside the project
	}
	return Allowed(a, rules)
}

func allInside(workspace string, paths []string) bool {
	if strings.TrimSpace(workspace) == "" {
		return false
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "~") {
			return false
		}
		if filepath.IsAbs(p) {
			rel, err := filepath.Rel(workspace, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return false
			}
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(p))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func relPath(workspace, p string) string {
	if workspace == "" || !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	rel, err := filepath.Rel(workspace, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return filepath.ToSlash(rel)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > max {
		return string([]rune(s)[:max-1]) + "…"
	}
	return s
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func prettyJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if len(b) > 8000 {
		return string(b[:8000]) + "\n…"
	}
	return string(b)
}
