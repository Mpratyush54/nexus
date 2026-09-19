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
	// CwdMatch: path will not contain the workspace folder (date-sharded
	// or hashed global stores). When true, scanDir peeks the file for a
	// cwd/workdir hint and matches that against the workspace.
	CwdMatch bool
	// Global: always include files under Dirs (machine-wide stores with no
	// per-project path segment, e.g. Cursor ai-tracking.db).
	Global bool
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

	// Telemetry for the portal Connect page (/local/harvest).
	lastScanAt    time.Time
	lastScanFiles int
	lastScanTurns int
	lastScanErr   string
	agentFiles    map[string]int // agent -> files touched in last scan
	// recentFiles: matched transcript paths from the latest scan (portal list).
	recentFiles []HarvestFileHit
}

// HarvestFileHit is one jsonl/sqlite path matched to this workspace.
type HarvestFileHit struct {
	Agent  string `json:"agent"`
	Format string `json:"format"`
	Path   string `json:"path"`
	Name   string `json:"name"`
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
		agentFiles:   make(map[string]int),
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

// ResolveSources returns transcript roots for every local agent harness we
// know how to read. Names are filtered against adapters.Registry() so the
// harvester stays in sync with the adapter set; when the registry is empty
// the full list is returned so a trimmed build still harvests.
//
// Coverage (JSONL unless noted):
//
//	claude, cursor (JSONL + SQLite liveness), opencode (JSONL + SQLite),
//	codex, antigravity (SQLite), copilot (CLI JSONL + VS Code SQLite),
//	windsurf (SQLite), gemini, grok, kimi, codeium, commandcode, cagent,
//	zcode (JSONL + SQLite), deepseek, hermes (SQLite).
func ResolveSources() []TranscriptSource {
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(home, ".config")
	}
	localApp := os.Getenv("LOCALAPPDATA")
	if localApp == "" {
		localApp = filepath.Join(home, ".local", "share")
	}
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(home, ".local", "share")
	}
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}
	codeWS := filepath.Join(appData, "Code", "User", "workspaceStorage")
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}

	hardcoded := []TranscriptSource{
		// Claude Code — per-project JSONL (~/.claude/projects/<encoded-cwd>/)
		{Agent: "claude", Dirs: []string{filepath.Join(home, ".claude", "projects")}, Format: FormatJSONL},
		// OpenCode — legacy JSON/JSONL dirs + modern opencode.db (SQLite).
		// CwdMatch: session paths are global; project is inside the file/DB.
		{Agent: "opencode", Dirs: []string{
			filepath.Join(xdgConfig, "opencode"),
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(xdgData, "opencode"),
			filepath.Join(home, ".local", "share", "opencode"),
			filepath.Join(home, ".opencode"),
		}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "opencode", Dirs: []string{
			filepath.Join(xdgData, "opencode"),
			filepath.Join(home, ".local", "share", "opencode"),
		}, Format: FormatSQLite, Global: true},
		// Cursor Agent chats — append-only JSONL under projects/<slug>/agent-transcripts/
		{Agent: "cursor", Dirs: []string{
			filepath.Join(home, ".cursor", "projects"),
		}, Format: FormatJSONL},
		// Cursor / VS Code workspace DBs — liveness (ADR-033)
		{Agent: "cursor", Dirs: []string{
			filepath.Join(appData, "Cursor", "User", "workspaceStorage"),
			filepath.Join(home, ".cursor", "chats"),
			codeWS,
		}, Format: FormatSQLite},
		// Cursor AI code-tracking DB — machine-global, no project slug in path
		{Agent: "cursor", Dirs: []string{
			filepath.Join(home, ".cursor", "ai-tracking"),
		}, Format: FormatSQLite, Global: true},
		// Codex CLI / Desktop — date-sharded rollouts (CODEX_HOME override)
		{Agent: "codex", Dirs: []string{
			filepath.Join(codexHome, "sessions"),
			filepath.Join(codexHome, "archived_sessions"),
		}, Format: FormatJSONL, CwdMatch: true},
		// Antigravity IDE — VS Code–style workspaceStorage (workspace.json match)
		{Agent: "antigravity", Dirs: []string{
			filepath.Join(appData, "Antigravity", "User", "workspaceStorage"),
			filepath.Join(home, ".antigravity"),
		}, Format: FormatSQLite, CwdMatch: true},
		// Antigravity agent brains — real multi-day chats live here as JSONL
		// (~/.gemini/antigravity/brain/<id>/.../transcript*.jsonl), not in
		// workspaceStorage. CwdMatch peeks file bytes for the folder name.
		{Agent: "antigravity", Dirs: []string{
			filepath.Join(home, ".gemini", "antigravity", "brain"),
		}, Format: FormatJSONL, CwdMatch: true},
		// Copilot — VS Code workspace DB + Copilot CLI session-state JSONL
		{Agent: "copilot", Dirs: []string{codeWS}, Format: FormatSQLite},
		{Agent: "copilot", Dirs: []string{
			filepath.Join(home, ".copilot", "session-state"),
			filepath.Join(home, ".copilot"),
		}, Format: FormatJSONL, CwdMatch: true},
		// Windsurf — VS Code fork workspace storage
		{Agent: "windsurf", Dirs: []string{
			filepath.Join(appData, "Windsurf", "User", "workspaceStorage"),
			filepath.Join(localApp, "Windsurf", "User", "workspaceStorage"),
			filepath.Join(home, ".windsurf"),
		}, Format: FormatSQLite},
		// Gemini CLI — ~/.gemini/tmp/<project-hash>/chats/*.jsonl
		{Agent: "gemini", Dirs: []string{
			filepath.Join(home, ".gemini"),
		}, Format: FormatJSONL, CwdMatch: true},
		// Grok / Kimi / Codeium / CommandCode / Cagent — home agent dirs
		{Agent: "grok", Dirs: []string{filepath.Join(home, ".grok")}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "kimi", Dirs: []string{filepath.Join(home, ".kimi-code"), filepath.Join(home, ".kimi")}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "codeium", Dirs: []string{filepath.Join(home, ".codeium")}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "commandcode", Dirs: []string{filepath.Join(home, ".commandcode")}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "cagent", Dirs: []string{filepath.Join(home, ".cagent")}, Format: FormatJSONL, CwdMatch: true},
		// Z CLI (zcode) — agent transcript JSONL + authoritative SQLite
		{Agent: "zcode", Dirs: []string{
			filepath.Join(home, ".zcode", "cli", "agents"),
			filepath.Join(home, ".zcode", "cli", "rollout"),
			filepath.Join(home, ".zcode", "v2", "sessions"),
			filepath.Join(home, ".zcode"),
		}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "zcode", Dirs: []string{
			filepath.Join(home, ".zcode", "cli", "db"),
		}, Format: FormatSQLite, CwdMatch: true},
		// DeepSeek CLI — XDG data + legacy ~/.deepseek-cli
		{Agent: "deepseek", Dirs: []string{
			filepath.Join(xdgData, "deepseek-cli"),
			filepath.Join(home, ".local", "share", "deepseek-cli"),
			filepath.Join(home, ".deepseek-cli"),
			filepath.Join(home, ".deepseek"),
		}, Format: FormatJSONL, CwdMatch: true},
		{Agent: "deepseek", Dirs: []string{
			filepath.Join(xdgData, "deepseek-cli"),
			filepath.Join(home, ".local", "share", "deepseek-cli"),
			filepath.Join(home, ".deepseek-cli"),
		}, Format: FormatJSON, CwdMatch: true},
		// Hermes — SQLite state (+ legacy JSON sessions)
		{Agent: "hermes", Dirs: []string{
			filepath.Join(home, ".hermes"),
		}, Format: FormatSQLite, CwdMatch: true},
		{Agent: "hermes", Dirs: []string{
			filepath.Join(home, ".hermes", "sessions"),
		}, Format: FormatJSONL, CwdMatch: true},
	}

	known := map[string]bool{}
	for _, a := range adapters.Registry() {
		known[a.Name()] = true
	}
	if len(known) == 0 {
		return hardcoded
	}
	var out []TranscriptSource
	for _, s := range hardcoded {
		if known[s.Agent] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
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
	// Cursor uses slugs like "d-central-memory" for folder "central-memory".
	want := strings.ToLower(h.folderName)
	for _, seg := range strings.FieldsFunc(low, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == want || strings.HasSuffix(seg, "-"+want) || strings.Contains(seg, "-"+want+"-") {
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

// MatchesTranscript reports whether path belongs to this workspace. Path
// segment matching comes first; then sibling workspace.json (Antigravity /
// Cursor/VS Code hash dirs); then for CwdMatch sources we peek the file
// header for a cwd that names this folder.
func (h *Harvester) MatchesTranscript(path string, cwdMatch bool) bool {
	if h.MatchesWorkspace(path) {
		return true
	}
	if h.matchesWorkspaceJSON(path) {
		return true
	}
	if !cwdMatch {
		return false
	}
	hint := PeekTranscriptCwd(path)
	if hint != "" {
		if h.MatchesWorkspace(hint) {
			return true
		}
		ws := strings.TrimSpace(h.workspace)
		if ws != "" && strings.EqualFold(filepath.Clean(hint), filepath.Clean(ws)) {
			return true
		}
	}
	// Antigravity / nested tool JSON often buries Cwd inside tool_calls; a
	// raw byte peek for the folder name recovers those transcripts.
	if h.folderName != "" && len(h.folderName) >= 4 && fileMentionsFolder(path, h.folderName) {
		return true
	}
	return false
}

// matchesWorkspaceJSON attributes VS Code–style workspaceStorage/<hash>/…
// paths via a nearby workspace.json (adapters.ProjectOf). Does not peek
// transcript cwd — that remains gated by CwdMatch.
func (h *Harvester) matchesWorkspaceJSON(path string) bool {
	dir := filepath.Dir(path)
	found := false
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "workspace.json")); err == nil {
			found = true
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if !found {
		return false
	}
	home, _ := os.UserHomeDir()
	leaf := adapters.ProjectOf(path, home)
	if leaf == "" || leaf == "global" {
		return false
	}
	if h.MatchesWorkspace(leaf) || strings.EqualFold(leaf, h.folderName) {
		return true
	}
	ws := strings.TrimSpace(h.workspace)
	if ws == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(ws), leaf) ||
		strings.Contains(strings.ToLower(ws), strings.ToLower(leaf))
}

// defaultKeepTail is how much of an unseen transcript we ingest on first
// sight. 8 MiB covers multi-hour Cursor agent JSONL without replaying
// entire historical archives on every daemon restart.
const defaultKeepTail = 8 << 20

// firstSightOffset picks the byte offset for a newly discovered transcript.
// NEXUS_HARVEST_BACKFILL=all|1|true → 0 (full file). NEXUS_HARVEST_KEEP_TAIL
// (bytes) overrides the default window size.
func firstSightOffset(size int64) int64 {
	if size <= 0 {
		return 0
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("NEXUS_HARVEST_BACKFILL")))
	if v == "1" || v == "true" || v == "all" || v == "full" {
		return 0
	}
	keep := int64(defaultKeepTail)
	if raw := strings.TrimSpace(os.Getenv("NEXUS_HARVEST_KEEP_TAIL")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			keep = n
		}
	}
	if size > keep {
		return size - keep
	}
	return 0
}

// fileMentionsFolder reports whether the first ~512KiB of path contains the
// workspace folder name (case-insensitive). Used when structured cwd peeks
// miss nested Antigravity tool_call Cwd fields.
func fileMentionsFolder(path, folder string) bool {
	folder = strings.TrimSpace(folder)
	if folder == "" || len(folder) < 4 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512<<10)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(buf[:n])), strings.ToLower(folder))
}

// PeekTranscriptCwd reads the first ~64KiB of a transcript and returns a
// workspace hint (cwd / working_directory / directory / workspace_path).
// Used for global session stores whose on-disk path has no project folder.
func PeekTranscriptCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(buf[:n])))
	sc.Buffer(make([]byte, 4<<10), maxJSONLLineBytes)
	lines := 0
	for sc.Scan() {
		lines++
		if lines > 40 {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			// Non-JSONL (e.g. pretty JSON): fall through to whole-buffer scan below.
			break
		}
		if cwd := extractCwdHint(m); cwd != "" {
			return cwd
		}
	}
	// Whole-buffer fallback for single-object JSON / torn first lines.
	var m map[string]any
	if err := json.Unmarshal(buf[:n], &m); err == nil {
		if cwd := extractCwdHint(m); cwd != "" {
			return cwd
		}
	}
	return ""
}

func extractCwdHint(m map[string]any) string {
	for _, k := range []string{
		"cwd", "working_directory", "workingDirectory", "workdir",
		"directory", "workspace", "workspace_path", "workspacePath",
		"project_path", "projectPath", "repo_path", "repoPath",
	} {
		if s := strings.TrimSpace(strField(m, k)); s != "" {
			return s
		}
	}
	if p, ok := m["payload"].(map[string]any); ok {
		if s := extractCwdHint(p); s != "" {
			return s
		}
	}
	if msg, ok := m["message"].(map[string]any); ok {
		if s := extractCwdHint(msg); s != "" {
			return s
		}
	}
	return ""
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
	// Codex rollout envelope types (dialogue lives under payload).
	"session_meta": true, "turn_context": true,
	"token_count": true, "task_complete": true,
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
// Tolerates Claude/Cursor nested message.content, OpenCode flats, and Codex
// rollout envelopes ({type, payload:{role|message|content}}).
func parseTurnLine(line []byte) (t Turn, ok bool) {
	if len(strings.TrimSpace(string(line))) == 0 {
		return Turn{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return Turn{}, false
	}
	typ := strings.ToLower(strField(m, "type"))
	if noiseTypes[typ] {
		return Turn{}, false
	}
	// Codex / OpenAI Responses-style: unwrap payload for dialogue fields.
	if p, ok := m["payload"].(map[string]any); ok {
		pt := strings.ToLower(strField(p, "type"))
		switch typ {
		case "response_item":
			if pt != "" && pt != "message" && pt != "output_text" && pt != "input_text" {
				if noiseTypes[pt] || pt == "function_call" || pt == "function_call_output" ||
					pt == "reasoning" || pt == "custom_tool_call" {
					return Turn{}, false
				}
			}
			m = p
		case "event_msg":
			switch pt {
			case "user_message", "agent_message", "assistant_message", "message":
				m = p
			default:
				return Turn{}, false
			}
		default:
			// Generic nested payload with role/content — prefer it.
			if strField(p, "role") != "" || p["content"] != nil || strField(p, "message") != "" {
				m = p
			}
		}
	}
	role := firstNonEmpty(
		strField(m, "role"),
		strField(m, "speaker"),
		strField(m, "author"),
		nestedStr(m, "message", "role"),
	)
	// Codex event_msg: type user_message / agent_message without role.
	if role == "" {
		switch strings.ToLower(strField(m, "type")) {
		case "user_message", "user", "user_input", "user_request":
			role = "user"
		case "agent_message", "assistant_message", "assistant", "planner_response", "model_response":
			role = "assistant"
		}
	}
	// Antigravity brain transcripts: source USER_EXPLICIT / MODEL.
	if role == "" {
		switch strings.ToUpper(strField(m, "source")) {
		case "USER_EXPLICIT", "USER":
			role = "user"
		case "MODEL", "AGENT", "PLANNER":
			role = "assistant"
		}
	}
	if r := strings.ToLower(role); r == "tool" || r == "function" {
		return Turn{}, false
	}
	content := extractContent(m)
	if content == "" {
		// Codex event_msg often uses "message" as a plain string.
		if s, _ := m["message"].(string); strings.TrimSpace(s) != "" {
			content = s
		}
	}
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
// Content is secret-screened (issue #132): transcripts routinely contain
// pasted secrets, and the event stream lands in Postgres.
func turnPayload(action, agent, path string, t Turn) map[string]any {
	p := map[string]any{
		"action":     action,
		"agent":      agent,
		"path":       path,
		"session_id": sessionID(path),
		"speaker":    t.Speaker,
		"content":    redact(t.Content),
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
	off, seen := h.offsets[path]
	if !seen && st.Size() > 0 {
		// First sighting: keep a large recent window so multi-hour chats
		// are not discarded. Override with NEXUS_HARVEST_KEEP_TAIL (bytes)
		// or NEXUS_HARVEST_BACKFILL=all|1 to read from offset 0.
		off = firstSightOffset(st.Size())
		h.offsets[path] = off
	}
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
	ex := h.extractors[agent]
	firstSight := !seen
	if !seen {
		if _, ok := h.lastActive[path]; !ok {
			h.lastActive[path] = time.Time{}
		}
		// Baseline for normal SQLite/vscdb. OpenCode's single global DB only
		// changes when the user chats — extract on first sight so multi-day
		// history is not stuck waiting for the next write.
		if !strings.EqualFold(filepath.Base(path), "opencode.db") {
			h.mu.Unlock()
			return false
		}
	}
	if !firstSight && cur == prev {
		h.mu.Unlock()
		return false
	}
	since := h.lastActive[path]
	h.mu.Unlock()

	runExtract := func(turns []Turn) bool {
		if len(turns) == 0 {
			return false
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

	// First sight or change: try registered extractor, then stdlib fallback.
	if ex != nil {
		turns, err := ex.ExtractNewRows(path, since)
		if err == nil && runExtract(turns) {
			return true
		}
	}
	if turns := extractSQLiteFallback(path, since); runExtract(turns) {
		return true
	}
	if firstSight {
		// Baseline only — no rows yet.
		h.mu.Lock()
		if h.lastActive[path].IsZero() {
			h.lastActive[path] = now
		}
		h.mu.Unlock()
		return false
	}
	log.Printf("harvester: sqlite source %q (%s) has no rows parsed — liveness-only", agent, path)
	h.mu.Lock()
	h.lastActive[path] = now
	delete(h.completed, path)
	h.mu.Unlock()
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
		// Drain the batch (issue #132): without this the session's turns
		// accumulate forever and every re-armed idle re-emits all old
		// turns to the Memory Processor (duplicate extraction + LLM cost).
		delete(h.batch, path)
		detail := strconv.Itoa(len(turns)) + " turns harvested"
		if _, isSQLite := h.sqlite[path]; isSQLite && len(turns) == 0 {
			detail = "sqlite/vscdb source: rows not parsed (no driver); " +
				"deep extraction must read the store out-of-band"
		}
		flat := make([]any, 0, len(turns))
		for _, t := range turns {
			m := map[string]any{"speaker": t.Speaker, "content": redact(t.Content)}
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
	maxWalkDepth = 8
	// maxWalkFileBytes skips sqlite/jsonl files larger than 64MB.
	maxWalkFileBytes = 64 << 20
)

// harvesterSkipDirs are never descended into during transcript walks.
var harvesterSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	"out": true, ".next": true, "__pycache__": true, ".venv": true,
	"venv": true, "target": true, "bin": true, "obj": true,
	"coverage": true, ".idea": true, ".vscode": true, "Library": true,
	// OpenCode snapshot trees drown the walk before opencode.db is seen.
	"snapshot": true, "tool-output": true, "log": true,
	// Antigravity brain noise — only transcript*.jsonl under logs matter.
	"messages": true, "steps": true, "tasks": true, "scratch": true,
	"chunks": true, "extensions": true,
}

// HarvesterStats is a point-in-time view for /local/harvest.
type HarvesterStats struct {
	LastScanAt     string
	LastScanFiles  int
	LastScanTurns  int
	LastScanError  string
	TrackedFiles   int
	ActiveSessions int
	AgentFiles     map[string]int
	RecentFiles    []HarvestFileHit
}

// Stats returns portal telemetry (safe under the harvester lock).
func (h *Harvester) Stats() HarvesterStats {
	if h == nil {
		return HarvesterStats{AgentFiles: map[string]int{}}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := HarvesterStats{
		LastScanFiles:  h.lastScanFiles,
		LastScanTurns:  h.lastScanTurns,
		LastScanError:  h.lastScanErr,
		TrackedFiles:   len(h.offsets) + len(h.sqlite),
		ActiveSessions: len(h.lastActive) - len(h.completed),
		AgentFiles:     make(map[string]int, len(h.agentFiles)),
		RecentFiles:    append([]HarvestFileHit(nil), h.recentFiles...),
	}
	if out.ActiveSessions < 0 {
		out.ActiveSessions = 0
	}
	if !h.lastScanAt.IsZero() {
		out.LastScanAt = h.lastScanAt.UTC().Format(time.RFC3339)
	}
	for k, v := range h.agentFiles {
		out.AgentFiles[k] = v
	}
	return out
}

// ScanAndTail walks all sources, tailing jsonl/json transcripts and tracking
// sqlite/vscdb files, restricted to this workspace. Files classified NEVER by
// adapters.ClassifyPath (credentials, secrets) are never touched. Walks are
// bounded (file count, depth, size, skip dirs) per issue #118.
func (h *Harvester) ScanAndTail() error {
	_, _, err := h.ScanAndTailCounted()
	return err
}

// ScanAndTailCounted is ScanAndTail with file/turn counts for the portal.
func (h *Harvester) ScanAndTailCounted() (files, turns int, err error) {
	agentFiles := map[string]int{}
	var hits []HarvestFileHit
	var firstErr error
	for _, src := range h.Sources {
		for _, dir := range src.Dirs {
			n, t, found, e := h.scanDirCounted(src, dir, agentFiles)
			files += n
			turns += t
			hits = append(hits, found...)
			if e != nil && firstErr == nil {
				firstErr = e
			}
		}
	}
	if len(hits) > 40 {
		hits = hits[:40]
	}
	h.mu.Lock()
	h.lastScanAt = h.now().UTC()
	h.lastScanFiles = files
	h.lastScanTurns = turns
	if firstErr != nil {
		h.lastScanErr = firstErr.Error()
	} else {
		h.lastScanErr = ""
	}
	h.agentFiles = agentFiles
	h.recentFiles = hits
	h.mu.Unlock()
	return files, turns, firstErr
}

func (h *Harvester) scanDir(src TranscriptSource, dir string) error {
	_, _, _, err := h.scanDirCounted(src, dir, nil)
	return err
}

func (h *Harvester) scanDirCounted(src TranscriptSource, dir string, agentFiles map[string]int) (files, turns int, hits []HarvestFileHit, err error) {
	if dir == "" {
		return 0, 0, nil, nil
	}
	// OpenCode: harvest the global DB directly — it is multi-GB and sits
	// beside a huge snapshot/ tree that would exhaust maxWalkFiles.
	if src.Agent == "opencode" && src.Format == FormatSQLite {
		db := filepath.Join(dir, "opencode.db")
		if st, serr := os.Stat(db); serr == nil && !st.IsDir() {
			h.mu.Lock()
			h.agents[db] = src.Agent
			h.mu.Unlock()
			_ = h.TrackSQLite(db, st)
			hit := HarvestFileHit{Agent: src.Agent, Format: src.Format, Path: db, Name: "opencode.db"}
			if agentFiles != nil {
				agentFiles[src.Agent]++
			}
			return 1, 0, []HarvestFileHit{hit}, nil
		}
		return 0, 0, nil, nil
	}
	visited := 0
	walkErr := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if visited > maxWalkFiles {
			return filepath.SkipDir
		}
		if info.IsDir() {
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
		// SQLite extractors query by SQL — allow large DBs (OpenCode ~GB).
		tooBig := info.Size() > maxWalkFileBytes
		if tooBig && !(src.Format == FormatSQLite && strings.EqualFold(filepath.Base(p), "opencode.db")) {
			return nil
		}
		if adapters.ClassifyPath(p) == adapters.Never {
			return nil
		}
		if !src.Global && !h.MatchesTranscript(p, src.CwdMatch) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		hit := HarvestFileHit{
			Agent:  src.Agent,
			Format: src.Format,
			Path:   p,
			Name:   filepath.Base(p),
		}
		switch src.Format {
		case FormatJSONL:
			if ext != ".jsonl" {
				return nil
			}
			base := filepath.Base(p)
			// Antigravity brains: only the canonical transcript logs.
			if src.Agent == "antigravity" {
				if base != "transcript_full.jsonl" && base != "transcript.jsonl" {
					return nil
				}
				if base == "transcript.jsonl" {
					full := filepath.Join(filepath.Dir(p), "transcript_full.jsonl")
					if _, err := os.Stat(full); err == nil {
						return nil // prefer the full transcript sibling
					}
				}
			}
			h.mu.Lock()
			h.agents[p] = src.Agent
			h.mu.Unlock()
			got, _ := h.TailFile(p)
			files++
			turns += len(got)
			hits = append(hits, hit)
			if agentFiles != nil {
				agentFiles[src.Agent]++
			}
		case FormatJSON:
			if ext != ".json" {
				return nil
			}
			h.mu.Lock()
			h.agents[p] = src.Agent
			h.mu.Unlock()
			got, _ := h.TailFile(p)
			files++
			turns += len(got)
			hits = append(hits, hit)
			if agentFiles != nil {
				agentFiles[src.Agent]++
			}
		case FormatSQLite:
			if ext != ".db" && ext != ".sqlite" && ext != ".sqlite3" && ext != ".vscdb" {
				return nil
			}
			// OpenCode: only the main DB (ignore snapshots / sidecars).
			if src.Agent == "opencode" && !strings.EqualFold(filepath.Base(p), "opencode.db") {
				return nil
			}
			h.mu.Lock()
			h.agents[p] = src.Agent
			h.mu.Unlock()
			_ = h.TrackSQLite(p, info)
			files++
			hits = append(hits, hit)
			if agentFiles != nil {
				agentFiles[src.Agent]++
			}
		}
		return nil
	})
	return files, turns, hits, walkErr
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
