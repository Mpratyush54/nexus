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
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/adapters"
	"central-memory/internal/platform"
	"central-memory/internal/usage"
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
	// per-project path segment, e.g. OpenCode's opencode.db). The reader
	// must still scope rows to the workspace.
	Global bool
}

// Turn is a single parsed dialogue turn.
type Turn struct {
	Speaker        string           // "user" | "assistant" (normalized where possible)
	Content        string           // text content (tool_use blocks not inlined into text)
	Timestamp      time.Time        // zero if the transcript line carried none
	ToolCalls      []map[string]any // structured tool_use / function_call blocks (JSON-compatible)
	SourcePosition int64            // stable byte position within an append-only transcript
	SessionID      string           // native session id for multi-session stores such as OpenCode
	SessionTitle   string           // native title, when available
	// Usage is the token usage reported on this line (Claude message.usage,
	// Codex token_count, OpenCode message tokens/cost, Gemini tokens). A
	// turn with Usage but no content is "usage only": it reaches the cloud
	// usage ledger but is not dialogue.
	Usage    *usage.Entry
	UsageKey string // dedup key: several lines can repeat one API call's usage
}

// UsageOnly reports a turn that only carries token usage.
func (t Turn) UsageOnly() bool {
	return t.Usage != nil && strings.TrimSpace(t.Content) == "" && len(t.ToolCalls) == 0
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
	pauseMu    sync.RWMutex
	paused     bool
	offsets    map[string]int64     // jsonl/json path -> last-read byte offset
	sqlite     map[string]fileMeta  // sqlite path -> last observed state
	lastActive map[string]time.Time // path -> last observed content time
	completed  map[string]bool      // path -> SESSION_TRANSCRIPT_COMPLETE sent
	agents     map[string]string    // path -> agent name
	batch      map[string][]Turn    // path -> turns since session start
	// batchDropped counts turns trimmed from batch (see addBatchLocked).
	batchDropped map[string]int
	models       map[string]string // path -> last model announced (Codex turn_context)
	// extractors holds per-agent SQLite row extractors (nil value or absent
	// agent = built-in fallback, then liveness-only). Issue #33/#77.
	extractors map[string]SQLiteExtractor
	// fallbackSeen dedupes stdlib SQLite fallback turns, which have no row
	// timestamps and would otherwise re-emit the same window on every mtime
	// change.
	fallbackSeen map[string]map[uint64]struct{}
	// lastFallback rate-limits the content-scanning SQLite fallback per file.
	// IDE workspace databases change every few seconds while the IDE is open,
	// and scanning them on every change made the whole machine sluggish.
	lastFallback map[string]time.Time
	// titles remembers the latest native chat title per transcript. Claude
	// appends its ai-title record after the turns it names, so the title
	// often lands in a read with no turns; it then names the next turns.
	titles map[string]nativeTitles
	// cursorSourceAt carries the latest prompt time per Cursor transcript so
	// replies read in a later poll inherit the prompt they answer.
	cursorSourceAt map[string]time.Time

	// routeFilter, when set, replaces the per-workspace attribution check:
	// the machine-wide SharedScanner decides once per transcript which
	// workspaces (if any) own it, and unowned files are never read.
	routeFilter func(path string, src TranscriptSource) bool
	// offsetsVersion increments whenever a read cursor moves, so the shared
	// scanner persists cursors only when they changed.
	offsetsVersion uint64
	// readThrottle, when set, is told how many bytes TailFile just read so
	// the shared scanner can pace history imports.
	readThrottle func(n int)

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
		fallbackSeen: make(map[string]map[uint64]struct{}),
		lastFallback: make(map[string]time.Time),
		titles:       make(map[string]nativeTitles),
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

// SetPaused suspends new filesystem scans without tearing down the
// harvester's durable cursor state.  A scan already in progress is allowed
// to finish; callers should also gate downstream processing.
func (h *Harvester) SetPaused(paused bool) {
	if h == nil {
		return
	}
	h.pauseMu.Lock()
	h.paused = paused
	h.pauseMu.Unlock()
}

func (h *Harvester) IsPaused() bool {
	if h == nil {
		return true
	}
	h.pauseMu.RLock()
	defer h.pauseMu.RUnlock()
	return h.paused
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
	// %APPDATA% on Windows, ~/Library/Application Support on macOS,
	// $XDG_CONFIG_HOME or ~/.config on Linux: where Cursor, VS Code,
	// Windsurf and Antigravity keep User/workspaceStorage.
	appData := platform.AppDataDir()
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
		if workspaceFolderInPath(candPath, folderName) {
			return MatchFolderName
		}
	} else {
		// No folder filter configured: match-all, as MatchesWorkspace does.
		return MatchFolderName
	}
	return MatchNone
}

// workspaceFolderInPath reports whether folder appears as a whole path
// segment (or a dash-delimited token inside an encoded segment). Plain
// substring matching is deliberately avoided so a generic workspace name
// like "api" cannot claim "capital".
func workspaceFolderInPath(path, folder string) bool {
	want := strings.ToLower(strings.TrimSpace(folder))
	if want == "" || want == "." || want == string(filepath.Separator) {
		return false
	}
	for _, seg := range strings.FieldsFunc(strings.ToLower(path), func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if seg == want || strings.HasSuffix(seg, "-"+want) || strings.Contains(seg, "-"+want+"-") {
			return true
		}
		for _, tok := range strings.Split(seg, "-") {
			if tok == want {
				return true
			}
		}
	}
	return false
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
	return workspaceFolderInPath(path, h.folderName)
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
	// Direct answer first: the folder workspace.json names is this workspace
	// (or lives inside it), or carries its folder name after a move.
	if folder := adapters.WorkspaceFolderOf(path); folder != "" {
		if ws := strings.TrimSpace(h.workspace); ws != "" && pathWithin(folder, ws) {
			return true
		}
		if h.folderName != "" && strings.EqualFold(lastPathSegment(folder), h.folderName) {
			return true
		}
	}
	if runtime.GOOS != "windows" {
		// adapters.ProjectOf resolves through project.CachedLeaves, which
		// walks every project root. Off Windows the default root is $HOME,
		// and on macOS walking it reaches ~/Documents, ~/Desktop,
		// ~/Downloads and ~/Library/Containers: each one is a privacy (TCC)
		// prompt, raised just because a Cursor database changed. The direct
		// check above is the whole answer here.
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
	return strings.EqualFold(filepath.Base(ws), leaf)
}

// pathWithin reports whether p is root or below it. Case-insensitive, as
// the default Windows and macOS file systems are.
func pathWithin(p, root string) bool {
	a := strings.ToLower(filepath.Clean(p))
	b := strings.ToLower(filepath.Clean(root))
	if a == b {
		return true
	}
	return strings.HasPrefix(a, strings.TrimSuffix(b, string(filepath.Separator))+string(filepath.Separator))
}

// lastPathSegment returns the final element of a path written with either
// separator (workspace.json folders may be Windows paths on any host).
func lastPathSegment(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
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
//
// Results are cached per (file, folder): only the head is read, so a match
// stays valid while the file only grows, and a miss stays valid once the
// file is longer than the peek window. A file that shrank (truncated or
// replaced) is read again.
func fileMentionsFolder(path, folder string) bool {
	folder = strings.TrimSpace(folder)
	if folder == "" || len(folder) < 4 {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	key := path + "\x00" + strings.ToLower(folder)
	headCache.Lock()
	e, ok := headCache.mentions[key]
	headCache.Unlock()
	if ok && st.Size() >= e.size && (e.found || e.size >= mentionPeekBytes || st.Size() == e.size) {
		if st.Size() > e.size {
			// Track the latest size so a later truncation is noticed.
			e.size = st.Size()
			headCache.Lock()
			headCache.mentions[key] = e
			headCache.Unlock()
		}
		return e.found
	}
	found := fileMentionsFolderUncached(path, folder)
	headCache.Lock()
	if len(headCache.mentions) >= headCacheMax {
		headCache.mentions = map[string]headCacheEntry{}
	}
	headCache.mentions[key] = headCacheEntry{size: st.Size(), found: found}
	headCache.Unlock()
	return found
}

const (
	mentionPeekBytes = 512 << 10
	cwdPeekBytes     = 64 << 10
	headCacheMax     = 8192
)

// headCache holds results derived from transcript heads (fileMentionsFolder,
// PeekTranscriptCwd). Attribution and discovery ask the same questions about
// the same files on every change notification and reconcile pass; without
// it each question re-read up to 512 KiB of the file.
var headCache = struct {
	sync.Mutex
	mentions map[string]headCacheEntry
	cwd      map[string]headCacheEntry
}{mentions: map[string]headCacheEntry{}, cwd: map[string]headCacheEntry{}}

type headCacheEntry struct {
	size  int64 // file size when the head was read
	found bool
	cwd   string
}

func fileMentionsFolderUncached(path, folder string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, mentionPeekBytes)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return false
	}
	text := strings.ToLower(string(buf[:n]))
	want := strings.ToLower(folder)
	for offset := 0; offset < len(text); {
		i := strings.Index(text[offset:], want)
		if i < 0 {
			return false
		}
		i += offset
		end := i + len(want)
		if (i == 0 || folderBoundary(text[i-1])) && (end == len(text) || folderBoundary(text[end])) {
			return true
		}
		offset = i + 1
	}
	return false
}

func folderBoundary(c byte) bool {
	switch c {
	case '/', '\\', '"', '\'', ' ', '\t', '\n', '\r', ':', '=', '(', '[', ',':
		return true
	}
	return false
}

// PeekTranscriptCwd reads the first ~64KiB of a transcript and returns a
// workspace hint (cwd / working_directory / directory / workspace_path).
// Used for global session stores whose on-disk path has no project folder.
//
// The hint comes from the head of the file, which appends do not change, so
// it is cached per path and read again only when the file shrank (truncated
// or replaced), or when no hint was found and the head was still growing.
func PeekTranscriptCwd(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	headCache.Lock()
	e, ok := headCache.cwd[path]
	headCache.Unlock()
	if ok && st.Size() >= e.size && (e.cwd != "" || e.size >= cwdPeekBytes || st.Size() == e.size) {
		if st.Size() > e.size {
			// Track the latest size so a later truncation is noticed.
			e.size = st.Size()
			headCache.Lock()
			headCache.cwd[path] = e
			headCache.Unlock()
		}
		return e.cwd
	}
	cwd := peekTranscriptCwdUncached(path)
	headCache.Lock()
	if len(headCache.cwd) >= headCacheMax {
		headCache.cwd = map[string]headCacheEntry{}
	}
	headCache.cwd[path] = headCacheEntry{size: st.Size(), cwd: cwd}
	headCache.Unlock()
	return cwd
}

func peekTranscriptCwdUncached(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, cwdPeekBytes)
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

// containerTranscriptStems are transcript file names (without extension)
// that never identify one conversation: either the file holds many
// conversations (VS Code/Cursor state.vscdb, Cursor ai-code-tracking.db,
// opencode.db) or the conversation id is a parent folder (Antigravity/Gemini
// brain transcript*.jsonl). Using the stem as a session id merged unrelated
// chats into one empty "state"/"opencode"/"transcript" chat in Timeline.
var containerTranscriptStems = map[string]bool{
	"state":              true,
	"opencode":           true,
	"ai-code-tracking":   true,
	"transcript":         true,
	"transcript_full":    true,
	"harvest-smoke-test": true, // capture self-test leftover
}

// folderIDTranscriptStems name per-conversation transcript files whose
// conversation id is an ancestor folder (…/brain/<id>/.system_generated/logs/
// transcript_full.jsonl).
var folderIDTranscriptStems = map[string]bool{
	"transcript":      true,
	"transcript_full": true,
}

// sessionID derives a stable native session id from the transcript path. It
// is the single place a harvested turn's session id comes from (turns that
// carry Turn.SessionID, e.g. OpenCode, override it). It returns "" when the
// path does not identify a single conversation; such turns still feed memory
// extraction but are not mirrored as a Timeline chat.
func sessionID(path string) string {
	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	stem := strings.ToLower(strings.TrimSpace(base))
	if !containerTranscriptStems[stem] {
		return base
	}
	if folderIDTranscriptStems[stem] {
		return conversationFolderID(filepath.Dir(path))
	}
	return ""
}

// conversationFolderID walks up from dir to the conversation folder of a
// brain transcript: the folder directly under "brain", else the nearest
// UUID-named ancestor. "" when neither exists.
func conversationFolderID(dir string) string {
	var uuidAncestor string
	for i := 0; i < 6; i++ {
		name := filepath.Base(dir)
		parent := filepath.Dir(dir)
		if name == "" || name == "." || parent == dir {
			break
		}
		if strings.EqualFold(filepath.Base(parent), "brain") {
			return name
		}
		if uuidAncestor == "" && looksLikeUUID(name) {
			uuidAncestor = name
		}
		dir = parent
	}
	return uuidAncestor
}

// noiseTypes are JSONL record types that carry no dialogue or structured
// tool-call value. tool_use / function_call / tool_call are NOT noise: F3
// requires retaining structured tool_calls on harvested turns so provenance
// (ParseToolCalls → pushParsedToolOps) is non-empty for file-edit sessions.
var noiseTypes = map[string]bool{
	"tool_result": true, "function_result": true, "function_call_output": true,
	// api-request-blob: Claude Code logs each raw API request body as its own
	// line; it repeats the assistant message and must not become a turn.
	"api_request": true, "api_response": true, "api-request-blob": true,
	"system": true, "summary": true, "snapshot": true,
	"file-history-snapshot": true, "queue-operation": true,
	"background_task": true, "mcp_tool_call": true, "mcp_tool_result": true,
	"thinking": true, "progress": true, "reasoning": true,
	// Codex rollout envelope types (dialogue lives under payload).
	"session_meta": true, "turn_context": true,
	"token_count": true, "task_complete": true,
}

// toolRecordTypes are top-level (or payload) types that represent a structured
// tool invocation rather than dialogue text.
var toolRecordTypes = map[string]bool{
	"tool_use": true, "tool_call": true, "function_call": true,
	"custom_tool_call": true, "server_tool_use": true,
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
	var titles nativeTitles
	defer func() { titles.apply(turns) }()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxJSONLLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) > maxJSONLLineBytes {
			continue
		}
		if titles.observe(line) {
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
// rollout envelopes ({type, payload:{role|message|content}}). Structured
// tool_use / function_call blocks are retained on Turn.ToolCalls (F3).
func parseTurnLine(line []byte) (t Turn, ok bool) {
	if len(bytes.TrimSpace(line)) == 0 {
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
	// Codex keeps its original timestamp on the envelope, not the payload.
	// Save it before unwrapping dialogue fields.
	envelopeTime := extractTime(m)
	// Codex / OpenAI Responses-style: unwrap payload for dialogue fields.
	if p, ok := m["payload"].(map[string]any); ok {
		pt := strings.ToLower(strField(p, "type"))
		switch typ {
		case "response_item":
			if toolRecordTypes[pt] {
				// Keep structured function_call / custom_tool_call as a turn.
				m = p
				typ = pt
			} else if pt != "" && pt != "message" && pt != "output_text" && pt != "input_text" {
				if noiseTypes[pt] {
					return Turn{}, false
				}
			} else {
				m = p
			}
		case "event_msg":
			switch pt {
			case "user_message", "agent_message", "assistant_message", "message":
				m = p
			default:
				return Turn{}, false
			}
		default:
			// Generic nested payload with role/content — prefer it.
			if strField(p, "role") != "" || p["content"] != nil || strField(p, "message") != "" || toolRecordTypes[pt] {
				m = p
				if pt != "" {
					typ = pt
				}
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
		case "function_call", "tool_use", "tool_call", "custom_tool_call":
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
	if r := strings.ToLower(strings.TrimSpace(role)); r == "tool" || r == "function" || r == "system" || r == "developer" {
		// tool_result rows — keep dialogue clean; structured calls live on
		// the assistant turn that issued them.
		return Turn{}, false
	}
	toolCalls := extractToolCalls(m)
	if toolRecordTypes[typ] && len(toolCalls) == 0 {
		return Turn{}, false
	}
	content := extractContent(m)
	if content == "" {
		// Codex event_msg often uses "message" as a plain string.
		if s, _ := m["message"].(string); strings.TrimSpace(s) != "" {
			content = s
		}
	}
	if strings.TrimSpace(content) == "" && len(toolCalls) == 0 {
		return Turn{}, false
	}
	sourceTime := extractTime(m)
	if sourceTime.IsZero() {
		sourceTime = envelopeTime
	}
	if sourceTime.IsZero() && normalizeSpeaker(role) == "user" {
		// Cursor agent transcripts have no time field; the prompt text
		// itself starts with a <timestamp> header.
		if at, ok := cursorPromptTime(content); ok {
			sourceTime = at
		}
	}
	t = Turn{
		Speaker:   normalizeSpeaker(role),
		Content:   strings.TrimSpace(content),
		Timestamp: sourceTime,
		ToolCalls: toolCalls,
	}
	if t.Speaker == "unknown" && len(toolCalls) > 0 {
		t.Speaker = "assistant"
	}
	return t, true
}

// extractToolCalls gathers structured tool_use / function_call blocks from a
// turn map (Claude content arrays, OpenAI tool_calls, Codex function_call).
func extractToolCalls(m map[string]any) []map[string]any {
	if m == nil {
		return nil
	}
	var out []map[string]any
	out = append(out, toolCallsFromContentValue(m["content"])...)
	if mm, ok := m["message"].(map[string]any); ok {
		out = append(out, toolCallsFromContentValue(mm["content"])...)
		out = append(out, normalizeToolCallList(mm["tool_calls"])...)
		out = append(out, normalizeToolCallList(mm["toolCalls"])...)
	}
	out = append(out, normalizeToolCallList(m["tool_calls"])...)
	out = append(out, normalizeToolCallList(m["toolCalls"])...)
	typ := strings.ToLower(strField(m, "type"))
	if toolRecordTypes[typ] {
		if call := normalizeOneToolCall(m); call != nil {
			out = append(out, call)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolCallsFromContentValue(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, item := range arr {
		im, ok := item.(map[string]any)
		if !ok {
			continue
		}
		bt := strings.ToLower(strField(im, "type"))
		if toolRecordTypes[bt] || bt == "" && (strField(im, "name") != "" || strField(im, "tool") != "") {
			if bt == "" || toolRecordTypes[bt] {
				if call := normalizeOneToolCall(im); call != nil {
					out = append(out, call)
				}
			}
		}
	}
	return out
}

func normalizeToolCallList(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, item := range arr {
		im, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if call := normalizeOneToolCall(im); call != nil {
			out = append(out, call)
		}
	}
	return out
}

// normalizeOneToolCall projects harness-specific tool shapes onto a stable
// {name, id?, input|args} map that ParseToolCalls / session_turns.tool_calls
// can consume.
func normalizeOneToolCall(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	name := firstNonEmpty(
		strField(m, "name"),
		strField(m, "tool"),
		strField(m, "ToolName"),
		strField(m, "tool_name"),
	)
	var fnArgs any
	if fn, ok := m["function"].(map[string]any); ok {
		if name == "" {
			name = strField(fn, "name")
		}
		if v, ok := fn["arguments"]; ok {
			fnArgs = v
		} else if v, ok := fn["parameters"]; ok {
			fnArgs = v
		}
	}
	if name == "" {
		return nil
	}
	call := map[string]any{"name": name}
	if id := firstNonEmpty(strField(m, "id"), strField(m, "tool_use_id"), strField(m, "call_id"), strField(m, "toolCallId")); id != "" {
		call["id"] = id
	}
	args := coerceToolArgs(m)
	if len(args) == 0 && fnArgs != nil {
		args = coerceArgsValue(fnArgs)
	}
	if len(args) > 0 {
		call["input"] = args
		call["args"] = args
	} else if raw, ok := m["arguments"]; ok {
		// Codex often ships apply_patch / shell args as a raw string.
		switch s := raw.(type) {
		case string:
			if strings.TrimSpace(s) != "" {
				call["arguments"] = s
				if parsed := coerceArgsValue(s); len(parsed) > 0 {
					call["input"] = parsed
					call["args"] = parsed
				}
			}
		}
	}
	return call
}

func coerceToolArgs(m map[string]any) map[string]any {
	for _, k := range []string{"input", "args", "Args", "arguments", "parameters", "parameters_json"} {
		if v, ok := m[k]; ok {
			if args := coerceArgsValue(v); len(args) > 0 {
				return args
			}
		}
	}
	return nil
}

func coerceArgsValue(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		if s[0] == '{' {
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err == nil {
				return m
			}
		}
		// Codex apply_patch: raw unified-ish patch text — surface as patch.
		if strings.Contains(s, "*** Begin Patch") || strings.Contains(s, "*** Update File:") {
			path := codexPatchPath(s)
			out := map[string]any{"patch": s}
			if path != "" {
				out["path"] = path
				out["TargetFile"] = path
			}
			return out
		}
	}
	return nil
}

func codexPatchPath(patch string) string {
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Update File:", "*** Add File:", "*** Delete File:"} {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return ""
}

// extractContent pulls text from the common content shapes. Structured
// tool_use/input blocks are collected separately via extractToolCalls.
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
				// tool_use / image / etc.: not inlined into text; see extractToolCalls.
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

// turnPayload renders a Turn into the CONVERSATION_TURN payload shape.
// Content is secret-screened (issue #132): transcripts routinely contain
// pasted secrets, and the event stream lands in Postgres. Structured
// tool_calls are retained for provenance (F3 / ParseToolCalls).
func turnPayload(action, agent, path string, t Turn) map[string]any {
	id := sessionID(path)
	if t.SessionID != "" {
		id = t.SessionID
	}
	p := map[string]any{
		"action":     action,
		"agent":      agent,
		"path":       path,
		"session_id": id,
		"speaker":    t.Speaker,
		"content":    redact(t.Content),
	}
	if t.SessionTitle != "" {
		p["session_title"] = t.SessionTitle
	}
	if !t.Timestamp.IsZero() {
		p["timestamp"] = t.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	// Preserve structured calls separately from the redacted preview.  They
	// drive provenance and the cloud Timeline's tool indicators; embedding
	// them into conversation text would make both less reliable.
	if t.SourcePosition > 0 {
		p["source_position"] = t.SourcePosition
	}
	if t.Usage != nil {
		p["usage"] = usagePayload(*t.Usage, t.UsageKey)
		if t.UsageOnly() {
			p["usage_only"] = true
		}
	}
	if len(t.ToolCalls) > 0 {
		// Emit as []any so JSON encoders and ParseToolCalls([]any) agree.
		flat := make([]any, len(t.ToolCalls))
		for i, c := range t.ToolCalls {
			flat[i] = c
		}
		p["tool_calls"] = flat
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
	h.mu.Lock()
	titles, knownTitle := h.titles[path]
	h.mu.Unlock()
	if !knownTitle && off > 0 {
		// Resumed past the title (restart cursor or first-sight window):
		// recover it from the bytes just before the cursor.
		titles = titlesBefore(f, off)
	}
	// Stream the new bytes line by line instead of buffering them: a first
	// read of a long chat is megabytes, mostly api-request-blob records that
	// are dropped unparsed. Only complete lines are consumed, so a torn
	// write (no trailing newline) is read again on the next poll. Reading
	// stops at the size stat'ed above; later appends wait for the next call.
	lr := newTranscriptLineReader(io.LimitReader(f, st.Size()-off), maxJSONLLineBytes)
	var turns []Turn
	var consumed, unthrottled int64
	position := off
	for {
		line, size, overlong, ok := lr.next()
		if !ok {
			break
		}
		consumed += int64(size)
		if h.readThrottle != nil {
			if unthrottled += int64(size); unthrottled >= 1<<20 {
				h.readThrottle(int(unthrottled))
				unthrottled = 0
			}
		}
		if overlong {
			// Overlong single line: skip it, keep reading the rest.
			position += int64(size)
			continue
		}
		// Source positions count each line without its line ending plus one,
		// as they always have: they key cloud turns, so they must not change.
		position += int64(len(line)) + 1
		if skippableTranscriptLine(line) {
			continue
		}
		if titles.observe(line) {
			continue
		}
		t, ok := parseTurnLine(line)
		u, key, model := parseLineUsage(line)
		if model != "" {
			h.setModel(path, model)
		}
		if u != nil {
			if u.Model == "" {
				u.Model = h.modelFor(path)
			}
			if !ok {
				t, ok = Turn{Speaker: "assistant", Timestamp: lineTime(line)}, true
			}
			t.Usage, t.UsageKey = u, key
		}
		if ok {
			t.SourcePosition = position
			turns = append(turns, t)
		}
	}
	if h.readThrottle != nil && unthrottled > 0 {
		h.readThrottle(int(unthrottled))
	}
	titles.apply(turns)
	if consumed == 0 {
		return nil, nil // nothing complete yet
	}

	now := h.now().UTC()
	h.mu.Lock()
	agent := h.agents[path]
	carry, knownCarry := h.cursorSourceAt[path]
	h.mu.Unlock()
	cursorTranscript := len(turns) > 0 && isCursorTranscript(agent, path)
	if cursorTranscript {
		if !knownCarry && off > 0 {
			carry = sourceTimeBefore(f, off)
		}
		carry = fillCursorSourceTimes(turns, carry, historicalMtime(st.ModTime(), now))
	}

	h.mu.Lock()
	if cursorTranscript && !carry.IsZero() {
		if h.cursorSourceAt == nil {
			h.cursorSourceAt = make(map[string]time.Time)
		}
		h.cursorSourceAt[path] = carry
	}
	h.offsets[path] = off + consumed
	h.offsetsVersion++
	if h.titles == nil {
		h.titles = make(map[string]nativeTitles)
	}
	h.titles[path] = titles
	if len(turns) > 0 {
		last := now
		for i := range turns {
			if turns[i].Timestamp.After(last) {
				last = turns[i].Timestamp
			}
		}
		h.lastActive[path] = last
		delete(h.completed, path) // new activity re-arms idle detection
		for _, t := range turns {
			if !t.UsageOnly() {
				h.addBatchLocked(path, t)
			}
		}
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

// maxFallbackSeen bounds the per-database dedupe set of fallbackTurnKey
// hashes (8 bytes each; it used to keep every turn's full text forever).
const maxFallbackSeen = 20000

func fallbackTurnKey(t Turn) uint64 {
	f := fnv.New64a()
	f.Write([]byte(t.Speaker))
	f.Write([]byte{0})
	f.Write([]byte(t.Content))
	return f.Sum64()
}

func (h *Harvester) freshFallbackTurns(path string, turns []Turn) []Turn {
	if len(turns) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := h.fallbackSeen[path]
	if seen == nil || len(seen) > maxFallbackSeen {
		// Each scan reads the newest window of the database, so keys that
		// fell out of the window never come back: keep only this window's.
		old := seen
		seen = make(map[uint64]struct{}, len(turns))
		for _, turn := range turns {
			if _, ok := old[fallbackTurnKey(turn)]; ok {
				seen[fallbackTurnKey(turn)] = struct{}{}
			}
		}
		h.fallbackSeen[path] = seen
	}
	fresh := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		key := fallbackTurnKey(turn)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		fresh = append(fresh, turn)
	}
	return fresh
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
	// OpenCode writes to WAL while the main DB's stat can stay unchanged.
	if strings.EqualFold(filepath.Base(path), "opencode.db") {
		if wal, err := os.Stat(path + "-wal"); err == nil {
			cur.size += wal.Size()
			if wal.ModTime().After(cur.mtime) {
				cur.mtime = wal.ModTime()
			}
		}
	}
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
		h.mu.Lock()
		for i := range turns {
			// Receipt time drives idle detection only. Missing source timestamps
			// stay missing in the emitted turn and archived transcript.
			activityAt := turns[i].Timestamp
			if activityAt.IsZero() {
				activityAt = now
			}
			turnPath := path
			if turns[i].SessionID != "" {
				turnPath = filepath.Join(filepath.Dir(path), ".nexus-opencode-sessions", turns[i].SessionID+".jsonl")
				h.agents[turnPath] = agent
			}
			if activityAt.After(h.lastActive[turnPath]) {
				h.lastActive[turnPath] = activityAt
			}
			delete(h.completed, turnPath)
			if !turns[i].UsageOnly() {
				h.addBatchLocked(turnPath, turns[i])
			}
			if activityAt.After(h.lastActive[path]) {
				h.lastActive[path] = activityAt
			}
		}
		h.mu.Unlock()
		for _, t := range turns {
			turnPath := path
			if t.SessionID != "" {
				turnPath = filepath.Join(filepath.Dir(path), ".nexus-opencode-sessions", t.SessionID+".jsonl")
			}
			h.emit(Event{
				Type:      EventConversationTurn,
				Payload:   turnPayload("conversation_turn", agent, turnPath, t),
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
		if strings.EqualFold(filepath.Base(path), "opencode.db") {
			if err != nil {
				log.Printf("harvester: OpenCode database read: %v", err)
			}
			return false
		}
	}
	if h.fallbackDue(path, now) {
		if turns := h.freshFallbackTurns(path, extractSQLiteFallback(path, since)); runExtract(turns) {
			return true
		}
	} else if !firstSight {
		// Change seen but the content scan is rate-limited: keep the session
		// alive without re-reading the database.
		h.mu.Lock()
		h.lastActive[path] = now
		delete(h.completed, path)
		h.mu.Unlock()
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

// maxIdleBatchTurns bounds the turns a session keeps for its end-of-session
// batch. Every turn has already been emitted on its own; the batch only
// feeds the end-of-session extraction, which reads the speaker and text of
// at most 120 turns. A chat that stays active for hours never goes idle,
// and keeping all its turns (tool inputs included) grew the heap with the
// length of the chat — and a history import held every imported chat's.
const maxIdleBatchTurns = 120

// addBatchLocked queues t for path's end-of-session batch, keeping the
// latest maxIdleBatchTurns (up to twice that between trims) and only the
// fields the batch carries. h.mu must be held.
func (h *Harvester) addBatchLocked(path string, t Turn) {
	b := append(h.batch[path], Turn{Speaker: t.Speaker, Content: t.Content, Timestamp: t.Timestamp})
	if len(b) > 2*maxIdleBatchTurns {
		drop := len(b) - maxIdleBatchTurns
		if h.batchDropped == nil {
			h.batchDropped = make(map[string]int)
		}
		h.batchDropped[path] += drop
		// Copy so the dropped turns' backing array is released.
		b = append([]Turn(nil), b[drop:]...)
	}
	h.batch[path] = b
}

// CheckIdle emits SESSION_TRANSCRIPT_COMPLETE for every session with no new
// content for IdleTimeout. Each session fires once until new activity
// re-arms it. Returned events were also passed to the emitter.
func (h *Harvester) CheckIdle() []Event {
	now := h.now().UTC()
	h.mu.Lock()
	var due []string
	for path, last := range h.lastActive {
		if strings.EqualFold(filepath.Base(path), "opencode.db") {
			continue
		} // global cursor, not a chat
		if !h.completed[path] && now.Sub(last) >= h.IdleTimeout {
			due = append(due, path)
		}
	}
	var out []Event
	for _, path := range due {
		h.completed[path] = true
		turns := append([]Turn(nil), h.batch[path]...)
		harvested := len(turns) + h.batchDropped[path]
		// Drain the batch (issue #132): without this the session's turns
		// accumulate forever and every re-armed idle re-emits all old
		// turns to the Memory Processor (duplicate extraction + LLM cost).
		delete(h.batch, path)
		delete(h.batchDropped, path)
		detail := strconv.Itoa(harvested) + " turns harvested"
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
			if len(t.ToolCalls) > 0 {
				flatCalls := make([]any, len(t.ToolCalls))
				for i, c := range t.ToolCalls {
					flatCalls[i] = c
				}
				m["tool_calls"] = flatCalls
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
				"turn_count": harvested,
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
		if hit, got, ok := h.considerFile(src, p, info); ok {
			files++
			turns += got
			hits = append(hits, hit)
			if agentFiles != nil {
				agentFiles[src.Agent]++
			}
		}
		return nil
	})
	return files, turns, hits, walkErr
}

// considerFile applies the per-file capture rules (size, secrets, workspace
// attribution, format) and reads new content. It is shared by the periodic
// walk and by file-change notifications so both follow identical rules.
// ok=false means the file is not a transcript this harvester captures.
func (h *Harvester) considerFile(src TranscriptSource, p string, info os.FileInfo) (hit HarvestFileHit, turns int, ok bool) {
	// SQLite extractors query by SQL — allow large DBs (OpenCode ~GB).
	tooBig := info.Size() > maxWalkFileBytes
	if tooBig && !(src.Format == FormatSQLite && strings.EqualFold(filepath.Base(p), "opencode.db")) {
		return hit, 0, false
	}
	if adapters.ClassifyPath(p) == adapters.Never {
		return hit, 0, false
	}
	ext := strings.ToLower(filepath.Ext(p))
	switch src.Format {
	case FormatJSONL:
		if ext != ".jsonl" {
			return hit, 0, false
		}
		base := filepath.Base(p)
		// Antigravity brains: only the canonical transcript logs.
		if src.Agent == "antigravity" {
			if base != "transcript_full.jsonl" && base != "transcript.jsonl" {
				return hit, 0, false
			}
			if base == "transcript.jsonl" {
				full := filepath.Join(filepath.Dir(p), "transcript_full.jsonl")
				if _, err := os.Stat(full); err == nil {
					return hit, 0, false // prefer the full transcript sibling
				}
			}
		}
	case FormatJSON:
		if ext != ".json" {
			return hit, 0, false
		}
	case FormatSQLite:
		if ext != ".db" && ext != ".sqlite" && ext != ".sqlite3" && ext != ".vscdb" {
			return hit, 0, false
		}
		// OpenCode: only the main DB (ignore snapshots / sidecars).
		if src.Agent == "opencode" && !strings.EqualFold(filepath.Base(p), "opencode.db") {
			return hit, 0, false
		}
	default:
		return hit, 0, false
	}
	// Chats run inside Nexus are saved by Nexus; their agent transcript
	// would be a duplicate.
	if isNexusOwnedTranscript(p) {
		return hit, 0, false
	}
	// Attribution last: it may peek file contents, so cheap name/format
	// checks above filter first.
	if h.routeFilter != nil {
		if !h.routeFilter(p, src) {
			return hit, 0, false
		}
	} else if !src.Global && !h.MatchesTranscript(p, src.CwdMatch) {
		return hit, 0, false
	}
	hit = HarvestFileHit{Agent: src.Agent, Format: src.Format, Path: p, Name: filepath.Base(p)}
	h.mu.Lock()
	h.agents[p] = src.Agent
	h.mu.Unlock()
	if src.Format == FormatSQLite {
		_ = h.TrackSQLite(p, info)
		return hit, 0, true
	}
	got, _ := h.TailFile(p)
	return hit, len(got), true
}

// sqliteFallbackInterval bounds how often one IDE database is content-scanned.
var sqliteFallbackInterval = 2 * time.Minute

// fallbackDue reports whether the content-scanning SQLite fallback may run
// for path now, and records the attempt when it may.
func (h *Harvester) fallbackDue(path string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastFallback == nil {
		h.lastFallback = make(map[string]time.Time)
	}
	if last, ok := h.lastFallback[path]; ok && now.Sub(last) < sqliteFallbackInterval {
		return false
	}
	h.lastFallback[path] = now
	return true
}

// cursorSnapshot copies the read cursors and their version for persistence.
func (h *Harvester) cursorSnapshot() (map[string]int64, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int64, len(h.offsets))
	for k, v := range h.offsets {
		out[k] = v
	}
	return out, h.offsetsVersion
}

// restoreCursors seeds read cursors saved by a previous run, so a restart
// continues where it stopped instead of re-reading every transcript. Files
// that shrank since are re-read from the start by TailFile.
func (h *Harvester) restoreCursors(saved map[string]int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for path, off := range saved {
		if off >= 0 {
			if _, ok := h.offsets[path]; !ok {
				h.offsets[path] = off
			}
		}
	}
}

// setSharedScanStats publishes the shared scanner's per-workspace view so
// the existing status surfaces (Stats) keep working for attached harvesters.
func (h *Harvester) setSharedScanStats(at time.Time, files, turns int, agentFiles map[string]int, hits []HarvestFileHit, scanErr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !at.IsZero() {
		h.lastScanAt = at
	}
	h.lastScanFiles = files
	h.lastScanTurns = turns
	h.lastScanErr = scanErr
	h.agentFiles = agentFiles
	if len(hits) > 40 {
		hits = hits[:40]
	}
	h.recentFiles = hits
}

// Start runs the poll loop (ScanAndTail + CheckIdle every PollInterval)
// until ctx is done. Stdlib polling replaces fsnotify per the decision doc.
func (h *Harvester) Start(ctx context.Context) {
	t := time.NewTicker(h.PollInterval)
	defer t.Stop()
	if !h.IsPaused() {
		_ = h.ScanAndTail()
		_ = h.CheckIdle()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if h.IsPaused() {
				continue
			}
			_ = h.ScanAndTail()
			_ = h.CheckIdle()
		}
	}
}

// nativeTitles collects the chat title an agent writes into its transcript:
// Claude Code records {"type":"ai-title","aiTitle":...} and, when the user
// renames the chat, {"type":"custom-title","customTitle":...}. Without it a
// captured chat was named after a fragment of its latest message.
type nativeTitles struct {
	ai, custom string
}

// observe reports whether line is a title record (and records it).
func (n *nativeTitles) observe(line []byte) bool {
	if !bytes.Contains(line, []byte(`-title"`)) {
		return false
	}
	var rec struct {
		Type        string `json:"type"`
		AITitle     string `json:"aiTitle"`
		CustomTitle string `json:"customTitle"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return false
	}
	switch rec.Type {
	case "ai-title":
		if t := strings.TrimSpace(rec.AITitle); t != "" {
			n.ai = t
		}
		return true
	case "custom-title":
		if t := strings.TrimSpace(rec.CustomTitle); t != "" {
			n.custom = t
		}
		return true
	}
	return false
}

// titleLookbackBytes bounds how much already-read transcript titlesBefore
// scans when a cursor resumes past the title records.
const titleLookbackBytes = 1 << 20

// titlesBefore recovers the latest title records from the window of f just
// before off. Claude re-appends its title as the chat grows, so a bounded
// window normally holds the current one; a miss only means no title yet.
func titlesBefore(f *os.File, off int64) nativeTitles {
	var n nativeTitles
	start := off - titleLookbackBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, off-start)
	read, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return n
	}
	for _, line := range bytes.Split(buf[:read], []byte{'\n'}) {
		n.observe(line)
	}
	return n
}

// apply names every turn of the batch after the chat (a person's own name
// for it wins over the generated one).
func (n *nativeTitles) apply(turns []Turn) {
	title := n.custom
	if title == "" {
		title = n.ai
	}
	if title == "" {
		return
	}
	title = redact(title)
	for i := range turns {
		if turns[i].SessionTitle == "" {
			turns[i].SessionTitle = title
		}
	}
}
