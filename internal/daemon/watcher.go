// Instruction-file watcher for the workspace daemon (issue #4).
//
// Layer 3 of passive extraction (plan §§1.3, 2.2): watches the known agent
// instruction files for out-of-band edits (the user editing rules by hand)
// and emits INSTRUCTION_FILE_CHANGED events carrying a hash-verified diff.
// The Memory Processor later extracts facts from that diff.
//
// Watched paths transcribe the plan §1.3 table exactly:
//
//	CLAUDE.md                       claude_md
//	.cursorrules                     cursorrules
//	.github/copilot-instructions.md  copilot_instructions
//	.windsurfrules                   windsurfrules
//
// Mechanism: fsnotify for prompt notification + SHA256 comparison against
// the last known hash (mirroring the watched_files.last_hash column from
// migration 001) + a debounced flush that coalesces rapid editor saves.
// PollOnce re-checks every watched file deterministically; it doubles as
// the fsnotify-gap fallback (e.g. a watched parent dir that did not exist
// at startup) and as the timing-free hook unit tests use.
//
// Hash/diff/type-mapping helpers are pure functions over strings/bytes so
// they are unit-testable without filesystem or network access.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchSpec pairs a workspace-relative instruction file with its
// watched_files.file_type value (migration 001 CHECK constraint).
type WatchSpec struct {
	RelPath  string
	FileType string
}

// InstructionFiles is the plan §1.3 watch list. Paths are slash-separated
// and relative to the workspace root.
var InstructionFiles = []WatchSpec{
	{RelPath: "CLAUDE.md", FileType: "claude_md"},
	{RelPath: ".cursorrules", FileType: "cursorrules"},
	{RelPath: ".github/copilot-instructions.md", FileType: "copilot_instructions"},
	{RelPath: ".windsurfrules", FileType: "windsurfrules"},
}

const (
	// DefaultDebounce is the quiet period after the last fsnotify event
	// before a dirty instruction file is re-read. Editors typically emit a
	// burst (write + chmod + rename); the debounce coalesces it into one
	// hash check and one event.
	DefaultDebounce = 250 * time.Millisecond
	// watchFlushInterval polls the pending set for due entries.
	watchFlushInterval = 100 * time.Millisecond
	// MaxWatchedContentBytes caps stored instruction-file content at 1MB.
	// Larger files still hash-compare (change is detected), but the diff
	// degrades to a hash-change notice since the "before" text is dropped.
	MaxWatchedContentBytes = 1 << 20
)

// FileTypeForPath maps a workspace-relative path to its instruction file
// type (pure). Matching is case-insensitive with slash normalization so
// Windows edits ("claude.md", backslash separators) resolve the same way.
func FileTypeForPath(p string) (string, bool) {
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
	for _, spec := range InstructionFiles {
		if norm == strings.ToLower(spec.RelPath) {
			return spec.FileType, true
		}
	}
	return "", false
}

// HashBytes returns the hex SHA256 of b (pure). This is the watched_files
// last_hash value.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashString returns the hex SHA256 of s (pure).
func HashString(s string) string { return HashBytes([]byte(s)) }

// DetectInstructionChange is the pure change-detection core: hash the new
// content, compare against oldHash, and render the diff against oldContent.
// oldContent may be nil (unknown "before" text — e.g. over the store cap),
// in which case a change still reports with a hash-change notice instead of
// a line diff. No filesystem or network access.
func DetectInstructionChange(oldHash string, oldContent, newContent []byte) (newHash string, changed bool, diff string) {
	newHash = HashBytes(newContent)
	if newHash == oldHash {
		return newHash, false, ""
	}
	if oldContent == nil {
		if oldHash == "" {
			// New file: diff from empty.
			return newHash, true, DiffLines("", string(newContent))
		}
		return newHash, true, "(content not retained; hash changed " + shortHash(oldHash) + " -> " + shortHash(newHash) + ")"
	}
	return newHash, true, DiffLines(string(oldContent), string(newContent))
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// InstructionChangedPayload builds the INSTRUCTION_FILE_CHANGED payload
// (pure). oldHash is "" for newly created files; deleted is true with an
// empty newHash and size 0 for removals.
func InstructionChangedPayload(relPath, fileType, oldHash, newHash, diff string, size int64, deleted bool) map[string]any {
	return map[string]any{
		"path":      relPath,
		"file_type": fileType,
		"old_hash":  oldHash,
		"new_hash":  newHash,
		"diff":      diff,
		"size":      size,
		"deleted":   deleted,
	}
}

// Watcher tracks instruction-file hashes under a workspace root and emits
// INSTRUCTION_FILE_CHANGED through the interceptor on every verified
// change. Use NewWatcher + Run + Close; PollOnce is the deterministic
// single-pass check.
type Watcher struct {
	root       string
	sink       EventSink
	watcher    *fsnotify.Watcher
	debounce   time.Duration
	maxContent int64

	mu          sync.Mutex
	lastHash    map[string]string // rel path -> SHA256 hex ("" = never seen)
	lastContent map[string][]byte // rel path -> last content (nil when over cap)
	pending     map[string]time.Time
	done        chan struct{}
	closeOnce   sync.Once
}

// NewWatcher snapshots the current instruction-file hashes under root and
// starts fsnotify watches on the root and any existing watched parent dirs
// (e.g. .github). Missing parent dirs are picked up when created (the root
// watch sees the mkdir and adds them). It emits nothing itself.
func NewWatcher(root string, sink EventSink) (*Watcher, error) {
	canon, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root:        canon,
		sink:        sink,
		watcher:     fw,
		debounce:    DefaultDebounce,
		maxContent:  MaxWatchedContentBytes,
		lastHash:    map[string]string{},
		lastContent: map[string][]byte{},
		pending:     map[string]time.Time{},
		done:        make(chan struct{}),
	}
	if err := fw.Add(canon); err != nil {
		_ = fw.Close()
		return nil, err
	}
	// Watch existing parent dirs of nested specs; absent ones are added
	// lazily when the root watch observes their creation.
	for _, dir := range watchParentDirs(canon) {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			_ = fw.Add(dir) // best-effort: PollOnce covers gaps
		}
	}
	w.snapshot()
	return w, nil
}

// watchParentDirs returns the distinct existing-or-future parent dirs of
// nested watch specs (e.g. <root>/.github), excluding the root itself.
func watchParentDirs(root string) []string {
	seen := map[string]bool{}
	var out []string
	for _, spec := range InstructionFiles {
		dir := filepath.Join(root, filepath.FromSlash(spec.RelPath))
		dir = filepath.Dir(dir)
		if dir == root || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

// snapshot records current hashes/content without emitting.
func (w *Watcher) snapshot() {
	for _, spec := range InstructionFiles {
		data, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(spec.RelPath)))
		if err != nil {
			continue
		}
		w.lastHash[spec.RelPath] = HashBytes(data)
		w.lastContent[spec.RelPath] = retainContent(data, w.maxContent)
	}
}

func retainContent(data []byte, max int64) []byte {
	if int64(len(data)) > max {
		return nil
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp
}

// Close stops the watcher. Idempotent.
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.watcher.Close()
	})
	return err
}

// Run processes fsnotify events until ctx is done or Close is called.
// Changed files are debounced, re-read, hash-compared, and emitted via the
// sink (nil-sink safe through the interceptor).
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(watchFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.onFSEvent(ev)
		case <-w.watcher.Errors:
			// Ignore and carry on; PollOnce remains the gap fallback.
		case now := <-t.C:
			w.flushDue(now)
		}
	}
}

// onFSEvent routes one fsnotify event: directory creation adds watches for
// future nested specs; events on watched files mark them dirty.
func (w *Watcher) onFSEvent(ev fsnotify.Event) {
	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	// A created directory may be a watched parent (e.g. .github).
	if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
		if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
			for _, dir := range watchParentDirs(w.root) {
				abs := filepath.ToSlash(dir)
				evSlash := filepath.ToSlash(filepath.Clean(ev.Name))
				if abs == evSlash {
					_ = w.watcher.Add(dir) // best-effort
				}
			}
			return
		}
	}
	if _, ok := FileTypeForPath(rel); !ok {
		return
	}
	// Canonicalize to the spec's RelPath spelling.
	for _, spec := range InstructionFiles {
		if strings.EqualFold(filepath.ToSlash(spec.RelPath), rel) {
			rel = spec.RelPath
			break
		}
	}
	w.mu.Lock()
	w.pending[rel] = time.Now()
	w.mu.Unlock()
}

// flushDue processes pending paths quiet for at least the debounce.
func (w *Watcher) flushDue(now time.Time) {
	w.mu.Lock()
	var due []string
	for rel, at := range w.pending {
		if now.Sub(at) >= w.debounce {
			due = append(due, rel)
			delete(w.pending, rel)
		}
	}
	w.mu.Unlock()
	for _, rel := range due {
		w.checkPath(rel)
	}
}

// PollOnce synchronously re-checks every watched file and emits one
// INSTRUCTION_FILE_CHANGED per verified change. Deterministic (no timers),
// so it is the hook tests use and the fallback where fsnotify is lossy.
func (w *Watcher) PollOnce() {
	for _, spec := range InstructionFiles {
		w.checkPath(spec.RelPath)
	}
}

// checkPath reads one watched file, hash-compares, updates state, and
// emits on change. Removals emit with deleted=true.
func (w *Watcher) checkPath(rel string) {
	fileType, ok := FileTypeForPath(rel)
	if !ok {
		return
	}
	data, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			w.emitRemoval(rel, fileType)
		}
		return
	}
	w.mu.Lock()
	oldHash := w.lastHash[rel]
	oldContent := w.lastContent[rel]
	newHash, changed, diff := DetectInstructionChange(oldHash, oldContent, data)
	var payload map[string]any
	if changed {
		w.lastHash[rel] = newHash
		w.lastContent[rel] = retainContent(data, w.maxContent)
		payload = InstructionChangedPayload(rel, fileType, oldHash, newHash, diff, int64(len(data)), false)
	}
	w.mu.Unlock()
	if payload != nil {
		NewInterceptor(w.sink).Emit(EventInstructionFileChanged, payload)
	}
}

// emitRemoval records a deleted instruction file and emits its event with
// the full-removal diff when the old content was retained.
func (w *Watcher) emitRemoval(rel, fileType string) {
	w.mu.Lock()
	oldHash, seen := w.lastHash[rel]
	if !seen || oldHash == "" {
		w.mu.Unlock()
		return // never existed: not a change
	}
	oldContent := w.lastContent[rel]
	var diff string
	if oldContent != nil {
		diff = DiffLines(string(oldContent), "")
	} else {
		diff = "(content not retained; file deleted, was " + shortHash(oldHash) + ")"
	}
	delete(w.lastHash, rel)
	delete(w.lastContent, rel)
	payload := InstructionChangedPayload(rel, fileType, oldHash, "", diff, 0, true)
	w.mu.Unlock()
	NewInterceptor(w.sink).Emit(EventInstructionFileChanged, payload)
}
