// Package daemon implements the local workspace daemon (plan §1.3).
//
// This file is Layer 2 — the Transcript Harvester, the backbone of passive
// memory extraction (plan §1.3 "Transcript Harvester", §2.2 "Layer 2").
//
// It tails agent conversation files on disk — completely invisible to the
// user and the agent — and emits CONVERSATION_TURN events, one per meaningful
// turn. When a session goes quiet for 5+ minutes it emits
// SESSION_TRANSCRIPT_COMPLETE so the Memory Processor can run its highest-
// quality end-of-session extraction pass.
//
// Design rules for this file:
//   - Read-only: transcript files are opened read-only and never modified.
//   - Zero UX disruption: no MCP sampling, no prompts, no writes.
//   - Adapter reuse: source directories mirror adapters/registry.go, and
//     file filtering / project attribution reuse adapters.ClassifyPath and
//     adapters.ProjectOf. Stdlib + fsnotify only (plan §1.9).
//   - Testability: ParseJSONLTurn, TailTurns/OffsetTracker and IdleDetector
//     are pure with an injectable clock; the live fsnotify + poll loop is a
//     thin shell around them.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"central-memory/adapters"
	"central-memory/internal/project"
)

// Event types emitted by the harvester (plan §2.1).
const (
	EventConversationTurn          = "CONVERSATION_TURN"
	EventSessionTranscriptComplete = "SESSION_TRANSCRIPT_COMPLETE"
)

// Tuning constants (plan §1.3 behaviors 3/6 and end-of-session analysis).
const (
	// IdleAfter is the quiet period after which a session is considered
	// ended and SESSION_TRANSCRIPT_COMPLETE is emitted.
	IdleAfter = 5 * time.Minute
	// SQLitePollInterval polls SQLite/vscdb conversation stores because
	// fsnotify does not fire reliably on SQLite WAL writes.
	SQLitePollInterval = 30 * time.Second
	// IdleSweepInterval is how often the live loop checks for idle sessions.
	IdleSweepInterval = time.Minute
	// MaxTurnContent caps emitted turn content so a single dumped turn
	// cannot flood the event stream (cf. interceptor's 4KB stdout cap).
	MaxTurnContent = 8000
)

// EventSink receives harvested events. It mirrors the interceptor's sink
// style (interceptor.go, Wave 1 #4); when that file lands, hoist this type
// to a shared daemon file — the signature must stay identical.
type EventSink func(eventType string, payload map[string]any)

// Clock is an injectable time source for tests.
type Clock func() time.Time

// Turn is one parsed conversation turn.
type Turn struct {
	Speaker   string    // "user" | "assistant" (normalized)
	Content   string    // trimmed text content
	Timestamp time.Time // zero when the line carries no parseable time
	SessionID string    // filled by TailTurns/processFile (agent::path)
	Agent     string    // filled by TailTurns/processFile
	Path      string    // transcript file the turn was read from
}

// ErrSkippedEntry marks a line that parses but carries no meaningful turn:
// tool-call-only blocks (Layer 1 already captures those), empty content, or
// system/tool boilerplate. Callers skip these silently.
var ErrSkippedEntry = errors.New("harvester: line carries no meaningful turn")

// ParseJSONLTurn parses one JSONL transcript line into a Turn. It is pure
// and format-tolerant: Claude ({"type":"human",...}), OpenCode/cursor-style
// ({"role":"user","content":...}), and {"speaker":...} shapes all work, with
// one envelope level (message/payload/data) unwrapped when the top level
// carries no role/content. Content arrays are concatenated from text parts;
// tool_use / function_call / tool_result-only entries return ErrSkippedEntry.
func ParseJSONLTurn(line []byte) (Turn, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return Turn{}, fmt.Errorf("%w: blank line", ErrSkippedEntry)
	}
	var top map[string]any
	if err := json.Unmarshal([]byte(trimmed), &top); err != nil {
		return Turn{}, fmt.Errorf("harvester: bad JSONL: %w", err)
	}

	get := func(keys ...string) (any, bool) {
		for _, k := range keys {
			if v, ok := top[k]; ok && v != nil {
				return v, true
			}
		}
		// One envelope level for wrapped shapes.
		for _, env := range []string{"message", "payload", "data"} {
			if m, ok := top[env].(map[string]any); ok {
				for _, k := range keys {
					if v, ok := m[k]; ok && v != nil {
						return v, true
					}
				}
			}
		}
		return nil, false
	}

	speaker := ""
	if v, ok := get("speaker", "role", "author", "sender", "type"); ok {
		speaker = normalizeSpeaker(stringVal(v))
	}
	// A bare {"role":...} nested only inside message (Claude assistant
	// blocks) is caught by the envelope lookup above.
	switch speaker {
	case "tool", "function", "system":
		return Turn{}, fmt.Errorf("%w: %s speaker", ErrSkippedEntry, speaker)
	}
	if speaker == "" {
		// Some OpenCode lines carry parts[] with no role; treat text-only
		// lines as assistant output only when content exists — otherwise
		// the entry is metadata (summary, file snapshots) and skipped.
		speaker = "assistant"
	}

	rawContent, ok := get("content", "text", "body")
	if !ok {
		// OpenCode-style {"parts":[...]} at top level.
		if v, found := get("parts"); found {
			rawContent, ok = v, true
		}
	}
	if !ok {
		return Turn{}, fmt.Errorf("%w: no content field", ErrSkippedEntry)
	}
	content := extractText(rawContent)
	if strings.TrimSpace(content) == "" {
		return Turn{}, fmt.Errorf("%w: empty/tool-only content", ErrSkippedEntry)
	}

	var ts time.Time
	if v, ok := get("timestamp", "created_at", "createdAt", "created", "time", "ts", "date"); ok {
		ts = parseTimestamp(v)
	}
	return Turn{Speaker: speaker, Content: content, Timestamp: ts}, nil
}

// normalizeSpeaker maps the adapter-specific zoo to user/assistant.
func normalizeSpeaker(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "human", "user", "people", "person", "customer":
		return "user"
	case "assistant", "ai", "model", "bot", "agent":
		return "assistant"
	case "tool", "function", "tools", "tool_use", "tool_result":
		return "tool"
	case "system", "developer":
		return "system"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// stringVal coerces numbers/bools to strings for speaker lookup.
func stringVal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		// {"role": {"name": ...}} — unlikely, best effort.
		if s, ok := t["name"].(string); ok {
			return s
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// extractText flattens string | array-of-parts | nested-object content.
// Text comes only from text-ish part types; tool_use / function_call /
// tool_result blocks are Layer 1's territory and contribute nothing here.
func extractText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		var sb strings.Builder
		for _, p := range t {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			pt, _ := pm["type"].(string)
			switch strings.ToLower(pt) {
			case "", "text", "input_text", "output_text":
				// "" covers untyped {"text": ...} parts.
			default:
				continue // tool_use, tool_result, function_call, image, ...
			}
			var s string
			if txt, ok := pm["text"].(string); ok {
				s = txt
			} else if c, ok := pm["content"]; ok {
				s = extractText(c)
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(s)
		}
		return sb.String()
	case map[string]any:
		if c, ok := t["content"]; ok {
			return extractText(c)
		}
		if txt, ok := t["text"].(string); ok {
			return strings.TrimSpace(txt)
		}
		return ""
	default:
		return ""
	}
}

// parseTimestamp accepts RFC3339 strings, common datetime layouts, unix
// seconds (int/float) and unix millis. Zero time on anything else.
func parseTimestamp(v any) time.Time {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}
		}
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339,
			"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
		} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts
			}
		}
		return time.Time{}
	case float64:
		return unixToTime(int64(t))
	case int64:
		return unixToTime(t)
	case int:
		return unixToTime(int64(t))
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return unixToTime(i)
		}
		return time.Time{}
	default:
		return time.Time{}
	}
}

// unixToTime treats >1e12 as millis, else seconds.
func unixToTime(i int64) time.Time {
	if i > 1_000_000_000_000 {
		return time.UnixMilli(i).UTC()
	}
	if i < 0 {
		return time.Time{}
	}
	return time.Unix(i, 0).UTC()
}

// OffsetTracker remembers the last-read byte offset per transcript file so
// restarts and re-polls never re-emit turns. Safe for concurrent use.
type OffsetTracker struct {
	mu  sync.Mutex
	off map[string]int64
}

// NewOffsetTracker returns an empty tracker.
func NewOffsetTracker() *OffsetTracker { return &OffsetTracker{off: map[string]int64{}} }

// Get returns the stored offset (0 for unseen files).
func (t *OffsetTracker) Get(path string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.off[path]
}

// Set stores the offset for path.
func (t *OffsetTracker) Set(path string, offset int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.off[path] = offset
}

// TailTurns reads turns appended to path since offset and returns them with
// the new offset. A file smaller than offset was rotated/truncated and is
// re-read from 0. A trailing partial line (still being written) is held back
// until it is newline-terminated. Line-level errors (bad JSON, tool noise)
// are skipped silently; only IO errors are returned.
func TailTurns(path string, offset int64) (turns []Turn, newOffset int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, offset, err
	}
	if info.Size() < offset {
		offset = 0 // rotated or truncated
	}
	if info.Size() == offset {
		return nil, offset, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	lastNL := -1
	for i, b := range buf {
		if b == '\n' {
			lastNL = i
		}
	}
	if lastNL < 0 {
		return nil, offset, nil // only a partial line so far
	}
	consumed := buf[:lastNL+1]
	newOffset = offset + int64(len(consumed))
	for _, ln := range strings.Split(string(consumed), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		turn, perr := ParseJSONLTurn([]byte(ln))
		if perr != nil {
			continue // metadata / noise / corrupt line: skip
		}
		turn.Path = path
		turns = append(turns, turn)
	}
	return turns, newOffset, nil
}

// IdleDetector tracks last-activity per session and reports sessions quiet
// longer than idleAfter. The clock is injectable for deterministic tests.
type IdleDetector struct {
	mu        sync.Mutex
	last      map[string]time.Time
	idleAfter time.Duration
	now       Clock
}

// NewIdleDetector builds a detector; a nil clock means time.Now.
func NewIdleDetector(idleAfter time.Duration, now Clock) *IdleDetector {
	if now == nil {
		now = time.Now
	}
	return &IdleDetector{last: map[string]time.Time{}, idleAfter: idleAfter, now: now}
}

// Touch records activity for session now.
func (d *IdleDetector) Touch(sessionID string) { d.TouchAt(sessionID, d.now()) }

// TouchAt records activity at an explicit time (tests).
func (d *IdleDetector) TouchAt(sessionID string, t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last[sessionID] = t
}

// Last returns the last activity time for session.
func (d *IdleDetector) Last(sessionID string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.last[sessionID]
	return t, ok
}

// Remove forgets a session.
func (d *IdleDetector) Remove(sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.last, sessionID)
}

// IdleSessions returns sessions with no activity for >= idleAfter.
func (d *IdleDetector) IdleSessions() []string {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for id, last := range d.last {
		if now.Sub(last) >= d.idleAfter {
			out = append(out, id)
		}
	}
	return out
}

// TranscriptSource is one agent conversation store (plan §1.3 sources table).
type TranscriptSource struct {
	Agent  string
	Dirs   []string // resolved absolute directories (may not exist)
	Format string   // "jsonl" | "sqlite"
}

// TranscriptSources resolves the plan §1.3 sources table. Home-scoped dirs
// mirror the agentDirs of adapters/registry.go (claude, opencode, cursor);
// APPDATA roots mirror its absRoots (copilot/cursor VS Code workspaceStorage,
// antigravity workspaceStorage). appData == "" falls back to
// $APPDATA (Windows) or ~/.config.
func TranscriptSources(home, appData string) []TranscriptSource {
	if appData == "" {
		if env := os.Getenv("APPDATA"); env != "" {
			appData = env
		} else {
			appData = filepath.Join(home, ".config")
		}
	}
	vscodeWS := filepath.Join(appData, "Code", "User", "workspaceStorage")
	return []TranscriptSource{
		{Agent: "claude", Dirs: []string{
			filepath.Join(home, ".claude", "projects"),
		}, Format: "jsonl"},
		{Agent: "opencode", Dirs: []string{
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(home, ".local", "share", "opencode"),
		}, Format: "jsonl"},
		{Agent: "cursor", Dirs: []string{
			filepath.Join(home, ".cursor"),
			vscodeWS,
		}, Format: "sqlite"},
		{Agent: "copilot", Dirs: []string{
			vscodeWS,
		}, Format: "sqlite"},
		{Agent: "antigravity", Dirs: []string{
			filepath.Join(appData, "Antigravity", "User", "workspaceStorage"),
		}, Format: "sqlite"},
	}
}

// ConversationFiles walks a source's dirs and returns conversation files:
// .jsonl/.json for jsonl sources, .db/.vscdb/.sqlite for sqlite sources.
// adapters.ClassifyPath filters out NEVER/IGNORE files (credentials, caches,
// logs) — the same fail-closed rules the backup adapters use.
func ConversationFiles(src TranscriptSource) ([]string, error) {
	var out []string
	for _, dir := range src.Dirs {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			switch src.Format {
			case "jsonl":
				if ext != ".jsonl" && ext != ".json" {
					return nil
				}
			case "sqlite":
				if ext != ".db" && ext != ".vscdb" && ext != ".sqlite" {
					return nil
				}
			default:
				return nil
			}
			if adapters.ClassifyPath(p) != adapters.Backup {
				return nil
			}
			out = append(out, p)
			return nil
		})
	}
	return out, nil
}

// WorkspaceMatchesFile reports whether a transcript belongs to the workspace
// at workspaceRoot. Signals, strongest first:
//  1. the transcript lives inside the workspace (project dot-dirs);
//  2. adapters.ProjectOf(transcript) equals project.ForPath(workspaceRoot)
//     (a conflicting concrete leaf is a hard no);
//  3. the transcript's embedded cwd / sibling workspace.json folder points
//     at the workspace;
//  4. last-resort heuristic: the transcript path contains the workspace base
//     name (covers Claude's encoded project dirs, e.g. D--server-nexus).
func WorkspaceMatchesFile(workspaceRoot, transcriptPath, home string) bool {
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		absRoot = workspaceRoot
	}
	absRoot = filepath.Clean(absRoot)
	absT, err := filepath.Abs(transcriptPath)
	if err != nil {
		absT = transcriptPath
	}
	absT = filepath.Clean(absT)

	fold := func(s string) string { return strings.ToLower(s) }
	if fold(absT) == fold(absRoot) ||
		strings.HasPrefix(fold(absT), fold(absRoot)+string(filepath.Separator)) {
		return true
	}

	leafW := project.ForPath(absRoot)
	leafT := adapters.ProjectOf(absT, home)
	if leafT != "" && leafT != "global" {
		if leafW == "" {
			// No leaf resolution available (e.g. non-D:\ dev box): fall
			// through to content signals rather than guessing.
		} else if leafT == leafW {
			return true
		} else {
			return false // strong signal: a different project
		}
	}

	if transcriptMentionsWorkspace(absT, absRoot) {
		return true
	}

	base := filepath.Base(absRoot)
	if base != "" && base != string(filepath.Separator) && base != "." {
		if strings.Contains(fold(absT), fold(base)) {
			return true
		}
	}
	return false
}

// transcriptMentionsWorkspace scans a transcript's head (JSONL cwd) and its
// sibling workspace.json (VS Code-family folder URI) for workspaceRoot.
func transcriptMentionsWorkspace(transcriptPath, workspaceRoot string) bool {
	lower := func(s string) string { return strings.ToLower(s) }
	slashRoot := lower(filepath.ToSlash(workspaceRoot))

	if f, err := os.Open(transcriptPath); err == nil {
		head := make([]byte, 64*1024)
		if n, _ := f.Read(head); n > 0 {
			// Transcripts are JSON: embedded paths appear escaped
			// ("cwd":"D:\\proj"), so collapse \\ runs before comparing.
			h := lower(strings.ReplaceAll(string(head[:n]), `\\`, `\`))
			if strings.Contains(h, lower(workspaceRoot)) ||
				strings.Contains(h, slashRoot) {
				f.Close()
				return true
			}
		}
		f.Close()
	}

	dir := filepath.Dir(transcriptPath)
	for i := 0; i < 2; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
		if err == nil {
			var w struct {
				Folder string `json:"folder"`
			}
			if json.Unmarshal(data, &w) == nil && w.Folder != "" {
				folder := w.Folder
				if u, err := url.Parse(folder); err == nil && u.Path != "" {
					if decoded, derr := url.PathUnescape(u.Path); derr == nil {
						folder = decoded
					} else {
						folder = u.Path
					}
				}
				folder = strings.ReplaceAll(folder, "/", `\`)
				folder = strings.TrimLeft(folder, `\`)
				if lower(folder) == lower(workspaceRoot) ||
					strings.HasPrefix(lower(folder), lower(workspaceRoot)) {
					return true
				}
			}
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false
}

// TrackedFile is a workspace-matching conversation file under harvest.
type TrackedFile struct {
	Path      string
	Agent     string
	Format    string // "jsonl" | "sqlite"
	SessionID string
	Project   string
}

// SessionIDFor derives a stable session id from agent + transcript path.
func SessionIDFor(agent, path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return agent + "::" + abs
}

// Harvester tails workspace-matching conversation files and emits turn /
// session-complete events to sink. Construct with NewHarvester; drive one
// shot with Discover + processFile + CheckIdle, or continuously with Start.
type Harvester struct {
	workspaceRoot string
	home          string
	appData       string
	sink          EventSink
	clock         Clock
	idleAfter     time.Duration
	sqlitePoll    time.Duration

	mu         sync.Mutex
	offsets    *OffsetTracker
	idle       *IdleDetector
	files      map[string]TrackedFile // by transcript path
	sessFile   map[string]TrackedFile // by session id
	counts     map[string]int         // turns emitted per session
	completed  map[string]bool        // session-complete already emitted
	sqliteSnap map[string][2]int64    // path -> {size, mtimeNano}
}

// Option customizes a Harvester.
type Option func(*Harvester)

// WithHome overrides the user home used for source resolution (tests).
func WithHome(home string) Option { return func(h *Harvester) { h.home = home } }

// WithAppData overrides the roaming-app-data root (tests).
func WithAppData(dir string) Option { return func(h *Harvester) { h.appData = dir } }

// WithClock injects the time source (tests).
func WithClock(c Clock) Option {
	return func(h *Harvester) {
		h.clock = c
		h.idle = NewIdleDetector(h.idleAfter, c)
	}
}

// WithIdleAfter overrides the 5-minute end-of-session threshold (tests).
func WithIdleAfter(d time.Duration) Option {
	return func(h *Harvester) {
		h.idleAfter = d
		h.idle = NewIdleDetector(d, h.clock)
	}
}

// WithSQLitePollInterval overrides the 30s SQLite poll (tests).
func WithSQLitePollInterval(d time.Duration) Option {
	return func(h *Harvester) { h.sqlitePoll = d }
}

// NewHarvester builds a harvester for workspaceRoot emitting to sink
// (nil sink = track only, useful for dry runs).
func NewHarvester(workspaceRoot string, sink EventSink, opts ...Option) *Harvester {
	h := &Harvester{
		workspaceRoot: workspaceRoot,
		sink:          sink,
		clock:         time.Now,
		idleAfter:     IdleAfter,
		sqlitePoll:    SQLitePollInterval,
		offsets:       NewOffsetTracker(),
		files:         map[string]TrackedFile{},
		sessFile:      map[string]TrackedFile{},
		counts:        map[string]int{},
		completed:     map[string]bool{},
		sqliteSnap:    map[string][2]int64{},
	}
	if home, err := os.UserHomeDir(); err == nil {
		h.home = home
	}
	for _, o := range opts {
		o(h)
	}
	h.idle = NewIdleDetector(h.idleAfter, h.clock)
	return h
}

// Sources returns the resolved transcript sources for this harvester.
func (h *Harvester) Sources() []TranscriptSource {
	return TranscriptSources(h.home, h.appData)
}

// Discover scans all sources for workspace-matching conversation files,
// registers them, and seeds offsets at current EOF so startup tails only new
// content (no backfill flood; historical import is Phase 1b's importer,
// issue #25). SQLite files also get an mtime/size snapshot for the poller.
func (h *Harvester) Discover() ([]TrackedFile, error) {
	var out []TrackedFile
	for _, src := range h.Sources() {
		files, err := ConversationFiles(src)
		if err != nil {
			continue
		}
		for _, p := range files {
			if !WorkspaceMatchesFile(h.workspaceRoot, p, h.home) {
				continue
			}
			tf := TrackedFile{
				Path:      p,
				Agent:     src.Agent,
				Format:    src.Format,
				SessionID: SessionIDFor(src.Agent, p),
				Project:   adapters.ProjectOf(p, h.home),
			}
			h.mu.Lock()
			h.files[p] = tf
			h.sessFile[tf.SessionID] = tf
			h.mu.Unlock()
			if st, err := os.Stat(p); err == nil {
				h.offsets.Set(p, st.Size())
				if src.Format == "sqlite" {
					h.mu.Lock()
					h.sqliteSnap[p] = [2]int64{st.Size(), st.ModTime().UnixNano()}
					h.mu.Unlock()
				}
			}
			out = append(out, tf)
		}
	}
	return out, nil
}

// classify attributes an unseen path to a source by directory containment
// (extension decides the effective format). Unknown paths get Agent == "".
func (h *Harvester) classify(path string) TrackedFile {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	lower := strings.ToLower(abs)
	format := ""
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl", ".json":
		format = "jsonl"
	case ".db", ".vscdb", ".sqlite":
		format = "sqlite"
	default:
		return TrackedFile{}
	}
	for _, src := range h.Sources() {
		wantFormat := src.Format
		// A .json transcript sitting in a sqlite dir (or vice versa) is
		// still harvestable — trust the extension.
		_ = wantFormat
		for _, dir := range src.Dirs {
			ad, err := filepath.Abs(dir)
			if err != nil {
				ad = dir
			}
			prefix := strings.ToLower(filepath.Clean(ad)) + string(filepath.Separator)
			if strings.HasPrefix(lower, prefix) || lower == strings.ToLower(filepath.Clean(ad)) {
				return TrackedFile{
					Path:      path,
					Agent:     src.Agent,
					Format:    format,
					SessionID: SessionIDFor(src.Agent, path),
					Project:   adapters.ProjectOf(path, h.home),
				}
			}
		}
	}
	return TrackedFile{}
}

// processFile tails one transcript and emits a CONVERSATION_TURN per new
// meaningful turn. Unknown files are ignored; sqlite files only refresh
// liveness (row parsing needs a SQL driver — follow-up, see ADR).
func (h *Harvester) processFile(path string) error {
	h.mu.Lock()
	tf, ok := h.files[path]
	h.mu.Unlock()
	if !ok {
		tf = h.classify(path)
		if tf.Agent == "" {
			return nil
		}
		if !WorkspaceMatchesFile(h.workspaceRoot, path, h.home) {
			return nil
		}
		h.mu.Lock()
		h.files[path] = tf
		h.sessFile[tf.SessionID] = tf
		h.mu.Unlock()
	}
	if tf.Format == "sqlite" {
		h.touch(tf.SessionID)
		return nil
	}
	turns, newOff, err := TailTurns(path, h.offsets.Get(path))
	if err != nil {
		return err
	}
	h.offsets.Set(path, newOff)
	if len(turns) == 0 {
		return nil
	}
	h.touch(tf.SessionID)
	h.mu.Lock()
	h.counts[tf.SessionID] += len(turns)
	h.mu.Unlock()
	for _, t := range turns {
		t.SessionID = tf.SessionID
		t.Agent = tf.Agent
		h.emit(EventConversationTurn, map[string]any{
			"session_id":      tf.SessionID,
			"agent":           tf.Agent,
			"speaker":         t.Speaker,
			"content":         truncate(t.Content, MaxTurnContent),
			"timestamp":       rfc3339OrEmpty(t.Timestamp),
			"transcript_path": path,
			"project":         tf.Project,
		})
	}
	return nil
}

// touch records session activity and re-arms completion if the session
// resumes after having gone idle.
func (h *Harvester) touch(sessionID string) {
	h.idle.Touch(sessionID)
	h.mu.Lock()
	delete(h.completed, sessionID)
	h.mu.Unlock()
}

// CheckIdle emits SESSION_TRANSCRIPT_COMPLETE once per idle session and
// returns the number emitted. Sessions that resume later re-arm via touch.
func (h *Harvester) CheckIdle() int {
	emitted := 0
	for _, sessionID := range h.idle.IdleSessions() {
		h.mu.Lock()
		if h.completed[sessionID] {
			h.mu.Unlock()
			continue
		}
		tf := h.sessFile[sessionID]
		count := h.counts[sessionID]
		h.completed[sessionID] = true
		h.mu.Unlock()

		last, _ := h.idle.Last(sessionID)
		var idleFor time.Duration
		if !last.IsZero() {
			idleFor = h.clock().Sub(last)
		}
		h.emit(EventSessionTranscriptComplete, map[string]any{
			"session_id":       sessionID,
			"agent":            tf.Agent,
			"transcript_path":  tf.Path,
			"project":          tf.Project,
			"turn_count":       count,
			"idle_for_seconds": int64(idleFor / time.Second),
			"ended_at":         rfc3339OrEmpty(h.clock()),
		})
		emitted++
	}
	return emitted
}

// pollSQLite stats tracked sqlite files (plus -wal/-shm siblings, which is
// where the real churn shows) and marks changed sessions active. fsnotify
// misses these writes, hence the 30s poll (plan §1.3 behavior 6).
func (h *Harvester) pollSQLite() {
	h.mu.Lock()
	paths := make([]string, 0, len(h.files))
	for p, tf := range h.files {
		if tf.Format == "sqlite" {
			paths = append(paths, p)
		}
	}
	h.mu.Unlock()
	for _, p := range paths {
		sig := sqliteSig(p)
		h.mu.Lock()
		old, seen := h.sqliteSnap[p]
		h.mu.Unlock()
		if !seen || sig != old {
			h.mu.Lock()
			h.sqliteSnap[p] = sig
			tf := h.files[p]
			h.mu.Unlock()
			h.touch(tf.SessionID)
		}
	}
}

// sqliteSig combines size+mtime of a sqlite file and its WAL/SHM siblings.
func sqliteSig(path string) [2]int64 {
	var size int64
	var mtime int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if st, err := os.Stat(p); err == nil {
			size += st.Size()
			if m := st.ModTime().UnixNano(); m > mtime {
				mtime = m
			}
		}
	}
	return [2]int64{size, mtime}
}

// Start runs the live loop until ctx is done: fsnotify on source dirs for
// JSONL, 30s SQLite polling, 1-minute idle sweeps. Blocks; returns nil on
// clean context cancellation.
func (h *Harvester) Start(ctx context.Context) error {
	if _, err := h.Discover(); err != nil {
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("harvester: fsnotify: %w", err)
	}
	defer watcher.Close()
	addDir := func(dir string) {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			_ = watcher.Add(dir) // best effort; a missing dir is not fatal
		}
	}
	seen := map[string]bool{}
	for _, src := range h.Sources() {
		for _, dir := range src.Dirs {
			ad, err := filepath.Abs(dir)
			if err != nil {
				ad = dir
			}
			if !seen[ad] {
				seen[ad] = true
				addDir(ad)
			}
		}
	}
	h.mu.Lock()
	for p := range h.files {
		if d := filepath.Dir(p); !seen[d] {
			seen[d] = true
			addDir(d)
		}
	}
	h.mu.Unlock()

	pollTicker := time.NewTicker(h.sqlitePoll)
	defer pollTicker.Stop()
	idleTicker := time.NewTicker(IdleSweepInterval)
	defer idleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			_ = h.processFile(ev.Name)
		case <-pollTicker.C:
			h.pollSQLite()
		case <-idleTicker.C:
			h.CheckIdle()
		case <-watcher.Errors:
			// Read-only observer: never let a watch error kill the loop.
			continue
		}
	}
}

// emit guards a nil sink (track-only mode).
func (h *Harvester) emit(eventType string, payload map[string]any) {
	if h.sink != nil {
		h.sink(eventType, payload)
	}
}

// truncate caps content at max runes with a marker.
func truncate(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…[truncated]"
}

// rfc3339OrEmpty formats t, or "" when zero.
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
