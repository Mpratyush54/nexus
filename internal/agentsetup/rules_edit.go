package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Rules (instruction) files: the Markdown files agents read at the start
// of every chat. People edit them in the app; Nexus keeps a backup, writes
// atomically and refuses to overwrite a file that changed on disk since it
// was opened, or a section Nexus itself keeps up to date.

// RulesFile is one instruction file the app can edit or create.
type RulesFile struct {
	// Agents lists the agents that read this file.
	Agents []string `json:"agents"`
	Scope  string   `json:"scope"`
	// Label is a short plain name, e.g. "CLAUDE.md in this project".
	Label  string `json:"label"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	// Managed is true when the file holds a section Nexus maintains.
	Managed bool `json:"managed,omitempty"`
}

// maxRulesEdit bounds files opened in the editor.
const maxRulesEdit = 1 << 20

type rulesCandidate struct {
	scope, rel, label string
	agents            []string
	always            bool // listed (as "create") even when missing
}

var userRules = []rulesCandidate{
	{"user", ".claude/CLAUDE.md", "Claude Code: your rules for every project", []string{"claude"}, true},
	{"user", ".codex/AGENTS.md", "Codex: your rules for every project", []string{"codex"}, true},
	{"user", ".gemini/GEMINI.md", "Gemini CLI: your rules for every project", []string{"gemini", "antigravity"}, true},
	{"user", ".config/opencode/AGENTS.md", "OpenCode: your rules for every project", []string{"opencode"}, false},
	{"user", ".copilot/copilot-instructions.md", "GitHub Copilot CLI: your rules", []string{"copilot"}, false},
	{"user", ".codeium/windsurf/memories/global_rules.md", "Windsurf: your global rules", []string{"windsurf"}, false},
}

var projectRules = []rulesCandidate{
	{"project", "CLAUDE.md", "CLAUDE.md in this project", []string{"claude"}, true},
	{"project", "AGENTS.md", "AGENTS.md in this project", []string{"codex", "opencode", "cursor", "copilot", "kimi"}, true},
	{"project", "GEMINI.md", "GEMINI.md in this project", []string{"gemini"}, true},
	{"project", ".claude/CLAUDE.md", ".claude/CLAUDE.md in this project", []string{"claude"}, false},
	{"project", ".cursorrules", ".cursorrules in this project", []string{"cursor"}, false},
	{"project", ".github/copilot-instructions.md", "Copilot instructions in this project", []string{"copilot"}, false},
	{"project", ".windsurfrules", ".windsurfrules in this project", []string{"windsurf"}, false},
}

// Rule folders whose .md / .mdc files are editable too.
var projectRuleDirs = []struct {
	rel    string
	agents []string
	label  string
}{
	{".cursor/rules", []string{"cursor"}, "Cursor rule"},
	{".windsurf/rules", []string{"windsurf"}, "Windsurf rule"},
	{".github/instructions", []string{"copilot"}, "Copilot instructions"},
	{".agent/rules", []string{"antigravity"}, "Antigravity rule"},
}

// RulesFiles lists the instruction files that exist, plus the main ones
// that could be created (CLAUDE.md, AGENTS.md, GEMINI.md and each agent's
// own user file). Project files need a Workspace.
func RulesFiles(o EditOptions) []RulesFile {
	r := o.roots()
	out := []RulesFile{}
	add := func(base string, c rulesCandidate) {
		p := filepath.Join(base, filepath.FromSlash(c.rel))
		if !r.safe(p) {
			return
		}
		st, err := os.Stat(p)
		exists := err == nil && st.Mode().IsRegular()
		if !exists && !c.always {
			return
		}
		f := RulesFile{Agents: c.agents, Scope: c.scope, Label: c.label, Path: p, Exists: exists}
		if exists {
			if b, e := r.read(p); e == nil {
				f.Managed = len(managedRanges(normalizeText(b))) > 0
			}
		}
		out = append(out, f)
	}
	if r.workspace != "" {
		for _, c := range projectRules {
			add(r.workspace, c)
		}
		for _, d := range projectRuleDirs {
			dir := filepath.Join(r.workspace, filepath.FromSlash(d.rel))
			var found []string
			r.files(dir, false, func(p string) {
				if ext := strings.ToLower(filepath.Ext(p)); ext == ".md" || ext == ".mdc" {
					found = append(found, p)
				}
			})
			sort.Strings(found)
			for _, p := range found {
				rel, _ := filepath.Rel(r.workspace, p)
				add(r.workspace, rulesCandidate{"project", filepath.ToSlash(rel), d.label + ": " + filepath.Base(p), d.agents, false})
			}
		}
	}
	for _, c := range userRules {
		add(r.home, c)
	}
	return out
}

func rulesFile(o EditOptions, path string) (RulesFile, bool) {
	clean := filepath.Clean(path)
	for _, f := range RulesFiles(o) {
		if strings.EqualFold(filepath.Clean(f.Path), clean) {
			return f, true
		}
	}
	return RulesFile{}, false
}

// ManagedRange marks lines (1-based, inclusive) Nexus keeps up to date.
type ManagedRange struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Label string `json:"label"`
}

// RulesDoc is one instruction file opened for editing.
type RulesDoc struct {
	Path    string `json:"path"`
	Label   string `json:"label"`
	Content string `json:"content"`
	// ETag identifies the version on disk; Save refuses a different one.
	ETag    string         `json:"etag"`
	Exists  bool           `json:"exists"`
	Managed []ManagedRange `json:"managed"`
	Agents  []string       `json:"agents"`
}

func normalizeText(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// ErrRulesConflict means the file changed on disk after it was opened.
var ErrRulesConflict = errors.New("This file changed on disk since you opened it. Reload it to see the new version, then make your edits again")

// ReadRules opens a listed instruction file (or a missing one to create).
func ReadRules(o EditOptions, path string) (RulesDoc, error) {
	f, ok := rulesFile(o, path)
	if !ok {
		return RulesDoc{}, fmt.Errorf("Nexus can only edit agent instruction files (like CLAUDE.md or AGENTS.md) in your user folder or this project")
	}
	doc := RulesDoc{Path: f.Path, Label: f.Label, Managed: []ManagedRange{}, Agents: f.Agents}
	b, exists, err := readMaybe(o.roots(), f.Path)
	if err != nil {
		return doc, err
	}
	if len(b) > maxRulesEdit {
		return doc, fmt.Errorf("%s is larger than 1 MB, which is too big to edit here. Open it in your editor instead", filepath.Base(f.Path))
	}
	doc.Exists = exists
	doc.ETag = etag(b, exists)
	doc.Content = normalizeText(b)
	doc.Managed = managedRanges(doc.Content)
	return doc, nil
}

func readMaybe(r roots, path string) ([]byte, bool, error) {
	if !r.safe(path) {
		return nil, false, fmt.Errorf("Nexus only changes files inside your user folder or this project")
	}
	b, err := r.read(path)
	if err == nil {
		return b, true, nil
	}
	if _, se := os.Lstat(path); os.IsNotExist(se) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("Nexus couldn't open %s. Close any program that has it locked, then try again", filepath.Base(path))
}

// managedRanges finds the sections Nexus maintains: the project-memory
// section (agentfiles) and rules shared from another agent.
func managedRanges(text string) []ManagedRange {
	out := []ManagedRange{}
	lines := strings.Split(text, "\n")
	open := -1
	label, end := "", ""
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if open >= 0 {
			if strings.HasPrefix(t, end) {
				out = append(out, ManagedRange{open + 1, i + 1, label})
				open = -1
				continue
			}
			// A shared section without its end marker ends at the next one.
			if end == "<!-- End shared from " && strings.HasPrefix(t, "## Shared from ") && strings.HasSuffix(t, " (Nexus)") {
				out = append(out, ManagedRange{open + 1, i, label})
				open = -1
			} else {
				continue
			}
		}
		switch {
		case strings.HasPrefix(t, "<!-- nexus:memory:start"):
			open, label, end = i, "Project memory, kept up to date by Nexus", "<!-- nexus:memory:end"
		case strings.HasPrefix(t, "## Shared from ") && strings.HasSuffix(t, " (Nexus)"):
			from := strings.TrimSuffix(strings.TrimPrefix(t, "## Shared from "), " (Nexus)")
			open, label, end = i, "Rules shared from "+from+" by Nexus", "<!-- End shared from "
		}
	}
	if open >= 0 {
		n := len(lines)
		if n > 0 && lines[n-1] == "" {
			n--
		}
		out = append(out, ManagedRange{open + 1, n, label})
	}
	return out
}

func managedBlocks(text string) []string {
	lines := strings.Split(text, "\n")
	var out []string
	for _, r := range managedRanges(text) {
		out = append(out, strings.TrimRight(strings.Join(lines[r.Start-1:r.End], "\n"), " \t\n"))
	}
	return out
}

func managedChanged(oldText, newText string) bool {
	a, b := managedBlocks(oldText), managedBlocks(newText)
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

// RulesPreview is what saving would change.
type RulesPreview struct {
	Path string `json:"path"`
	// Diff is a unified diff of the file as it is on disk now.
	Diff    string `json:"diff"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	// Unchanged: the text equals the file on disk.
	Unchanged bool `json:"unchanged"`
	// Conflict: the file changed on disk after it was opened.
	Conflict bool `json:"conflict"`
	// ManagedChanged: the edit touches a section Nexus maintains.
	ManagedChanged bool `json:"managed_changed"`
	Create         bool `json:"create"`
}

// PreviewRules diffs content against the file on disk.
func PreviewRules(o EditOptions, path, content, tag string) (RulesPreview, error) {
	doc, err := ReadRules(o, path)
	if err != nil {
		return RulesPreview{}, err
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	p := RulesPreview{Path: doc.Path, Create: !doc.Exists, Conflict: tag != doc.ETag}
	p.Unchanged = doc.Exists && content == doc.Content
	p.ManagedChanged = managedChanged(doc.Content, content)
	p.Diff, p.Added, p.Removed = unifiedDiff(filepath.Base(doc.Path), doc.Content, content)
	return p, nil
}

func unifiedDiff(name, a, b string) (string, int, int) {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return difflib.SplitLines(s)
	}
	al, bl := split(a), split(b)
	text, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: al, B: bl, FromFile: "a/" + name, ToFile: "b/" + name, Context: 3})
	added, removed := 0, 0
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return text, added, removed
}

// RulesSaved reports a saved instruction file.
type RulesSaved struct {
	Path    string `json:"path"`
	ETag    string `json:"etag"`
	Backup  string `json:"backup,omitempty"`
	Created bool   `json:"created"`
	// Action is "saved", "created" or "unchanged".
	Action string `json:"action"`
}

// SaveRules writes content to a listed instruction file. tag must match
// the version that was opened ("" for a file that did not exist). The
// sections Nexus maintains must be left as they are unless allowManaged.
// The old file is backed up first, the original line endings and BOM are
// kept, and the write is atomic.
func SaveRules(o EditOptions, path, content, tag string, allowManaged bool) (RulesSaved, error) {
	r := o.roots()
	f, ok := rulesFile(o, path)
	if !ok {
		return RulesSaved{}, fmt.Errorf("Nexus can only edit agent instruction files (like CLAUDE.md or AGENTS.md) in your user folder or this project")
	}
	out := RulesSaved{Path: f.Path}
	old, exists, err := readMaybe(r, f.Path)
	if err != nil {
		return out, err
	}
	if etag(old, exists) != tag {
		return out, ErrRulesConflict
	}
	if len(content) > maxRulesEdit {
		return out, fmt.Errorf("That's more than 1 MB of text, which is too big for an instruction file")
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	oldText := normalizeText(old)
	if exists && !allowManaged && managedChanged(oldText, content) {
		ranges := managedRanges(oldText)
		where := ""
		if len(ranges) > 0 {
			where = fmt.Sprintf(" (lines %d–%d)", ranges[0].Start, ranges[0].End)
		}
		return out, fmt.Errorf("You changed the section Nexus keeps up to date%s. Nexus would replace those edits on its next update, so make your changes outside it", where)
	}
	data := []byte(content)
	if exists {
		if bytes.Contains(old, []byte("\r\n")) {
			data = []byte(strings.ReplaceAll(content, "\n", "\r\n"))
		}
		if bytes.HasPrefix(old, []byte{0xef, 0xbb, 0xbf}) {
			data = append([]byte{0xef, 0xbb, 0xbf}, data...)
		}
		if bytes.Equal(data, old) {
			out.ETag, out.Action = tag, "unchanged"
			return out, nil
		}
		if out.Backup, err = o.backupCopy(f.Path, old); err != nil {
			return out, err
		}
	}
	if err := writeAtomic(f.Path, data); err != nil {
		return out, err
	}
	out.ETag, out.Created = etag(data, true), !exists
	out.Action = "saved"
	if !exists {
		out.Action = "created"
	}
	return out, nil
}

// RulesTemplate is a starting point for a new instruction file. Project
// files list the build and test commands Nexus can find in the folder.
func RulesTemplate(o EditOptions, path string) (string, error) {
	f, ok := rulesFile(o, path)
	if !ok {
		return "", fmt.Errorf("Nexus can only create agent instruction files (like CLAUDE.md or AGENTS.md) in your user folder or this project")
	}
	if f.Scope == "user" {
		return "# My preferences\n\n" +
			"<!-- Rules here apply to every project. Keep them short: the agent reads this file at the start of every chat. -->\n\n" +
			"- Explain what you changed and why, briefly.\n" +
			"- Keep changes small and focused; don't reformat code you didn't touch.\n" +
			"- Ask before adding a new dependency.\n", nil
	}
	r := o.roots()
	name := filepath.Base(r.workspace)
	cmds := projectCommands(r)
	var b strings.Builder
	b.WriteString("# " + name + "\n\n")
	b.WriteString("<!-- What this project is, in one or two sentences. Agents read this file at the start of every chat, so keep it short and specific. -->\n\n")
	b.WriteString("## Commands\n\n")
	if len(cmds) == 0 {
		b.WriteString("- Build: `...`\n- Test: `...`\n")
	}
	for _, c := range cmds {
		b.WriteString("- " + c[0] + ": `" + c[1] + "`\n")
	}
	b.WriteString("\n## How we work\n\n")
	b.WriteString("- Follow the patterns already used in the files you change.\n")
	b.WriteString("- Keep changes small and focused; don't reformat unrelated code.\n")
	b.WriteString("- Run the tests before saying a task is done.\n")
	b.WriteString("- Ask before adding a new dependency.\n")
	b.WriteString("\n## Good to know\n\n")
	b.WriteString("- Where the main code lives, and anything an agent should avoid touching.\n")
	return b.String(), nil
}

// projectCommands finds build/test/lint commands from the usual manifests.
func projectCommands(r roots) [][2]string {
	w := r.workspace
	exists := func(rel string) bool { _, e := os.Stat(filepath.Join(w, rel)); return e == nil }
	var out [][2]string
	if b, e := r.read(filepath.Join(w, "package.json")); e == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal(b, &pkg)
		runner := "npm run"
		switch {
		case exists("pnpm-lock.yaml"):
			runner = "pnpm"
		case exists("yarn.lock"):
			runner = "yarn"
		case exists("bun.lockb"), exists("bun.lock"):
			runner = "bun run"
		}
		for _, s := range [][2]string{{"Install", ""}, {"Dev server", "dev"}, {"Build", "build"}, {"Test", "test"}, {"Lint", "lint"}} {
			if s[1] == "" {
				install := strings.Fields(runner)[0] + " install"
				out = append(out, [2]string{s[0], install})
				continue
			}
			if _, ok := pkg.Scripts[s[1]]; ok {
				out = append(out, [2]string{s[0], runner + " " + s[1]})
			}
		}
	}
	if exists("go.mod") {
		out = append(out, [2]string{"Build (Go)", "go build ./..."}, [2]string{"Test (Go)", "go test ./..."})
	}
	if exists("Cargo.toml") {
		out = append(out, [2]string{"Build (Rust)", "cargo build"}, [2]string{"Test (Rust)", "cargo test"})
	}
	if exists("pyproject.toml") || exists("requirements.txt") {
		out = append(out, [2]string{"Test (Python)", "pytest"})
	}
	return out
}
