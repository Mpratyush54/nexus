package daemon

// code_hunks.go turns captured edit tool calls into line-level hunks for
// the code-links report (server: code_changes, the why / line_history
// tools). For each edit it keeps the old and new text the agent sent; at
// flush time the reporter reads the file once, finds the new text in it and
// reports the changed line range in the file as it is now, with at most 3
// lines of before/after snippet and the file's content hash. Whole files
// are never uploaded.
//
// Shapes: Claude Edit/MultiEdit/Write, the Anthropic text editor tool
// (str_replace/create/insert), Codex apply_patch (raw, patch field, or a
// shell call running it; also unified-diff hunks), Cursor search_replace /
// edit_file (code_edit with "... existing code ..." markers), OpenCode
// edit/write (oldString/newString, content), Windsurf replace_file_content
// (ReplacementChunks) and write_to_file, Gemini replace/write_file.
//
// Each hunk is anchored on content: added_hashes are lineHash of each line
// the edit wrote (read from the file, so partial-line edits hash the whole
// line), context_before/context_after the hashes of up to 2 unchanged lines
// above and below. The server finds the change behind a line by its text
// and neighbours, so the line numbers sent are only a hint. lineHash must
// match store.LineHash (TestLineHashVector pins both).

import (
	"bufio"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// maxEditPieceBytes: larger old/new texts are not kept for hunks.
	maxEditPieceBytes = 256 << 10
	// maxPendingEditBytes bounds the edit text queued across sessions.
	maxPendingEditBytes = 8 << 20
	// maxHunksPerPiece bounds replace_all occurrences.
	maxHunksPerPiece = 20
	// snippetLines / snippetLineBytes bound before/after snippets.
	snippetLines     = 3
	snippetLineBytes = 240
)

// editPiece is one old -> new replacement of an edit.
type editPiece struct {
	old, new string
	// noOld: the old text is unknown (Write, create, edit_file segments);
	// every located line counts as added.
	noOld bool
	// wholeLines: old/new are whole lines (patch hunks) and must match at
	// line boundaries.
	wholeLines bool
	// all: every occurrence changed (replace_all).
	all bool
	// hint: the 1-based new start line from a unified diff header (0 when
	// unknown), used when the text is not found.
	hint int
}

// pendingEdit is one tool call's edit of one file, queued for a report.
type pendingEdit struct {
	rel        string
	toolCallID string
	at         time.Time
	pieces     []editPiece
	// base is the checkout holding rel ("" = the reporter's root).
	base string
}

func (e pendingEdit) size() int {
	n := len(e.rel) + len(e.toolCallID) + len(e.base)
	for _, p := range e.pieces {
		n += len(p.old) + len(p.new)
	}
	return n
}

// codeChange is one located hunk, as posted in code-links "changes".
type codeChange struct {
	Path          string `json:"path"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
	HunkIndex     int    `json:"hunk_index"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	RemovedLines  int    `json:"removed_lines"`
	AddedLines    int    `json:"added_lines"`
	BeforeSnippet string `json:"before_snippet,omitempty"`
	AfterSnippet  string `json:"after_snippet,omitempty"`
	AfterHash     string `json:"after_hash,omitempty"`
	// Content anchors (see the file comment).
	AddedHashes   []string `json:"added_hashes,omitempty"`
	ContextBefore []string `json:"context_before,omitempty"`
	ContextAfter  []string `json:"context_after,omitempty"`
	At            string   `json:"at,omitempty"`
}

// ---- tool call shapes ----

// callToolName returns the lower-case tool name of a call without an MCP
// prefix ("functions.apply_patch" -> "apply_patch"), and its arguments.
func callToolName(call map[string]any) (string, map[string]any) {
	name := strings.ToLower(strings.TrimSpace(firstString(call, "name", "Name", "tool", "ToolName", "tool_name")))
	if fn, ok := call["function"].(map[string]any); ok && name == "" {
		name = strings.ToLower(strings.TrimSpace(firstString(fn, "name")))
	}
	if name == "" {
		return "", nil
	}
	if i := strings.LastIndexAny(name, ".:/"); i >= 0 && !shellToolNames[name] {
		name = name[i+1:]
	}
	return name, toolArgs(call)
}

var writeToolNames = map[string]bool{
	"write": true, "write_file": true, "write_to_file": true, "create_file": true, "writefile": true,
}

// rawEdit is an edit of one raw (unnormalised) path.
type rawEdit struct {
	path   string
	pieces []editPiece
}

func pieceOK(p editPiece) bool {
	return p.new != "" && len(p.new) <= maxEditPieceBytes && len(p.old) <= maxEditPieceBytes && (p.noOld || p.old != p.new)
}

func stringOf(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s, true
		}
	}
	return "", false
}

// replacePairs reads old/new pairs from args itself and from its list
// fields (MultiEdit edits, Windsurf ReplacementChunks).
func replacePairs(args map[string]any, all bool) []editPiece {
	oldKeys := []string{"old_string", "oldString", "old_str", "TargetContent", "target_content", "old_text"}
	newKeys := []string{"new_string", "newString", "new_str", "ReplacementContent", "replacement_content", "new_text"}
	pair := func(m map[string]any) (editPiece, bool) {
		o, okO := stringOf(m, oldKeys...)
		n, okN := stringOf(m, newKeys...)
		if !okO || !okN {
			return editPiece{}, false
		}
		ra := all || firstBool(m, "replace_all", "replaceAll", "AllowMultiple")
		return editPiece{old: o, new: n, all: ra}, true
	}
	var out []editPiece
	if p, ok := pair(args); ok {
		out = append(out, p)
	}
	for _, k := range []string{"edits", "ReplacementChunks", "replacement_chunks", "chunks"} {
		list, _ := args[k].([]any)
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				if p, ok := pair(m); ok {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

var existingCodeMarker = regexp.MustCompile(`(?i)\.\.\.\s*(existing|rest of|unchanged)`)

// codeEditSegments splits a Cursor code_edit at its "... existing code ..."
// marker lines into the literal segments it writes.
func codeEditSegments(edit string) []editPiece {
	var out []editPiece
	var cur []string
	flush := func() {
		seg := strings.Join(cur, "\n")
		if strings.TrimSpace(seg) != "" {
			out = append(out, editPiece{new: seg, noOld: true, wholeLines: true})
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(edit, "\r\n", "\n"), "\n") {
		if existingCodeMarker.MatchString(line) && len(strings.TrimSpace(line)) < 80 {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

var unifiedHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// patchEdits parses an apply_patch body (Codex "*** Update File:" sections
// or plain unified-diff hunks) into per-file whole-line pieces: each hunk's
// context+removed lines -> context+added lines.
func patchEdits(patch string) []rawEdit {
	var out []rawEdit
	var cur *rawEdit
	var oldL, newL []string
	hint, inHunk, adding := 0, false, false
	endHunk := func() {
		if cur != nil && inHunk && len(newL) > 0 {
			p := editPiece{old: strings.Join(oldL, "\n"), new: strings.Join(newL, "\n"), wholeLines: true, hint: hint}
			if adding {
				p.old, p.noOld = "", true
			}
			if pieceOK(p) {
				cur.pieces = append(cur.pieces, p)
			}
		}
		oldL, newL, hint, inHunk = nil, nil, 0, false
	}
	startFile := func(path string, add bool) {
		endHunk()
		out = append(out, rawEdit{path: strings.TrimSpace(path)})
		cur = &out[len(out)-1]
		adding = add
		// An added file is one hunk of + lines; an update's first hunk may
		// come without "@@".
		inHunk = true
	}
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	for i, line := range lines {
		// "--- a/x" + "+++ b/x" is a unified diff file header; a lone
		// "--- x" inside a hunk is a removed "-- x" line.
		header := strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ")
		if header {
			endHunk()
			cur = nil
			continue
		}
		if strings.HasPrefix(line, "+++ ") && i > 0 && strings.HasPrefix(lines[i-1], "--- ") {
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if i := strings.IndexByte(p, '\t'); i >= 0 {
				p = p[:i]
			}
			if p == "/dev/null" {
				continue
			}
			out = append(out, rawEdit{path: strings.TrimPrefix(strings.TrimPrefix(p, "b/"), "./")})
			cur = &out[len(out)-1]
			adding = false
			continue
		}
		switch {
		case strings.HasPrefix(line, `\`):
			// "\ No newline at end of file"
		case strings.HasPrefix(line, "*** Update File:"):
			startFile(strings.TrimPrefix(line, "*** Update File:"), false)
		case strings.HasPrefix(line, "*** Add File:"):
			startFile(strings.TrimPrefix(line, "*** Add File:"), true)
		case strings.HasPrefix(line, "*** Move to:"):
			if cur != nil {
				cur.path = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to:"))
			}
		case strings.HasPrefix(line, "*** Delete File:"):
			endHunk()
			cur = nil
		case strings.HasPrefix(line, "*** "):
			// "*** Begin Patch", "*** End Patch", "*** End of File".
		case strings.HasPrefix(line, "diff --git "):
			endHunk()
			cur = nil
		case strings.HasPrefix(line, "@@"):
			if cur == nil {
				continue
			}
			endHunk()
			inHunk = true
			if m := unifiedHeader.FindStringSubmatch(line); m != nil {
				hint, _ = strconv.Atoi(m[1])
			}
		case inHunk && strings.HasPrefix(line, "+"):
			newL = append(newL, line[1:])
		case inHunk && strings.HasPrefix(line, "-"):
			oldL = append(oldL, line[1:])
		case inHunk && strings.HasPrefix(line, " "):
			oldL = append(oldL, line[1:])
			newL = append(newL, line[1:])
		case inHunk && line == "":
			// A blank context line some writers send without its space.
			oldL = append(oldL, "")
			newL = append(newL, "")
		}
	}
	endHunk()
	kept := out[:0]
	for _, e := range out {
		if e.path != "" && len(e.pieces) > 0 {
			kept = append(kept, e)
		}
	}
	return kept
}

// editsFromCall returns the edits (with their text) of one tool call.
func editsFromCall(call map[string]any) []rawEdit {
	name, args := callToolName(call)
	if name == "" {
		return nil
	}
	if shellToolNames[name] {
		return patchEdits(shellPatchText(args))
	}
	if !editToolNames[name] {
		return nil
	}
	if patch := rawPatchText(call, args); patch != "" {
		return patchEdits(patch)
	}
	path := strings.TrimSpace(firstString(args, editPathKeys...))
	if path == "" || strings.HasSuffix(strings.ToLower(path), ".ipynb") {
		return nil
	}
	var pieces []editPiece
	command := strings.ToLower(firstString(args, "command"))
	switch {
	case name == "str_replace_editor" || name == "str_replace_based_edit_tool" || command != "":
		switch command {
		case "create":
			if s, ok := stringOf(args, "file_text", "content"); ok {
				pieces = append(pieces, editPiece{new: s, noOld: true})
			}
		case "insert":
			if s, ok := stringOf(args, "new_str", "text"); ok {
				pieces = append(pieces, editPiece{new: s, noOld: true, hint: firstInt(args, "insert_line") + 1})
			}
		default:
			pieces = replacePairs(args, false)
		}
	case writeToolNames[name]:
		if s, ok := stringOf(args, "content", "contents", "CodeContent", "file_text", "text"); ok {
			pieces = append(pieces, editPiece{new: s, noOld: true})
		}
	default:
		pieces = replacePairs(args, false)
		if len(pieces) == 0 {
			if s, ok := stringOf(args, "code_edit", "codeEdit"); ok {
				pieces = codeEditSegments(s)
			} else if s, ok := stringOf(args, "content", "contents", "CodeContent"); ok {
				pieces = append(pieces, editPiece{new: s, noOld: true})
			}
		}
	}
	kept := pieces[:0]
	for _, p := range pieces {
		if pieceOK(p) {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return []rawEdit{{path: path, pieces: kept}}
}

// EditsFromEvents returns the repo-relative edits of a captured batch
// (same path rules as TouchedPathsFromEvents).
func EditsFromEvents(batch []Event, root string) []pendingEdit {
	var out []pendingEdit
	for _, ev := range batch {
		if ev.Payload == nil {
			continue
		}
		calls := toolCallSlice(ev.Payload["tool_calls"])
		if calls == nil {
			calls = toolCallSlice(ev.Payload["toolCalls"])
		}
		if len(calls) == 0 {
			continue
		}
		cwd := eventCwd(ev)
		// The edit's own time (the transcript line), not when it was read.
		at := ev.CreatedAt
		if raw, ok := ev.Payload["timestamp"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				at = parsed
			}
		}
		for _, call := range calls {
			id := strings.TrimSpace(firstString(call, "id", "tool_use_id", "call_id", "toolCallId"))
			for _, e := range editsFromCall(call) {
				loc, ok := resolveEditPath(root, cwd, e.path)
				if !ok {
					continue
				}
				pe := pendingEdit{rel: loc.rel, toolCallID: id, at: at, pieces: e.pieces}
				if !strings.EqualFold(filepath.Clean(loc.base), filepath.Clean(root)) {
					pe.base = loc.base
				}
				out = append(out, pe)
			}
		}
	}
	return out
}

const (
	// maxChangesPerReport stays under the server's store.MaxCodeChanges.
	maxChangesPerReport = 500
	// maxChangesReportBytes keeps a report well under the server's 2 MiB
	// request limit (the links ride along with the first chunk).
	maxChangesReportBytes = 1 << 20
)

// chunkCodeChanges splits hunks into reports the server accepts.
func chunkCodeChanges(changes []codeChange) [][]codeChange {
	var out [][]codeChange
	var cur []codeChange
	size := 0
	for _, c := range changes {
		n := 64 + len(c.Path) + len(c.ToolCallID) + len(c.BeforeSnippet) + len(c.AfterSnippet) + len(c.AfterHash) + len(c.At) +
			20*(len(c.AddedHashes)+len(c.ContextBefore)+len(c.ContextAfter)) + 200
		if len(cur) > 0 && (len(cur) >= maxChangesPerReport || size+n > maxChangesReportBytes) {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, c)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// ---- locating hunks ----

var errNotText = errors.New("not a text file")

// readTextFile reads a regular text file of at most maxHashBytes (no NUL
// in the first binarySniffBytes) and returns it with its sha256.
func readTextFile(abs string) (string, string, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if !st.Mode().IsRegular() || st.Size() > maxHashBytes {
		return "", "", errNotText
	}
	fh, err := os.Open(abs)
	if err != nil {
		return "", "", err
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(bufio.NewReader(fh), maxHashBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > maxHashBytes {
		return "", "", errNotText
	}
	head := data
	if len(head) > binarySniffBytes {
		head = head[:binarySniffBytes]
	}
	for _, b := range head {
		if b == 0 {
			return "", "", errNotText
		}
	}
	sum := sha256.Sum256(data)
	return string(data), hex.EncodeToString(sum[:]), nil
}

// textLines splits text into lines; a trailing newline does not start a
// new line and "" has none.
func textLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func clipSnippet(lines []string) string {
	if len(lines) > snippetLines {
		lines = lines[:snippetLines]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		l = strings.TrimRight(l, " \t")
		if len(l) > snippetLineBytes {
			cut := snippetLineBytes
			for cut > 0 && (l[cut]&0xC0) == 0x80 {
				cut--
			}
			l = l[:cut] + "…"
		}
		out[i] = l
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

type located struct {
	start, end, removed, added int
	before, after              string
}

// findAll returns the byte offsets of needle in content (at most max),
// at line boundaries when wholeLines.
func findAll(content, needle string, wholeLines bool, max int) []int {
	var out []int
	for from := 0; from <= len(content)-len(needle) && len(out) < max; {
		i := strings.Index(content[from:], needle)
		if i < 0 {
			break
		}
		i += from
		end := i + len(needle)
		if !wholeLines || ((i == 0 || content[i-1] == '\n') && (end == len(content) || content[end] == '\n' || strings.HasSuffix(needle, "\n"))) {
			out = append(out, i)
		}
		from = i + 1
	}
	return out
}

// locatePiece finds one replacement in content (CRs removed) and returns
// the changed lines of each located occurrence: common leading and
// trailing lines of old and new are not counted as changed.
func locatePiece(content string, p editPiece) []located {
	newText := strings.ReplaceAll(p.new, "\r\n", "\n")
	oldText := strings.ReplaceAll(p.old, "\r\n", "\n")
	if strings.TrimSpace(newText) == "" {
		return nil
	}
	max := 1
	if p.all {
		max = maxHunksPerPiece
	}
	starts := []int{}
	for _, off := range findAll(content, newText, p.wholeLines, max) {
		starts = append(starts, 1+strings.Count(content[:off], "\n"))
	}
	if len(starts) == 0 && p.hint > 0 {
		if total := len(textLines(content)); p.hint <= total {
			starts = append(starts, p.hint)
		}
	}
	newL := textLines(newText)
	var oldL []string
	if !p.noOld {
		oldL = textLines(oldText)
	}
	pre := 0
	for pre < len(oldL) && pre < len(newL) && oldL[pre] == newL[pre] {
		pre++
	}
	suf := 0
	for suf < len(oldL)-pre && suf < len(newL)-pre && oldL[len(oldL)-1-suf] == newL[len(newL)-1-suf] {
		suf++
	}
	added, removed := len(newL)-pre-suf, len(oldL)-pre-suf
	if added <= 0 && removed <= 0 {
		return nil
	}
	if added < 0 {
		added = 0
	}
	if removed < 0 {
		removed = 0
	}
	out := make([]located, 0, len(starts))
	for _, s := range starts {
		start := s + pre
		end := start + added - 1
		if end < start {
			end = start
		}
		out = append(out, located{
			start: start, end: end, removed: removed, added: added,
			before: clipSnippet(oldL[pre : pre+removed]), after: clipSnippet(newL[pre : pre+added]),
		})
	}
	return out
}

// maxAddedHashes caps the added-line hashes sent per hunk (as the server).
const maxAddedHashes = 2000

// normalizeLine: CR removed, leading/trailing whitespace trimmed, runs of
// spaces and tabs collapsed to one space (as store.NormalizeLine).
func normalizeLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	var b strings.Builder
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteByte(c)
	}
	return b.String()
}

// lineHash is the first 8 bytes (hex) of the sha1 of the normalised line.
func lineHash(s string) string {
	sum := sha1.Sum([]byte(normalizeLine(s)))
	return hex.EncodeToString(sum[:8])
}

// hashLines hashes lines[from-1 .. to-1] (1-based, inclusive, clamped).
func hashLines(lines []string, from, to int) []string {
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if to < from {
		return nil
	}
	out := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, lineHash(lines[i-1]))
	}
	return out
}

// anchorHunk fills the content anchors of a hunk from the file's lines.
func anchorHunk(c *codeChange, lines []string) {
	if c.AddedLines > 0 {
		end := c.EndLine
		if end-c.StartLine+1 > maxAddedHashes {
			end = c.StartLine + maxAddedHashes - 1
		}
		c.AddedHashes = hashLines(lines, c.StartLine, end)
		c.ContextBefore = hashLines(lines, c.StartLine-2, c.StartLine-1)
		c.ContextAfter = hashLines(lines, c.EndLine+1, c.EndLine+2)
		return
	}
	// A pure deletion sits before StartLine.
	c.ContextBefore = hashLines(lines, c.StartLine-2, c.StartLine-1)
	c.ContextAfter = hashLines(lines, c.StartLine, c.StartLine+1)
}

// locateEdits reads each edited file once and returns the located hunks
// and the hash of the content they were located in (by path).
func locateEdits(root string, edits []pendingEdit) ([]codeChange, map[string]string) {
	if strings.TrimSpace(root) == "" || len(edits) == 0 {
		return nil, nil
	}
	type fileText struct {
		content, hash string
		lines         []string
		ok            bool
	}
	files := map[string]*fileText{}
	hashes := map[string]string{}
	hunkIndex := map[string]int{} // per tool call and path
	var out []codeChange
	for _, e := range edits {
		base := root
		if e.base != "" {
			base = e.base
		}
		fileKey := strings.ToLower(base) + "\x00" + e.rel
		f := files[fileKey]
		if f == nil {
			f = &fileText{}
			if content, hash, err := readTextFile(filepath.Join(base, filepath.FromSlash(e.rel))); err == nil {
				f.content, f.hash, f.ok = strings.ReplaceAll(content, "\r", ""), hash, true
				f.lines = textLines(f.content)
				hashes[e.rel] = hash
			}
			files[fileKey] = f
		}
		if !f.ok {
			continue
		}
		at := ""
		if !e.at.IsZero() {
			at = e.at.UTC().Format(time.RFC3339)
		}
		key := e.toolCallID + "\x00" + e.rel
		for _, p := range e.pieces {
			for _, l := range locatePiece(f.content, p) {
				idx := hunkIndex[key]
				hunkIndex[key] = idx + 1
				c := codeChange{
					Path: e.rel, ToolCallID: e.toolCallID, HunkIndex: idx,
					StartLine: l.start, EndLine: l.end, RemovedLines: l.removed, AddedLines: l.added,
					BeforeSnippet: l.before, AfterSnippet: l.after, AfterHash: f.hash, At: at,
				}
				anchorHunk(&c, f.lines)
				out = append(out, c)
			}
		}
	}
	return out, hashes
}
