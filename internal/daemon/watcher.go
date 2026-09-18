// watcher.go is Layer 3 (Instruction File Watcher) of the passive extraction
// pipeline (implementation-plan.md §1.3, §2.2).
//
// It polls the known instruction files inside a workspace root, compares
// SHA256 hashes, and emits INSTRUCTION_FILE_CHANGED events with a capped
// diff when content changes. The Memory Processor later extracts facts from
// the diff.
//
// Polling (stdlib only) is used instead of fsnotify — see
// docs/decisions/2026-09-17-interceptor-watcher.md for the full rationale
// (zero new deps per go.mod, SQLite-hostile environments, Windows-friendly).
//
// Ownership note: like interceptor.go, this file only coordinates with the
// rest of the daemon through the local ToolEventEmitter interface and file
// existence checks. It never edits daemon.go / fileops.go.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WatchedFile mirrors the watched_files table row shape from plan §1.1
// (see internal/store/models.go). It is defined locally — not imported —
// because internal/store currently requires external modules (pgx,
// pgvector) while the daemon package must stay stdlib-only, and because
// storage ownership belongs to another agent. Field names/types are kept
// identical so mapping to the store row is a plain struct copy; a schema
// drift will surface in ToStoreWatchedFile call sites at integration time.
type WatchedFile struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Path        string    `json:"path"`
	LastHash    string    `json:"last_hash"`
	FileType    string    `json:"file_type"` // claude_md | cursorrules | copilot_instructions | windsurfrules | custom
	CreatedAt   time.Time `json:"created_at"`
}

// WatchedTarget is one instruction file tracked inside a workspace root.
type WatchedTarget struct {
	// RelPath is slash-separated and relative to the workspace root.
	RelPath string
	// FileType matches the watched_files.file_type CHECK constraint.
	FileType string
}

// DefaultWatchedTargets lists the instruction files from plan §1.3.
// Kept at 4 for backward compatibility; ExtendedWatchedTargets adds the
// issue-#118 gaps (AGENTS.md, .cursor/rules).
func DefaultWatchedTargets() []WatchedTarget {
	return []WatchedTarget{
		{RelPath: "CLAUDE.md", FileType: "claude_md"},
		{RelPath: ".cursorrules", FileType: "cursorrules"},
		{RelPath: ".github/copilot-instructions.md", FileType: "copilot_instructions"},
		{RelPath: ".windsurfrules", FileType: "windsurfrules"},
	}
}

// ExtendedWatchedTargets covers the issue-#118 watcher gaps: the base four
// plus AGENTS.md (agents_md) and .cursor/rules (cursor_rules). Nested
// CLAUDE.md files (e.g. pkg/foo/CLAUDE.md) are discovered by
// NestedClaudeFiles and checked alongside the fixed targets.
func ExtendedWatchedTargets() []WatchedTarget {
	base := DefaultWatchedTargets()
	return append(base,
		WatchedTarget{RelPath: "AGENTS.md", FileType: "agents_md"},
		WatchedTarget{RelPath: ".cursor/rules", FileType: "cursor_rules"},
	)
}

// WatcherPollInterval is the fallback ticker period for Start().
const WatcherPollInterval = 5 * time.Second

// WatchedFileStore persists last-known instruction-file hashes across
// daemon restarts (issue #34). Without it the watcher keeps hashes in
// memory only: a restart forgets every hash, so edits made while down are
// silently adopted as the new baseline instead of surfacing as changes.
//
// The interface is defined here (not imported from internal/store) so the
// daemon never depends on the Postgres layer: the server persists hashes
// in watched_files, while the daemon carries this local seam. Keys are
// (workspaceID, workspace-relative slash path).
type WatchedFileStore interface {
	// GetHash returns the persisted hash for a watched file.
	GetHash(workspaceID, path string) (hash string, ok bool)
	// SetHash records the latest hash for a watched file.
	SetHash(workspaceID, path, hash string) error
	// DeleteHash drops a watched file (e.g. it was deleted on disk).
	DeleteHash(workspaceID, path string) error
}

// MemoryHashStore is a mutex-guarded in-memory WatchedFileStore for tests
// and for daemons that opt out of persistence (nil store behaves the same).
type MemoryHashStore struct {
	mu     sync.Mutex
	hashes map[string]map[string]string // workspaceID -> path -> hash
}

// NewMemoryHashStore returns an empty in-memory hash store.
func NewMemoryHashStore() *MemoryHashStore {
	return &MemoryHashStore{hashes: map[string]map[string]string{}}
}

// GetHash returns the stored hash, if any.
func (s *MemoryHashStore) GetHash(workspaceID, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hashes[workspaceID][path]
	return h, ok
}

// SetHash records the hash.
func (s *MemoryHashStore) SetHash(workspaceID, path, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes[workspaceID] == nil {
		s.hashes[workspaceID] = map[string]string{}
	}
	s.hashes[workspaceID][path] = hash
	return nil
}

// DeleteHash drops the stored hash.
func (s *MemoryHashStore) DeleteHash(workspaceID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hashes[workspaceID], path)
	if len(s.hashes[workspaceID]) == 0 {
		delete(s.hashes, workspaceID)
	}
	return nil
}

// FileHashStore is a JSON file-backed WatchedFileStore: the daemon's
// restart-durable default. The file holds {workspaceID: {path: hash}} and
// is rewritten on every mutation (best-effort — the watcher ignores
// persistence errors and CheckOnce remains the correctness fallback).
type FileHashStore struct {
	path   string
	mu     sync.Mutex
	hashes map[string]map[string]string
}

// NewFileHashStore loads hashes from path (missing file = empty store;
// malformed JSON is an error so a corrupt state file is never silently
// treated as "nothing changed").
func NewFileHashStore(path string) (*FileHashStore, error) {
	s := &FileHashStore{path: path, hashes: map[string]map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.hashes); err != nil {
		return nil, err
	}
	if s.hashes == nil {
		s.hashes = map[string]map[string]string{}
	}
	return s, nil
}

// Path returns the backing file path.
func (s *FileHashStore) Path() string { return s.path }

// GetHash returns the stored hash, if any.
func (s *FileHashStore) GetHash(workspaceID, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hashes[workspaceID][path]
	return h, ok
}

// SetHash records the hash and rewrites the file.
func (s *FileHashStore) SetHash(workspaceID, path, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hashes[workspaceID] == nil {
		s.hashes[workspaceID] = map[string]string{}
	}
	s.hashes[workspaceID][path] = hash
	return s.saveLocked()
}

// DeleteHash drops the stored hash and rewrites the file.
func (s *FileHashStore) DeleteHash(workspaceID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hashes[workspaceID], path)
	if len(s.hashes[workspaceID]) == 0 {
		delete(s.hashes, workspaceID)
	}
	return s.saveLocked()
}

// saveLocked rewrites the JSON file atomically (caller holds mu): write to
// a temp file in the same directory, fsync, then rename (issue #109).
// A crash mid-write never truncates the state file. Parent dirs are
// created so a fresh state path works on first boot.
func (s *FileHashStore) saveLocked() error {
	data, err := json.MarshalIndent(s.hashes, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	} else {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".hashes-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	_ = os.Chmod(s.path, 0o600)
	if df, err := os.Open(dir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}
	return nil
}

// Watcher polls instruction files under root and emits change events.
type Watcher struct {
	root        string
	targets     []WatchedTarget
	interval    time.Duration
	emitter     ToolEventEmitter // may be nil: CheckOnce results still returned
	workspaceID string           // hash-store namespace; "" = memory-only baselines
	hashStore   WatchedFileStore // nil = memory-only (no persistence)

	mu       sync.Mutex
	known    map[string]string // relPath -> sha256 hex of last observed content
	contents map[string]string // relPath -> last observed content (for diffs)

	stopOnce sync.Once
}

// NewWatcher creates a Watcher for a workspace root. targets == nil selects
// DefaultWatchedTargets(). interval <= 0 selects WatcherPollInterval.
// root may not exist yet — existence is checked per poll via os.Stat, so a
// workspace that appears later is picked up without error.
func NewWatcher(root string, targets []WatchedTarget, interval time.Duration, emitter ToolEventEmitter) *Watcher {
	if targets == nil {
		targets = DefaultWatchedTargets()
	}
	if interval <= 0 {
		interval = WatcherPollInterval
	}
	return &Watcher{
		root:     root,
		targets:  targets,
		interval: interval,
		emitter:  emitter,
		known:    make(map[string]string),
		contents: make(map[string]string),
	}
}

// NewWatcherWithStore is NewWatcher with hash persistence installed before
// the baseline snapshot is taken, so persisted hashes seed the baseline:
// content edited while the daemon was down surfaces as a change on the
// first CheckOnce instead of being silently adopted. A nil store (or empty
// workspaceID) means memory-only baselines, identical to NewWatcher.
// Issue #34.
func NewWatcherWithStore(root string, targets []WatchedTarget, interval time.Duration, emitter ToolEventEmitter, workspaceID string, hashStore WatchedFileStore) *Watcher {
	w := NewWatcher(root, targets, interval, emitter)
	if workspaceID != "" {
		w.workspaceID = workspaceID
	}
	if hashStore != nil {
		w.hashStore = hashStore
	}
	return w
}

// HashBytes returns the hex SHA256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashString returns the hex SHA256 of s.
func HashString(s string) string {
	return HashBytes([]byte(s))
}

// HashFile returns the hex SHA256 of the file at path, streaming so large
// instruction files never fully load into memory twice.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SeedBaseline records current on-disk hashes without emitting events. Call
// on daemon startup so pre-existing instruction files do not produce a storm
// of INSTRUCTION_FILE_CHANGED events. With a hash store installed
// (NewWatcherWithStore), persisted hashes seed the baseline instead of fresh
// disk hashes, so edits made while the daemon was down surface as changes
// on the first CheckOnce. In-memory contents still snapshot current disk
// content for diff context.
func (w *Watcher) SeedBaseline() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.targets {
		abs := filepath.Join(w.root, filepath.FromSlash(t.RelPath))
		content, err := os.ReadFile(abs)
		if err != nil {
			continue // missing/unreadable: checked again on next poll
		}
		hash := HashBytes(content)
		if w.hashStore != nil && w.workspaceID != "" {
			if persisted, ok := w.hashStore.GetHash(w.workspaceID, t.RelPath); ok && persisted != "" {
				hash = persisted
			}
		}
		w.known[t.RelPath] = hash
		w.contents[t.RelPath] = string(content)
	}
}

// CheckOnce polls every target a single time and returns the change events.
// It is the synchronous, fully testable core; Start() just ticks it.
// Semantics:
//   - missing file  -> forgotten from known state, no event;
//   - first sight   -> baselined silently, no event;
//   - same hash     -> no event;
//   - changed hash  -> INSTRUCTION_FILE_CHANGED with capped diff + emit.
func (w *Watcher) CheckOnce() []ToolEvent {
	w.mu.Lock()
	defer w.mu.Unlock()

	var out []ToolEvent
	now := time.Now().UTC()
	for _, t := range w.targets {
		abs := filepath.Join(w.root, filepath.FromSlash(t.RelPath))
		// Coordinate via file existence checks only (no edits to other
		// daemon files, per task constraints).
		if _, err := os.Stat(abs); err != nil {
			if os.IsNotExist(err) {
				delete(w.known, t.RelPath)
				delete(w.contents, t.RelPath)
				// Best-effort: drop the persisted hash too (issue #34).
				if w.hashStore != nil && w.workspaceID != "" {
					_ = w.hashStore.DeleteHash(w.workspaceID, t.RelPath)
				}
			}
			continue
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		hash := HashBytes(content)
		oldHash, seen := w.known[t.RelPath]
		if !seen {
			// A persisted hash (pre-restart state) counts as seen: an
			// edit made while down must surface, not silently rebaseline.
			if w.hashStore != nil && w.workspaceID != "" {
				if persisted, ok := w.hashStore.GetHash(w.workspaceID, t.RelPath); ok && persisted != "" {
					oldHash, seen = persisted, true
				}
			}
		}
		if !seen {
			w.known[t.RelPath] = hash
			w.contents[t.RelPath] = string(content)
			continue
		}
		if hash == oldHash {
			continue
		}
		diff := BuildDiff(w.contents[t.RelPath], string(content), MaxDiffBytes)
		ev := ToolEvent{
			Type: ToolEventInstructionFileChanged,
			Payload: map[string]any{
				"action":    "instruction_file_changed",
				"path":      t.RelPath,
				"file_type": t.FileType,
				"old_hash":  oldHash,
				"new_hash":  hash,
				// Secret-screened (issue #132): instruction files hold
				// API keys and tokens; the diff lands in the event stream.
				"diff": redact(diff),
			},
			CreatedAt: now,
		}
		w.known[t.RelPath] = hash
		w.contents[t.RelPath] = string(content)
		out = append(out, ev)
		// Best-effort hash persistence across restarts (issue #34);
		// CheckOnce stays correct even when persistence fails.
		if w.hashStore != nil && w.workspaceID != "" {
			_ = w.hashStore.SetHash(w.workspaceID, t.RelPath, hash)
		}
		if w.emitter != nil {
			w.emitter.Emit(ev)
		}
	}
	return out
}

// Start ticks CheckOnce until ctx ends. It never returns an error; poll
// failures are swallowed per-file so one unreadable file cannot stop the
// watcher. SeedBaseline is NOT called automatically — the daemon entrypoint
// decides whether startup state should be silent or eventful.
func (w *Watcher) Start(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.CheckOnce()
		}
	}
}

// KnownHashes returns a copy of the last-observed hashes (test/debug aid).
func (w *Watcher) KnownHashes() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make(map[string]string, len(w.known))
	for k, v := range w.known {
		cp[k] = v
	}
	return cp
}

// ToStoreWatchedFile converts an observed target into the store row shape for
// the watched_files table, showing how this watcher feeds plan §1.1 without
// this package owning any storage code.
func ToStoreWatchedFile(workspaceID string, target WatchedTarget, hash string) WatchedFile {
	return WatchedFile{
		WorkspaceID: workspaceID,
		Path:        target.RelPath,
		LastHash:    hash,
		FileType:    target.FileType,
		CreatedAt:   time.Now().UTC(),
	}
}
