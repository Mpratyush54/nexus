package daemon

// code_links.go links captured chats to the code they edited. From each
// captured turn's tool calls (Edit/Write/MultiEdit/NotebookEdit/apply_patch/
// str_replace and the Codex, Cursor and OpenCode equivalents) it takes the
// files the agent changed, makes them workspace-relative (changedRelPath:
// paths outside the workspace and ignored folders are dropped), and posts
// them per session to POST /v1/projects/{id}/code-links with each file's
// content hash. The server links those files to the session and to the
// memories harvested from it, so agents can ask what was decided about a
// file and which chats touched it.
//
// CodeLinksReporter mirrors FilesChangedReporter: Note never blocks (short
// mutex, buffered kick), Run does the I/O, a missing route backs off.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	codeLinksFlushEvery  = 30 * time.Second
	codeLinksMaxBatch    = 200
	codeLinksMaxSessions = 200
	codeLinksMaxAttempts = 3
	codeLinksBackoff     = time.Hour
	codeLinksErrLogGap   = 10 * time.Minute

	// maxHashBytes: larger files are not hashed (generated or data files).
	maxHashBytes = 2 << 20
	// binarySniffBytes is how much of a file is checked for NUL bytes.
	binarySniffBytes = 8000
)

// ---- content hashes ----

var errNotHashable = errors.New("not hashable")

// fileContentHash returns the hex sha256 of a regular text file of at most
// maxHashBytes. Binary files (a NUL byte in the first 8000 bytes), larger
// files, directories and missing files are skipped. The file is streamed,
// never read whole into memory.
func fileContentHash(abs string) (string, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() > maxHashBytes {
		return "", errNotHashable
	}
	fh, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, binarySniffBytes)
	head, err := r.Peek(binarySniffBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return "", err
	}
	for _, b := range head {
		if b == 0 {
			return "", errNotHashable
		}
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, maxHashBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxHashBytes {
		return "", errNotHashable
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// workspaceHashes hashes the workspace-relative paths that are hashable.
func workspaceHashes(root string, rels []string) map[string]string {
	if strings.TrimSpace(root) == "" || len(rels) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, rel := range rels {
		if sum, err := fileContentHash(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			out[rel] = sum
		}
	}
	return out
}

// ---- touched paths from tool calls ----

// editToolNames are tool names (lower case) whose calls change files.
var editToolNames = map[string]bool{
	// Claude Code
	"edit": true, "write": true, "multiedit": true, "multi_edit": true,
	"notebookedit": true, "notebook_edit": true,
	// Anthropic text editor tool / str_replace variants
	"str_replace": true, "strreplace": true, "str_replace_editor": true,
	"str_replace_based_edit_tool": true,
	// Codex / OpenCode
	"apply_patch": true, "patch": true,
	// Cursor, Windsurf, Antigravity, Gemini and friends
	"edit_file": true, "search_replace": true, "replace_file_content": true,
	"multi_replace_file_content": true, "write_file": true, "write_to_file": true,
	"create_file": true, "writefile": true, "edit_notebook": true, "editnotebook": true,
	"replace": true,
}

// shellToolNames may carry an apply_patch invocation (Codex shell tool).
var shellToolNames = map[string]bool{
	"shell": true, "bash": true, "local_shell": true, "container.exec": true, "exec_command": true,
}

var editPathKeys = []string{
	"file_path", "filePath", "FilePath", "path", "TargetFile", "target_file",
	"AbsolutePath", "absolute_path", "notebook_path", "notebookPath", "target_notebook",
}

// patchFiles returns the files named by an apply_patch body ("*** Update
// File:", "*** Add File:", "*** Delete File:", "*** Move to:") or by a
// unified diff ("--- a/x" then "+++ b/x").
func patchFiles(patch string) []string {
	var out []string
	lines := strings.Split(patch, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "+++ ") && i > 0 && strings.HasPrefix(lines[i-1], "--- ") {
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if j := strings.IndexByte(p, '\t'); j >= 0 {
				p = p[:j]
			}
			if p != "/dev/null" && p != "" {
				out = append(out, strings.TrimPrefix(strings.TrimPrefix(p, "b/"), "./"))
			}
			continue
		}
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Update File:", "*** Add File:", "*** Delete File:", "*** Move to:"} {
			if strings.HasPrefix(line, prefix) {
				if p := strings.TrimSpace(strings.TrimPrefix(line, prefix)); p != "" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func looksLikePatch(s string) bool {
	return strings.Contains(s, "*** Begin Patch") || strings.Contains(s, "*** Update File:") ||
		strings.Contains(s, "*** Add File:") ||
		(strings.Contains(s, "\n+++ ") && strings.Contains(s, "\n@@ "))
}

// rawPatchText finds an apply_patch body in a call: a patch/input field of
// the arguments, or a raw string argument.
func rawPatchText(call, args map[string]any) string {
	for _, m := range []map[string]any{args, call} {
		for _, k := range []string{"patch", "Patch", "patchText", "patch_text", "input", "arguments", "diff"} {
			if s, ok := m[k].(string); ok && looksLikePatch(s) {
				return s
			}
		}
	}
	return ""
}

// shellPatchText returns the patch of a shell call that runs apply_patch
// (Codex: {"command": ["apply_patch", "*** Begin Patch ..."]}).
func shellPatchText(args map[string]any) string {
	var parts []string
	switch c := args["command"].(type) {
	case []any:
		for _, v := range c {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
	case []string:
		parts = c
	case string:
		parts = []string{c}
	}
	for _, p := range parts {
		if looksLikePatch(p) {
			return p
		}
	}
	return ""
}

// touchedPathsFromCall returns the raw (unnormalised) paths one tool call
// edited, or nil for reads, shell commands and unknown tools.
func touchedPathsFromCall(call map[string]any) []string {
	// MCP-prefixed names such as "functions.apply_patch" are unprefixed.
	name, args := callToolName(call)
	if name == "" {
		return nil
	}
	if shellToolNames[name] {
		return patchFiles(shellPatchText(args))
	}
	if !editToolNames[name] {
		return nil
	}
	if name == "str_replace_editor" || name == "str_replace_based_edit_tool" {
		switch strings.ToLower(firstString(args, "command")) {
		case "view", "":
			return nil
		}
	}
	var out []string
	if patch := rawPatchText(call, args); patch != "" {
		out = append(out, patchFiles(patch)...)
	}
	if p := strings.TrimSpace(firstString(args, editPathKeys...)); p != "" {
		out = append(out, p)
	}
	return out
}

// TouchedPathsFromEvents returns the repo-relative files the agent edited
// in batch (deduplicated, in first-seen order). Paths in ignored folders,
// and outside root and every other checkout of root's repository
// (edit_paths.go), are dropped.
func TouchedPathsFromEvents(batch []Event, root string) []string {
	locs := touchedLocsFromEvents(batch, root)
	out := make([]string, 0, len(locs))
	for _, l := range locs {
		out = append(out, l.rel)
	}
	return out
}

// touchedLocsFromEvents is TouchedPathsFromEvents with each file's checkout.
func touchedLocsFromEvents(batch []Event, root string) []editLoc {
	seen := map[string]bool{}
	var out []editLoc
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
		for _, call := range calls {
			for _, raw := range touchedPathsFromCall(call) {
				loc, ok := resolveEditPath(root, cwd, raw)
				if !ok || seen[strings.ToLower(loc.rel)] {
					continue
				}
				seen[strings.ToLower(loc.rel)] = true
				out = append(out, loc)
			}
		}
	}
	return out
}

// ---- reporter ----

type sessionTouches struct {
	sessionID string // cloud session id, when the mirror resolved it
	nativeID  string
	harness   string
	paths     map[string]struct{}
	order     []string
	// bases holds the checkout of a path outside root (edit_paths.go).
	bases    map[string]string
	attempts int
	// edits carry the text of the edits for line-level hunks
	// (code_hunks.go); each edit's path is in paths.
	edits []pendingEdit
}

// CodeLinksReporter batches touched files per session and posts them.
type CodeLinksReporter struct {
	root string
	conn func() (base, token, projectID string, client *http.Client, err error)

	FlushEvery      time.Duration
	MaxBatch        int
	NotFoundBackoff time.Duration
	now             func() time.Time

	kick chan struct{}

	mu           sync.Mutex
	pending      map[string]*sessionTouches // native id -> touches
	order        []string
	backoffUntil time.Time
	dropped      int64
	sent         int64
	lastErrLog   time.Time
	// editBytes is the edit text queued across sessions.
	editBytes int
	// noChanges is set when the server rejected the changes field (older
	// servers decode strictly); later reports omit it.
	noChanges bool
}

// NewCodeLinksReporter builds a reporter for workspace root. conn supplies
// the cloud connection at flush time.
func NewCodeLinksReporter(root string, conn func() (base, token, projectID string, client *http.Client, err error)) *CodeLinksReporter {
	return &CodeLinksReporter{
		root:            root,
		conn:            conn,
		FlushEvery:      codeLinksFlushEvery,
		MaxBatch:        codeLinksMaxBatch,
		NotFoundBackoff: codeLinksBackoff,
		now:             time.Now,
		kick:            make(chan struct{}, 1),
		pending:         map[string]*sessionTouches{},
	}
}

func (c *CodeLinksReporter) maxBatch() int {
	if c.MaxBatch <= 0 {
		return codeLinksMaxBatch
	}
	return c.MaxBatch
}

// NoteEvents records the files edited in one session's captured batch.
// cloudID is the mirrored cloud session id ("" when unknown); nativeID is
// the harness session id. Never blocks on I/O.
func (c *CodeLinksReporter) NoteEvents(cloudID, nativeID, harness string, batch []Event) {
	if c == nil {
		return
	}
	c.note(cloudID, nativeID, harness, touchedLocsFromEvents(batch, c.root), EditsFromEvents(batch, c.root))
}

// Note records workspace-relative touched paths for one session.
func (c *CodeLinksReporter) Note(cloudID, nativeID, harness string, rels []string) {
	locs := make([]editLoc, 0, len(rels))
	for _, rel := range rels {
		locs = append(locs, editLoc{base: c.root, rel: rel})
	}
	c.note(cloudID, nativeID, harness, locs, nil)
}

func (c *CodeLinksReporter) note(cloudID, nativeID, harness string, locs []editLoc, edits []pendingEdit) {
	if c == nil || len(locs) == 0 {
		return
	}
	nativeID = strings.TrimSpace(nativeID)
	cloudID = strings.TrimSpace(cloudID)
	if !looksLikeUUID(cloudID) {
		cloudID = ""
	}
	key := nativeID
	if key == "" {
		key = cloudID
	}
	if key == "" {
		return
	}
	now := c.now()
	c.mu.Lock()
	if now.Before(c.backoffUntil) {
		c.mu.Unlock()
		return
	}
	st := c.pending[key]
	if st == nil {
		if len(c.pending) >= codeLinksMaxSessions {
			c.dropped += int64(len(locs))
			c.mu.Unlock()
			return
		}
		st = &sessionTouches{nativeID: nativeID, paths: map[string]struct{}{}}
		c.pending[key] = st
		c.order = append(c.order, key)
	}
	if cloudID != "" {
		st.sessionID = cloudID
	}
	if harness != "" {
		st.harness = harness
	}
	for _, loc := range locs {
		rel := loc.rel
		if _, dup := st.paths[rel]; dup {
			continue
		}
		if len(st.paths) >= 4*c.maxBatch() {
			c.dropped++
			continue
		}
		st.paths[rel] = struct{}{}
		st.order = append(st.order, rel)
		st.setBase(c.root, rel, loc.base)
	}
	if !c.noChanges {
		for _, e := range edits {
			if _, queued := st.paths[e.rel]; !queued {
				continue
			}
			n := e.size()
			if c.editBytes+n > maxPendingEditBytes {
				c.dropped++
				continue
			}
			c.editBytes += n
			st.edits = append(st.edits, e)
		}
	}
	full := len(st.order) >= c.maxBatch()
	c.mu.Unlock()
	if full {
		select {
		case c.kick <- struct{}{}:
		default:
		}
	}
}

// Pending returns how many paths await a flush across sessions.
func (c *CodeLinksReporter) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, st := range c.pending {
		n += len(st.order)
	}
	return n
}

// Run flushes every FlushEvery and when a session's batch is full, until
// ctx ends (one best-effort flush runs on shutdown).
func (c *CodeLinksReporter) Run(ctx context.Context) {
	every := c.FlushEvery
	if every <= 0 {
		every = codeLinksFlushEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.Flush(fctx)
			cancel()
			return
		case <-t.C:
			c.Flush(ctx)
		case <-c.kick:
			c.Flush(ctx)
		}
	}
}

// Flush posts every pending session once (at most MaxBatch paths each) and
// returns how many paths the server accepted.
func (c *CodeLinksReporter) Flush(ctx context.Context) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	keys := append([]string(nil), c.order...)
	c.mu.Unlock()
	sent := 0
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		n, stop := c.flushSession(ctx, key)
		sent += n
		if stop {
			break
		}
	}
	return sent
}

// take removes up to MaxBatch paths of one session, with their edits,
// from the queue. The returned meta carries the taken edits.
func (c *CodeLinksReporter) take(key string) (sessionTouches, []string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Before(c.backoffUntil) {
		c.pending = map[string]*sessionTouches{}
		c.order = nil
		c.editBytes = 0
		return sessionTouches{}, nil, false
	}
	st := c.pending[key]
	if st == nil || len(st.order) == 0 {
		return sessionTouches{}, nil, false
	}
	n := len(st.order)
	if n > c.maxBatch() {
		n = c.maxBatch()
	}
	batch := append([]string(nil), st.order[:n]...)
	st.order = append([]string(nil), st.order[n:]...)
	var bases map[string]string
	for _, p := range batch {
		delete(st.paths, p)
		if b, ok := st.bases[p]; ok {
			if bases == nil {
				bases = map[string]string{}
			}
			bases[p] = b
			delete(st.bases, p)
		}
	}
	var taken, rest []pendingEdit
	for _, e := range st.edits {
		if _, still := st.paths[e.rel]; still {
			rest = append(rest, e)
			continue
		}
		taken = append(taken, e)
		c.editBytes -= e.size()
	}
	st.edits = rest
	if len(st.order) == 0 {
		delete(c.pending, key)
		c.removeOrderLocked(key)
	}
	meta := *st
	meta.edits = taken
	meta.bases = bases
	return meta, batch, true
}

// setBase records the checkout of rel when it is not root.
func (st *sessionTouches) setBase(root, rel, base string) {
	if base == "" || strings.EqualFold(filepath.Clean(base), filepath.Clean(root)) {
		return
	}
	if st.bases == nil {
		st.bases = map[string]string{}
	}
	st.bases[rel] = base
}

// baseOf is the checkout holding rel (root unless recorded otherwise).
func (st *sessionTouches) baseOf(root, rel string) string {
	if b, ok := st.bases[rel]; ok && b != "" {
		return b
	}
	return root
}

func (c *CodeLinksReporter) removeOrderLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i:i], c.order[i+1:]...)
			return
		}
	}
}

// requeue puts a failed batch back (unless the session ran out of
// attempts).
func (c *CodeLinksReporter) requeue(key string, meta sessionTouches, batch []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if meta.attempts+1 >= codeLinksMaxAttempts {
		c.dropped += int64(len(batch))
		return
	}
	st := c.pending[key]
	if st == nil {
		if len(c.pending) >= codeLinksMaxSessions {
			c.dropped += int64(len(batch))
			return
		}
		st = &sessionTouches{sessionID: meta.sessionID, nativeID: meta.nativeID, harness: meta.harness, paths: map[string]struct{}{}}
		c.pending[key] = st
		c.order = append(c.order, key)
	}
	st.attempts = meta.attempts + 1
	var keep []string
	for _, p := range batch {
		if _, dup := st.paths[p]; dup {
			continue
		}
		st.paths[p] = struct{}{}
		st.setBase(c.root, p, meta.baseOf(c.root, p))
		keep = append(keep, p)
	}
	st.order = append(keep, st.order...)
	if !c.noChanges {
		for _, e := range meta.edits {
			if n := e.size(); c.editBytes+n <= maxPendingEditBytes {
				c.editBytes += n
				st.edits = append(st.edits, e)
			}
		}
	}
}

// flushSession posts one batch for key. stop=true ends this flush (not
// connected, or backing off).
func (c *CodeLinksReporter) flushSession(ctx context.Context, key string) (int, bool) {
	meta, batch, ok := c.take(key)
	if !ok {
		return 0, false
	}
	base, token, projectID, client, err := c.conn()
	if err != nil || !looksLikeUUID(projectID) {
		// Not connected yet: keep the batch without spending an attempt.
		meta.attempts--
		c.requeue(key, meta, batch)
		return 0, true
	}
	sort.Strings(batch)
	c.mu.Lock()
	withChanges := !c.noChanges
	c.mu.Unlock()
	var changes []codeChange
	located := map[string]string{}
	if withChanges {
		changes, located = locateEdits(c.root, meta.edits)
	}
	hashes := map[string]string{}
	for _, p := range batch {
		if sum, err := fileContentHash(filepath.Join(meta.baseOf(c.root, p), filepath.FromSlash(p))); err == nil {
			hashes[p] = sum
		}
	}
	for p, h := range located {
		// The hash of the content the hunks were located in.
		hashes[p] = h
	}
	links := make([]map[string]any, 0, len(batch))
	for _, p := range batch {
		link := map[string]any{"path": p, "relation": "touched", "source": "tool_call"}
		if h := hashes[p]; h != "" {
			link["hash"] = h
		}
		links = append(links, link)
	}
	body := map[string]any{"links": links}
	if meta.sessionID != "" {
		body["session_id"] = meta.sessionID
	}
	if meta.nativeID != "" {
		body["session_native_id"] = meta.nativeID
	}
	if meta.harness != "" {
		body["harness"] = meta.harness
	}
	// The server takes at most store.MaxCodeChanges hunks and 2 MiB per
	// request: the links go with the first chunk of hunks, the rest follow
	// as hunk-only reports for the same session.
	chunks := chunkCodeChanges(changes)
	if len(chunks) > 0 {
		body["changes"] = chunks[0]
	}
	endpoint := base + "/v1/projects/" + url.PathEscape(projectID) + "/code-links"
	status, err := mirrorJSON(ctx, client, http.MethodPost, endpoint, token, body, nil)
	if err != nil && status == http.StatusBadRequest && len(chunks) > 0 {
		// Resend the links alone. Only an older server that rejects the
		// unknown changes field switches hunks off for the rest of the run;
		// any other 400 (a bad or oversized batch) loses this batch's hunks
		// only.
		if strings.Contains(err.Error(), "unknown field") {
			c.mu.Lock()
			c.noChanges = true
			c.mu.Unlock()
		} else {
			c.logErr(err)
		}
		delete(body, "changes")
		chunks = nil
		status, err = mirrorJSON(ctx, client, http.MethodPost, endpoint, token, body, nil)
	}
	if err == nil && len(chunks) > 1 {
		delete(body, "links")
		for _, chunk := range chunks[1:] {
			body["changes"] = chunk
			if _, cerr := mirrorJSON(ctx, client, http.MethodPost, endpoint, token, body, nil); cerr != nil {
				c.logErr(cerr)
				break
			}
		}
	}
	switch {
	case err == nil:
		c.mu.Lock()
		c.sent += int64(len(batch))
		c.mu.Unlock()
		return len(batch), false
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented:
		c.enterBackoff("server has no code-links route yet")
		return 0, true
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		c.enterBackoff("token lacks memory:write for code-links")
		return 0, true
	case status == http.StatusBadRequest:
		c.mu.Lock()
		c.dropped += int64(len(batch))
		c.mu.Unlock()
		c.logErr(err)
		return 0, false
	default:
		// 409: the session is not mirrored yet; 5xx / network: transient.
		c.requeue(key, meta, batch)
		c.logErr(err)
		return 0, false
	}
}

func (c *CodeLinksReporter) logErr(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	logIt := c.now().Sub(c.lastErrLog) >= codeLinksErrLogGap
	if logIt {
		c.lastErrLog = c.now()
	}
	c.mu.Unlock()
	if logIt {
		log.Printf("daemon: code-links report failed: %v", err)
	}
}

func (c *CodeLinksReporter) enterBackoff(reason string) {
	d := c.NotFoundBackoff
	if d <= 0 {
		d = codeLinksBackoff
	}
	c.mu.Lock()
	first := c.backoffUntil.IsZero()
	c.backoffUntil = c.now().Add(d)
	c.pending = map[string]*sessionTouches{}
	c.order = nil
	c.editBytes = 0
	c.mu.Unlock()
	if first {
		log.Printf("daemon: code-links reporting paused for %s (%s)", d, reason)
	}
}

// BackoffUntil reports when reporting resumes (zero when not backing off).
func (c *CodeLinksReporter) BackoffUntil() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backoffUntil
}
