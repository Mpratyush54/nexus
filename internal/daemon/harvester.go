// harvester.go is Layer 2 (Transcript Harvesting — the backbone) of the
// passive extraction pipeline (implementation-plan.md Phase 1.3 + Phase 2.2).
//
// It tails agent conversation files on disk and emits CONVERSATION_TURN events
// plus an end-of-session SESSION_TRANSCRIPT_COMPLETE batch trigger. It is
// stdlib-only: polling replaces fsnotify, and SQLite/vscdb files are tracked
// by mtime/size (no sqlite driver) — see
// docs/decisions/2026-09-17-harvester.md.
//
// Ownership note: like interceptor.go / watcher.go, this file only
// coordinates with the rest of the daemon through the local EventEmitter
// interface and the shared Event envelope. It never edits other daemon files.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/adapters"
)

// EventType is the wire-visible type of a daemon-emitted event.
// Names match the event store vocabulary (plan §2.1). This package's shared
// envelope is owned here (interceptor.go uses its own ToolEvent* names to
// avoid colliding with it).
type EventType string

// Event is the daemon-local event envelope. Payload carries the per-type
// fields ({speaker, content, ...} for turns; {turns, turn_count, ...} for
// session batches) without importing internal/store, keeping the daemon
// stdlib-only.
type Event struct {
	Type      EventType      `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

// EventEmitter is the minimal sink interface for daemon events. The real
// daemon wires this to the server POST path; tests use a fake.
type EventEmitter interface {
	Emit(ev Event)
}

// Layer 2 event types emitted by the Harvester.
const (
	// EventConversationTurn is one meaningful dialogue turn from a transcript.
	EventConversationTurn EventType = "CONVERSATION_TURN"
	// EventSessionComplete signals a session went idle; the Memory Processor
	// should run the end-of-session deep analysis pass over the batch.
	EventSessionComplete EventType = "SESSION_TRANSCRIPT_COMPLETE"
)

// Transcript formats supported by the harvester.
const (
	FormatJSONL  = "jsonl"
	FormatSQLite = "sqlite"
	FormatJSON   = "json"
)

const (
	// HarvesterPollInterval matches the plan: 30s polling for SQLite/WAL
	// files (fsnotify doesn't fire on SQLite WAL writes) and the stdlib
	// replacement for fsnotify on jsonl. Named distinctly from the
	// watcher.go DefaultPollInterval (5s for instruction files).
	HarvesterPollInterval = 30 * time.Second
	// DefaultIdleTimeout matches the plan: 5min of no new content triggers
	// the end-of-session deep analysis batch.
	DefaultIdleTimeout = 5 * time.Minute
)

// TranscriptSource is one agent's conversation storage location.
type TranscriptSource struct {
	Agent  string   // e.g. "claude", "cursor", "opencode"
	Dirs   []string // resolved absolute directories to scan
	Format string   // FormatJSONL, FormatSQLite, FormatJSON
}

// Turn is a single parsed dialogue turn.
type Turn struct {
	Speaker   string    // "user" | "assistant" (normalized where possible)
	Content   string    // text content, tool-call blocks stripped
	Timestamp time.Time // zero if the transcript line carried none
}

// Harvester tails agent transcript files for one workspace.
type Harvester struct {
	workspace  string // workspace root (absolute)
	folderName string // base folder name, fallback for workspace matching
	origin     string // git remote origin URL (primary identity, issue #109)
	rootCommit string // git root commit hash (primary identity, issue #109)

	Sources      []TranscriptSource
	PollInterval time.Duration
	IdleTimeout  time.Duration

	emitter EventEmitter // may be nil: TailFile/CheckIdle results still returned

	// now is an injectable clock (tests advance it to simulate idleness).
	now func() time.Time

	mu         sync.Mutex
	offsets    map[string]int64     // jsonl/json path -> last-read byte offset
	sqlite     map[string]fileMeta  // sqlite path -> last observed state
	lastActive map[string]time.Time // path -> last observed content time
	completed  map[string]bool      // path -> SESSION_TRANSCRIPT_COMPLETE sent
	agents     map[string]string    // path -> agent name
	batch      map[string][]Turn    // path -> turns since session start
	// extractors holds per-agent SQLite row extractors (nil value or absent
	// agent = built-in fallback, then liveness-only). Issue #33/#77.
	extractors map[string]SQLiteExtractor
}

// fileMeta tracks SQLite/vscdb files we cannot parse without a driver.
type fileMeta struct {
	size  int64
	mtime time.Time
}

// SQLiteExtractor is the seam for sqlite-format conversation stores
// (Cursor/Copilot/Antigravity .db/.vscdb/.sqlite). Row parsing needs a SQL
// driver, and no SQL driver dep is approved (see ADR-033), so the harvester
// core never imports one. A driver-backed implementation registers itself
// via SetSQLiteExtractor; the harvester calls it with the DB path and a
// lower-bound timestamp and emits one CONVERSATION_TURN per returned Turn.
// No registration (the default) means liveness-only: the harvester refreshes
// idle timers on file change and logs the explicit reason, but never emits
// CONVERSATION_TURN for sqlite rows.
type SQLiteExtractor interface {
	ExtractNewRows(dbPath string, since time.Time) ([]Turn, error)
}

// NewHarvester builds a harvester for workspaceRoot. emitter may be nil
// (events are still returned from TailFile/CheckIdle for tests and for the
// daemon entrypoint to forward). poll <= 0 selects HarvesterPollInterval,
// idle <= 0 selects DefaultIdleTimeout.
func NewHarvester(workspaceRoot string, emitter EventEmitter) *Harvester {
	return NewHarvesterWithPoll(workspaceRoot, emitter, 0, 0)
}

// NewHarvesterWithPoll is NewHarvester with explicit intervals (tests use a
// short poll; production uses HarvesterPollInterval). Workspace identity
// (origin + root commit, issue #109) is resolved best-effort; empty when
// the workspace is not a git repo.
func NewHarvesterWithPoll(workspaceRoot string, emitter EventEmitter, poll, idle time.Duration) *Harvester {
	if poll <= 0 {
		poll = HarvesterPollInterval
	}
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	origin, rootCommit := "", ""
	if strings.TrimSpace(workspaceRoot) != "" {
		origin, rootCommit = FingerprintOf(workspaceRoot)
	}
	return &Harvester{
		workspace:    workspaceRoot,
		folderName:   filepath.Base(filepath.Clean(workspaceRoot)),
		origin:       origin,
		rootCommit:   rootCommit,
		Sources:      ResolveSources(),
		PollInterval: poll,
		IdleTimeout:  idle,
		emitter:      emitter,
		now:          time.Now,
		offsets:      make(map[string]int64),
		sqlite:       make(map[string]fileMeta),
		lastActive:   make(map[string]time.Time),
		completed:    make(map[string]bool),
		agents:       make(map[string]string),
		batch:        make(map[string][]Turn),
		extractors:   make(map[string]SQLiteExtractor),
	}
}

// WorkspaceIdentity returns the harvester's git identity (origin URL and
// root commit) used to prioritize exact matching over folder-name fallback
// (issue #109). Empty strings mean "not a git repo / unknown".
func (h *Harvester) WorkspaceIdentity() (origin, rootCommit string) {
	if h == nil {
		return "", ""
	}
	return h.origin, h.rootCommit
}

// SetSQLiteExtractor registers (or with nil, unregisters) a row extractor
// for one sqlite agent (cursor, copilot, antigravity). Unregistered agents
// stay liveness-only. Issue #33.
func (h *Harvester) SetSQLiteExtractor(agent string, ex SQLiteExtractor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ex == nil {
		delete(h.extractors, agent)
		return
	}
	h.extractors[agent] = ex
}

// SetEmitter swaps the event sink (issue #115: runtime wiring). Nil means
// results are only returned, never forwarded. Safe for concurrent use.
func (h *Harvester) SetEmitter(emitter EventEmitter) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitter = emitter
}

func (h *Harvester) emit(ev Event) {
	if h.emitter != nil {
		h.emitter.Emit(ev)
	}
}

// ResolveSources returns the agent transcript locations from
// implementation-plan.md Phase 1.3. Agent names are cross-checked against
// adapters.Registry() so the harvester stays in sync with the adapter set;
// directories fall back to the hardcoded per-OS list because the registry
// does not expose raw transcript dirs (only backup roots).
func ResolveSources() []TranscriptSource {
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	if appData == "" {
		// Non-Windows roaming equivalent.
		appData = filepath.Join(home, ".config")
	}
	codeWS := filepath.Join(appData, "Code", "User", "workspaceStorage")

	hardcoded := []TranscriptSource{
		{Agent: "claude", Dirs: []string{filepath.Join(home, ".claude", "projects")}, Format: FormatJSONL},
		{Agent: "opencode", Dirs: []string{
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(home, ".local", "share", "opencode"),
		}, Format: FormatJSONL},
		{Agent: "cursor", Dirs: []string{
			filepath.Join(home, ".cursor"),
			codeWS,
		}, Format: FormatSQLite},
		{Agent: "antigravity", Dirs: []string{
			filepath.Join(appData, "Antigravity", "User", "workspaceStorage"),
		}, Format: FormatSQLite},
		{Agent: "copilot", Dirs: []string{codeWS}, Format: FormatSQLite},
	}

	// Reuse adapters pkg: keep only agents the registry knows about.
	known := map[string]bool{}
	for _, a := range adapters.Registry() {
		known[a.Name()] = true
	}
	var out []TranscriptSource
	for _, s := range hardcoded {
		if known[s.Agent] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		// Registry unavailable/empty (e.g. trimmed build): fall back to the
		// hardcoded list rather than harvesting nothing.
		return hardcoded
	}
	return out
}

// Origin returns the workspace's git remote origin URL ("" when unknown).
// Together with RootCommit it is the canonical identity to pass to the
// server's ResolveProject (canonical_url, root_commit, folder_name).
// Issue #109.
func (h *Harvester) Origin() string { return h.origin }

// RootCommit returns the workspace's git root-commit hash ("" when unknown).
// Issue #109.
func (h *Harvester) RootCommit() string { return h.rootCommit }

// normalizeGitURL strips scheme/user/suffix noise so equivalent remotes
// compare equal. It mirrors store.NormalizeGitURL and is kept local (not
// imported) so the daemon stays stdlib-only — same precedent as
// internal/migrate's local copy. The two must stay in sync.
// Issue #109.
func normalizeGitURL(raw string) string {
	s := strings.TrimSpace(raw)
	for _, scheme := range []string{"https://", "http://", "ssh://", "git+ssh://", "git://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	// Strip any "user@" (covers "git@host" both bare and post-scheme).
	if i := strings.Index(s, "@"); i >= 0 && (strings.Index(s, "/") == -1 || i < strings.Index(s, "/")) {
		s = s[i+1:]
	}
	// scp-like "host:path" -> "host/path".
	s = strings.Replace(s, ":", "/", 1)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	return strings.ToLower(s)
}

// MatchLevel ranks workspace-identity evidence, strongest first. It mirrors
// MemStore.ResolveProject priority: canonical URL > root commit >
// folder name. Issue #109.
type MatchLevel int

const (
	// MatchNone means the candidate is not attributable to the workspace.
	MatchNone MatchLevel = iota
	// MatchFolderName is the weakest signal: the transcript path merely
	// contains the workspace folder base name. Generic dir names
	// ("api", "server", "frontend") collide here — it is a fallback only.
	MatchFolderName
	// MatchRootCommit is a strong signal: equal git root-commit hashes.
	MatchRootCommit
	// MatchRemoteURL is the strongest signal: equal normalized git remotes.
	MatchRemoteURL
)

// ClassifyWorkspaceMatch ranks a candidate transcript attribution against a
// workspace identity triple (origin, rootCommit, folderName). The candidate
// carries its own origin/root ("" when the transcript format records none)
// plus its path on disk. Pure function — no git binary — so the priority
// ordering is unit-testable. Issue #109.
func ClassifyWorkspaceMatch(origin, rootCommit, folderName, candOrigin, candRoot, candPath string) MatchLevel {
	n, c := normalizeGitURL(origin), normalizeGitURL(candOrigin)
	urlsKnown := n != "" && c != ""
	if urlsKnown && n == c {
		return MatchRemoteURL
	}
	r, cr := strings.TrimSpace(rootCommit), strings.TrimSpace(candRoot)
	rootsKnown := r != "" && cr != ""
	if rootsKnown && r == cr {
		// Equal roots rescue disagreeing URLs (forks, renamed remotes).
		return MatchRootCommit
	}
	if urlsKnown || rootsKnown {
		// Both sides carry identity and none of it agrees: these are
		// different repos, so the folder-name fallback must NOT rescue
		// the match — that fallback is the misattribution vector
		// (generic dir names like "api" / "server") this ordering
		// exists to close. Single-sided identity stays inconclusive
		// and falls through to the path check below.
		return MatchNone
	}
	if folderName != "" && folderName != "." && folderName != string(filepath.Separator) {
		if strings.Contains(strings.ToLower(candPath), strings.ToLower(folderName)) {
			return MatchFolderName
		}
	} else {
		// No folder filter configured: match-all, as MatchesWorkspace does.
		return MatchFolderName
	}
	return MatchNone
}

// MatchesCandidate reports whether a transcript with the given origin/root
// identity at path belongs to this workspace. Identity evidence outranks
// the folder-name fallback, so unrelated repos sharing a generic directory
// name ("api", "server") are no longer misattributed once either side
// records git identity. Issue #109.
func (h *Harvester) MatchesCandidate(candOrigin, candRoot, path string) bool {
	return ClassifyWorkspaceMatch(h.origin, h.rootCommit, h.folderName, candOrigin, candRoot, path) != MatchNone
}

// MatchesWorkspace reports whether a transcript path belongs to this
// workspace (issue #109). Matching priority: git origin/root-commit
// identity first, then exact folder-segment match. The old fuzzy
// substring fallback ("api" matching any path containing "api") is gone:
// generic directory names no longer misattribute unrelated repos.
//
//   - Empty/degenerate workspace matches all (no filter).
//   - Otherwise the path matches when any slash-separated segment equals
//     the workspace folder name (case-insensitive), or when the path
//     contains the normalized origin fragment or root-commit hash.
func (h *Harvester) MatchesWorkspace(path string) bool {
	if h.folderName == "" || h.folderName == "." || h.folderName == string(filepath.Separator) {
		return true
	}
	low := strings.ToLower(path)
	// Primary: git identity fragments (origin URL tail / root commit).
	if h.origin != "" {
		frag := strings.ToLower(h.origin)
		// Match on the repo tail (e.g. "nexus" from github.com/org/nexus)
		// so encoded transcript dirs still hit without fuzzy collisions.
		if tail := originTail(frag); tail != "" && strings.Contains(low, tail) {
			return true
		}
	}
	if h.rootCommit != "" && strings.Contains(low, strings.ToLower(h.rootCommit)) {
		return true
	}
	// Fallback: exact path-segment match on the folder name (not substring).
	want := strings.ToLower(h.folderName)
	for _, seg := range strings.FieldsFunc(low, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == want {
			return true
		}
		// Claude-style encoded dirs use "-" for "/" (e.g. "--home--user--proj"):
		// accept when the segment contains the folder as a dash-delimited token.
		for _, tok := range strings.Split(seg, "-") {
			if tok == want {
				return true
			}
		}
	}
	return false
}

// originTail extracts the repo tail from a git origin URL for matching
// (e.g. "git@github.com:org/nexus.git" -> "nexus").
func originTail(origin string) string {
	o := strings.ToLower(strings.TrimSpace(origin))
	o = strings.TrimSuffix(o, ".git")
	o = strings.TrimSuffix(o, "/")
	if i := strings.LastIndexAny(o, "/:"); i >= 0 {
		o = o[i+1:]
	}
	// Keep only alphanumerics/dash/underscore to avoid regex noise.
	var b strings.Builder
	for _, r := range o {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) < 3 {
		return "" // too generic to match safely (e.g. "api" alone)
	}
	return s
}

// sessionID derives a stable session id from the transcript path.
func sessionID(path string) string {
	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// noiseTypes are JSONL record types that carry no dialogue value. Layer 1
// (tool interception) already captures tool activity, so the harvester skips
// these to keep CONVERSATION_TURN events to meaningful turns only.
var noiseTypes = map[string]bool{
	"tool_use": true, "tool_result": true, "tool_call": true,
	"function_call": true, "function_result": true,
	"api_request": true, "api_response": true,
	"system": true, "summary": true, "snapshot": true,
	"file-history-snapshot": true, "queue-operation": true,
	"background_task": true, "mcp_tool_call": true, "mcp_tool_result": true,
	"thinking": true, "progress": true,
}

// maxJSONLLineBytes bounds a single JSONL line (issue #118): a 1MB
// single-line record must not stall tailing. Overlong lines are skipped
// (truncated to this bound before parse attempt).
const maxJSONLLineBytes = 1 << 20

// ParseTurns parses JSONL dialogue turns from r, skipping tool-call noise and
// blank records. It tolerates per-line schema drift across agents (Claude,
// Cursor, OpenCode) by probing several field names. Overlong single lines
// (>1MB) are skipped, never stalling the scan.
func ParseTurns(r io.Reader) ([]Turn, error) {
	var turns []Turn
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxJSONLLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) > maxJSONLLineBytes {
			continue
		}
		t, ok := parseTurnLine(line)
		if ok {
			turns = append(turns, t)
		}
	}
	if err := sc.Err(); err != nil {
		// A single overlong line (ErrTooLong) skips that line, not the file.
		if err == bufio.ErrTooLong {
			return turns, nil
		}
		return turns, err
	}
	return turns, nil
}

// parseTurnLine parses one JSONL line. ok=false means "skip, not an error"
// (noise, blank, or unparsable line — transcripts must never break tailing).
func parseTurnLine(line []byte) (t Turn, ok bool) {
	if len(strings.TrimSpace(string(line))) == 0 {
		return Turn{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return Turn{}, false
	}
	if noiseTypes[strings.ToLower(strField(m, "type"))] {
		return Turn{}, false
	}
	role := firstNonEmpty(
		strField(m, "role"),
		strField(m, "speaker"),
		strField(m, "author"),
		nestedStr(m, "message", "role"),
	)
	if r := strings.ToLower(role); r == "tool" || r == "function" {
		return Turn{}, false
	}
	content := extractContent(m)
	if strings.TrimSpace(content) == "" {
		return Turn{}, false
	}
	t = Turn{
		Speaker:   normalizeSpeaker(role),
		Content:   strings.TrimSpace(content),
		Timestamp: extractTime(m),
	}
	return t, true
}

// extractContent pulls text from the common content shapes, dropping
// tool_use/input blocks (Layer 1 already captures those).
func extractContent(m map[string]any) string {
	if s := messageContent(m["content"]); s != "" {
		return s
	}
	if mm, ok := m["message"].(map[string]any); ok {
		if s := messageContent(mm["content"]); s != "" {
			return s
		}
		if s, _ := mm["text"].(string); s != "" {
			return s
		}
	}
	for _, k := range []string{"text", "body", "input_text"} {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

// messageContent renders a content value: plain string as-is, arrays of
// blocks concatenated from text-bearing blocks only.
func messageContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, item := range c {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			bt := strings.ToLower(strField(im, "type"))
			switch bt {
			case "text", "input_text", "output_text", "message", "":
				if s, _ := im["text"].(string); s != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(s)
				}
			default:
				// tool_use, image, server_tool_use, etc.: skipped as noise.
			}
		}
		return b.String()
	}
	return ""
}

func normalizeSpeaker(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "human", "user":
		return "user"
	case "assistant", "ai", "agent", "claude", "gpt", "model":
		return "assistant"
	case "":
		return "unknown"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func extractTime(m map[string]any) time.Time {
	for _, k := range []string{"timestamp", "created_at", "createdAt", "time"} {
		if v, present := m[k]; present {
			if t, ok := parseTimeValue(v); ok {
				return t
			}
		}
	}
	if mm, ok := m["message"].(map[string]any); ok {
		for _, k := range []string{"timestamp", "created_at", "createdAt"} {
			if v, present := mm[k]; present {
				if t, ok := parseTimeValue(v); ok {
					return t
				}
			}
		}
	}
	return time.Time{}
}

func parseTimeValue(v any) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		if tm, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return tm, true
		}
		if tm, err := time.Parse(time.RFC3339, s); err == nil {
			return tm, true
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixToTime(n), true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return floatUnixToTime(f), true
		}
	case float64:
		return floatUnixToTime(t), true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return floatUnixToTime(f), true
		}
	case int64:
		return unixToTime(t), true
	}
	return time.Time{}, false
}

func unixToTime(n int64) time.Time {
	// Heuristic: millis vs seconds.
	if n > 1e12 {
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

func floatUnixToTime(f float64) time.Time {
	sec, frac := int64(f), f-float64(int64(f))
	return time.Unix(sec, int64(frac*1e9)).UTC()
}

func strField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func nestedStr(m map[string]any, outer, inner string) string {
	if mm, ok := m[outer].(map[string]any); ok {
		s, _ := mm[inner].(string)
		return s
	}
	return ""
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// turnPayload renders a Turn into the CONVERSATION_TURN payload shape.
func turnPayload(action, agent, path string, t Turn) map[string]any {
	p := map[string]any{
		"action":     action,
		"agent":      agent,
		"path":       path,
		"session_id": sessionID(path),
		"speaker":    t.Speaker,
		"content":    t.Content,
	}
	if !t.Timestamp.IsZero() {
		p["timestamp"] = t.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return p
}

// TailFile reads content appended to a jsonl/json transcript since the last
// call (byte-offset tailing), parses new turns, updates session state, and
// emits one CONVERSATION_TURN per meaningful turn. Truncated files (log
// rotation) reset the offset to zero.
func (h *Harvester) TailFile(path string) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	off := h.offsets[path]
	if st.Size() < off {
		off = 0 // truncated or rotated: re-read from the start
	}
	h.mu.Unlock()

	if st.Size() == off {
		return nil, nil
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	// Only consume through the last full line so torn writes (no trailing
	// newline) are retried on the next poll instead of failing JSON parse.
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	consumable := buf
	if len(buf) > 0 && buf[len(buf)-1] != '\n' {
		if i := lastNewline(buf); i >= 0 {
			consumable = buf[:i+1]
		} else {
			h.mu.Lock()
			h.offsets[path] = off // nothing consumable yet
			h.mu.Unlock()
			return nil, nil
		}
	}

	var turns []Turn
	sc := bufio.NewScanner(strings.NewReader(string(consumable)))
	sc.Buffer(make([]byte, 64<<10), maxJSONLLineBytes)
	for sc.Scan() {
		if t, ok := parseTurnLine(sc.Bytes()); ok {
			turns = append(turns, t)
		}
	}
	if err := sc.Err(); err != nil {
		// Overlong single line: skip it but still advance the offset below.
		if err != bufio.ErrTooLong {
			return nil, err
		}
	}

	now := h.now().UTC()
	h.mu.Lock()
	h.offsets[path] = off + int64(len(consumable))
	agent := h.agents[path]
	if len(turns) > 0 {
		last := now
		for i := range turns {
			if turns[i].Timestamp.IsZero() {
				turns[i].Timestamp = now
			}
			if turns[i].Timestamp.After(last) {
				last = turns[i].Timestamp
			}
		}
		h.lastActive[path] = last
		delete(h.completed, path) // new activity re-arms idle detection
		h.batch[path] = append(h.batch[path], turns...)
	}
	h.mu.Unlock()

	for _, t := range turns {
		h.emit(Event{
			Type:      EventConversationTurn,
			Payload:   turnPayload("conversation_turn", agent, path, t),
			CreatedAt: now,
		})
	}
	return turns, nil
}

// TrackSQLite records the mtime/size of a SQLite/vscdb transcript file.
// Without a registered SQLiteExtractor the daemon cannot read rows; a change
// only marks session activity (deferring the idle trigger) and returns true.
// With an extractor registered (SetSQLiteExtractor), changed files yield one
// CONVERSATION_TURN per new row. The later SESSION_TRANSCRIPT_COMPLETE event
// carries a hint when no rows were parsed so the Memory Processor knows full
// extraction must happen out-of-band.
func (h *Harvester) TrackSQLite(path string, info os.FileInfo) bool {
	now := h.now().UTC()
	h.mu.Lock()
	prev, seen := h.sqlite[path]
	cur := fileMeta{size: info.Size(), mtime: info.ModTime()}
	h.sqlite[path] = cur
	agent := h.agents[path]
	if !seen {
		if _, ok := h.lastActive[path]; !ok {
			h.lastActive[path] = now
		}
		h.mu.Unlock()
		return false
	}
	if cur == prev {
		h.mu.Unlock()
		return false
	}
	ex := h.extractors[agent]
	since := h.lastActive[path]
	h.mu.Unlock()

	// No registered extractor: try the built-in stdlib fallback first
	// (issue #77: sqlite3 CLI when present, else raw string-scan for
	// VSCode/Cursor storage payloads). Opaque test fixtures yield zero
	// turns and fall through to liveness-only, preserving prior behavior.
	if ex == nil {
		if turns := extractSQLiteFallback(path, since); len(turns) > 0 {
			last := now
			h.mu.Lock()
			for i := range turns {
				if turns[i].Timestamp.IsZero() {
					turns[i].Timestamp = now
				}
				if turns[i].Timestamp.After(last) {
					last = turns[i].Timestamp
				}
			}
			h.lastActive[path] = last
			delete(h.completed, path)
			h.batch[path] = append(h.batch[path], turns...)
			h.mu.Unlock()
			for _, t := range turns {
				h.emit(Event{
					Type:      EventConversationTurn,
					Payload:   turnPayload("conversation_turn", agent, path, t),
					CreatedAt: now,
				})
			}
			return true
		}
		log.Printf("harvester: sqlite source %q (%s) has no SQLiteExtractor registered — liveness-only (no SQL driver dep, see ADR-033)", agent, path)
		h.mu.Lock()
		h.lastActive[path] = now
		delete(h.completed, path) // new activity re-arms idle detection
		h.mu.Unlock()
		return true
	}
	turns, err := ex.ExtractNewRows(path, since)
	if err != nil {
		log.Printf("harvester: sqlite extractor for %q (%s) failed: %v; keeping liveness only", agent, path, err)
		h.mu.Lock()
		h.lastActive[path] = now
		delete(h.completed, path)
		h.mu.Unlock()
		return true
	}
	if len(turns) == 0 {
		h.mu.Lock()
		h.lastActive[path] = now
		delete(h.completed, path)
		h.mu.Unlock()
		return true
	}
	last := now
	h.mu.Lock()
	for i := range turns {
		if turns[i].Timestamp.IsZero() {
			turns[i].Timestamp = now
		}
		if turns[i].Timestamp.After(last) {
			last = turns[i].Timestamp
		}
	}
	h.lastActive[path] = last
	delete(h.completed, path)
	h.batch[path] = append(h.batch[path], turns...)
	h.mu.Unlock()
	for _, t := range turns {
		h.emit(Event{
			Type:      EventConversationTurn,
			Payload:   turnPayload("conversation_turn", agent, path, t),
			CreatedAt: now,
		})
	}
	return true
}

// CheckIdle emits SESSION_TRANSCRIPT_COMPLETE for every session with no new
// content for IdleTimeout. Each session fires once until new activity
// re-arms it. Returned events were also passed to the emitter.
func (h *Harvester) CheckIdle() []Event {
	now := h.now().UTC()
	h.mu.Lock()
	var due []string
	for path, last := range h.lastActive {
		if !h.completed[path] && now.Sub(last) >= h.IdleTimeout {
			due = append(due, path)
		}
	}
	var out []Event
	for _, path := range due {
		h.completed[path] = true
		turns := append([]Turn(nil), h.batch[path]...)
		detail := strconv.Itoa(len(turns)) + " turns harvested"
		if _, isSQLite := h.sqlite[path]; isSQLite && len(turns) == 0 {
			detail = "sqlite/vscdb source: rows not parsed (no driver); " +
				"deep extraction must read the store out-of-band"
		}
		flat := make([]any, 0, len(turns))
		for _, t := range turns {
			m := map[string]any{"speaker": t.Speaker, "content": t.Content}
			if !t.Timestamp.IsZero() {
				m["timestamp"] = t.Timestamp.UTC().Format(time.RFC3339Nano)
			}
			flat = append(flat, m)
		}
		out = append(out, Event{
			Type: EventSessionComplete,
			Payload: map[string]any{
				"action":     "session_transcript_complete",
				"agent":      h.agents[path],
				"path":       path,
				"session_id": sessionID(path),
				"turn_count": len(turns),
				"turns":      flat,
				"detail":     detail,
			},
			CreatedAt: now,
		})
	}
	h.mu.Unlock()
	for _, ev := range out {
		h.emit(ev)
	}
	return out
}

// Harvester walk bounds (issue #118): the full-home walk every 30s is
// capped so a huge home directory cannot stall the daemon.
const (
	// maxWalkFiles caps files visited per ScanAndTail pass.
	maxWalkFiles = 2000
	// maxWalkDepth caps directory depth below each source dir.
	maxWalkDepth = 6
	// maxWalkFileBytes skips sqlite/jsonl files larger than 64MB.
	maxWalkFileBytes = 64 << 20
)

// harvesterSkipDirs are never descended into during transcript walks.
var harvesterSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	"out": true, ".next": true, "__pycache__": true, ".venv": true,
	"venv": true, "target": true, "bin": true, "obj": true,
	"coverage": true, ".idea": true, ".vscode": true, "Library": true,
}

// ScanAndTail walks all sources, tailing jsonl/json transcripts and tracking
// sqlite/vscdb files, restricted to this workspace. Files classified NEVER by
// adapters.ClassifyPath (credentials, secrets) are never touched. Walks are
// bounded (file count, depth, size, skip dirs) per issue #118.
func (h *Harvester) ScanAndTail() error {
	for _, src := range h.Sources {
		for _, dir := range src.Dirs {
			_ = h.scanDir(src, dir)
		}
	}
	return nil
}

func (h *Harvester) scanDir(src TranscriptSource, dir string) error {
	if dir == "" {
		return nil
	}
	visited := 0
	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if visited > maxWalkFiles {
			return filepath.SkipDir
		}
		if info.IsDir() {
			// Depth bound + skip rebuildables/tooling dirs.
			rel, rerr := filepath.Rel(dir, p)
			if rerr == nil && rel != "." {
				depth := len(strings.Split(rel, string(filepath.Separator)))
				if depth > maxWalkDepth {
					return filepath.SkipDir
				}
			}
			if harvesterSkipDirs[filepath.Base(p)] {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if info.Size() > maxWalkFileBytes {
			return nil
		}
		if adapters.ClassifyPath(p) == adapters.Never {
			return nil
		}
		if !h.MatchesWorkspace(p) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		switch src.Format {
		case FormatJSONL:
			if ext != ".jsonl" {
				return nil
			}
			h.mu.Lock()
			h.agents[p] = src.Agent
			h.mu.Unlock()
			_, _ = h.TailFile(p)
		case FormatJSON:
			if ext != ".json" {
				return nil
			}
			h.mu.Lock()
			h.agents[p] = src.Agent
			h.mu.Unlock()
			_, _ = h.TailFile(p)
		case FormatSQLite:
			if ext != ".db" && ext != ".sqlite" && ext != ".sqlite3" && ext != ".vscdb" {
				return nil
			}
			h.mu.Lock()
			h.agents[p] = src.Agent
			h.mu.Unlock()
			_ = h.TrackSQLite(p, info)
		}
		return nil
	})
}

// Start runs the poll loop (ScanAndTail + CheckIdle every PollInterval)
// until ctx is done. Stdlib polling replaces fsnotify per the decision doc.
func (h *Harvester) Start(ctx context.Context) {
	t := time.NewTicker(h.PollInterval)
	defer t.Stop()
	_ = h.ScanAndTail()
	_ = h.CheckIdle()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = h.ScanAndTail()
			_ = h.CheckIdle()
		}
	}
}
