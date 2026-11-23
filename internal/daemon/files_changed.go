package daemon

// files_changed.go reports changed workspace files to the server
// (POST /v1/projects/{id}/files-changed) so memories whose files_affected
// match can be marked stale. A small fsnotify watcher (project_watcher.go)
// feeds Note; FilesChangedReporter batches, filters and posts. Each report
// carries the files' sha256 content hashes (text files under 2 MB) so the
// server can tell a real change from a save of the same content; servers
// that reject the hashes field get reports without it.

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/scan"
)

const (
	filesChangedFlushEvery = 30 * time.Second
	filesChangedMaxBatch   = 500
	filesChangedBackoff    = time.Hour
	filesChangedErrLogGap  = 10 * time.Minute
)

// changedSkipDirs are path segments whose contents are never reported:
// VCS metadata, dependency/build output, IDE state, and agent transcript /
// tool state directories (including Nexus' own .central-memory).
var changedSkipDirs = map[string]bool{
	// VCS
	".git": true, ".hg": true, ".svn": true,
	// dependencies / build output (harvesterSkipDirs + scan.DriveIgnore)
	"node_modules": true, "dist": true, "build": true, "out": true,
	".next": true, "__pycache__": true, ".venv": true, "venv": true,
	"target": true, "bin": true, "obj": true, "coverage": true,
	".gradle": true, ".terraform": true, ".tox": true, ".mypy_cache": true,
	".pytest_cache": true, ".cache": true, ".turbo": true, ".parcel-cache": true,
	// IDE state
	".idea": true, ".vscode": true, ".vs": true,
	// agent transcript / tool state
	tokenDirName: true, ".claude": true, ".codex": true, ".cursor": true,
	".gemini": true, ".opencode": true, ".antigravity": true, ".copilot": true,
	".windsurf": true, ".grok": true, ".kimi": true, ".kimi-code": true,
	".codeium": true, ".commandcode": true, ".cagent": true, ".zcode": true,
	".deepseek-cli": true, ".continue": true, ".aider.tags.cache.v3": true,
	".nexus": true,
}

// changedSkipFile reports whether a base name is noise or sensitive
// (scan.DriveIgnore file patterns plus editor/temp files).
func changedSkipFile(base string) bool {
	lower := strings.ToLower(base)
	switch {
	case lower == ".ds_store", lower == "thumbs.db", lower == "4913":
		return true
	case strings.HasPrefix(lower, ".env"):
		return true
	case strings.HasPrefix(lower, ".aider"):
		return true
	case strings.HasPrefix(lower, ".#"), strings.HasPrefix(lower, "~$"):
		return true
	case strings.HasSuffix(lower, "~"):
		return true
	case strings.HasPrefix(lower, "#") && strings.HasSuffix(lower, "#"):
		return true
	case strings.Contains(lower, ".nexus-") && strings.HasSuffix(lower, ".tmp"):
		return true // agentfiles atomic-write temp files
	}
	for _, ext := range []string{".pyc", ".log", ".pem", ".key", ".swp", ".swo", ".swx", ".tmp", ".temp", ".crdownload", ".lock~"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// changedRelPath turns an observed path into a workspace-relative,
// forward-slash path, or ok=false when it is outside root or ignored.
func changedRelPath(root, p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || root == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	segs := strings.Split(rel, "/")
	for _, seg := range segs[:len(segs)-1] {
		if changedSkipDirs[seg] || changedSkipDirs[strings.ToLower(seg)] {
			return "", false
		}
	}
	base := segs[len(segs)-1]
	if changedSkipDirs[base] || changedSkipFile(base) || scan.ShouldSkip(rel) {
		return "", false
	}
	return path.Clean(rel), true
}

// FilesChangedReporter batches changed workspace files and posts them to
// the server. Note never blocks: it takes a short mutex and at most signals
// a buffered channel. Run does the network I/O.
type FilesChangedReporter struct {
	root string
	conn func() (base, token, projectID string, client *http.Client, err error)

	FlushEvery      time.Duration
	MaxBatch        int
	NotFoundBackoff time.Duration
	now             func() time.Time

	kick chan struct{}

	mu           sync.Mutex
	pending      map[string]struct{}
	order        []string
	firstAt      time.Time
	backoffUntil time.Time
	suppress     map[string]time.Time
	dropped      int64
	sent         int64
	lastErrLog   time.Time
	// noHashes is set when the server rejected the hashes field (older
	// servers decode strictly); later reports omit it.
	noHashes bool
}

// NewFilesChangedReporter builds a reporter for root. conn supplies the
// cloud connection (base, token, project id, client) at flush time.
func NewFilesChangedReporter(root string, conn func() (base, token, projectID string, client *http.Client, err error)) *FilesChangedReporter {
	return &FilesChangedReporter{
		root:            root,
		conn:            conn,
		FlushEvery:      filesChangedFlushEvery,
		MaxBatch:        filesChangedMaxBatch,
		NotFoundBackoff: filesChangedBackoff,
		now:             time.Now,
		kick:            make(chan struct{}, 1),
		pending:         map[string]struct{}{},
		suppress:        map[string]time.Time{},
	}
}

func (f *FilesChangedReporter) maxBatch() int {
	if f.MaxBatch <= 0 {
		return filesChangedMaxBatch
	}
	return f.MaxBatch
}

// Note records one changed path (absolute or workspace-relative). Ignored
// and out-of-root paths are dropped. Never blocks on I/O.
func (f *FilesChangedReporter) Note(p string) {
	if f == nil {
		return
	}
	rel, ok := changedRelPath(f.root, p)
	if !ok {
		return
	}
	now := f.now()
	f.mu.Lock()
	if now.Before(f.backoffUntil) {
		f.mu.Unlock()
		return
	}
	if until, ok := f.suppress[strings.ToLower(rel)]; ok {
		if now.Before(until) {
			f.mu.Unlock()
			return
		}
		delete(f.suppress, strings.ToLower(rel))
	}
	if _, dup := f.pending[rel]; !dup {
		if len(f.pending) >= 4*f.maxBatch() {
			f.dropped++
			f.mu.Unlock()
			return
		}
		f.pending[rel] = struct{}{}
		f.order = append(f.order, rel)
		if len(f.order) == 1 {
			f.firstAt = now
		}
	}
	full := len(f.pending) >= f.maxBatch()
	f.mu.Unlock()
	if full {
		select {
		case f.kick <- struct{}{}:
		default:
		}
	}
}

// Suppress ignores changes to rel (workspace-relative) for d — used for
// files Nexus itself writes (agent instruction files).
func (f *FilesChangedReporter) Suppress(rel string, d time.Duration) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.suppress[strings.ToLower(filepath.ToSlash(rel))] = f.now().Add(d)
	f.mu.Unlock()
}

// Pending returns how many paths await a flush.
func (f *FilesChangedReporter) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

// Run flushes every FlushEvery and whenever MaxBatch paths are pending,
// until ctx ends (a final best-effort flush runs on shutdown).
func (f *FilesChangedReporter) Run(ctx context.Context) {
	every := f.FlushEvery
	if every <= 0 {
		every = filesChangedFlushEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			f.Flush(fctx)
			cancel()
			return
		case <-t.C:
			f.Flush(ctx)
		case <-f.kick:
			f.Flush(ctx)
		}
	}
}

// Flush posts pending paths in batches of at most MaxBatch. It returns the
// number of paths the server accepted.
func (f *FilesChangedReporter) Flush(ctx context.Context) int {
	if f == nil {
		return 0
	}
	sent := 0
	for ctx.Err() == nil {
		n, more := f.flushOnce(ctx)
		sent += n
		if !more {
			break
		}
	}
	return sent
}

// flushOnce posts one batch; more=true when paths remain after a success.
func (f *FilesChangedReporter) flushOnce(ctx context.Context) (int, bool) {
	now := f.now()
	f.mu.Lock()
	if now.Before(f.backoffUntil) {
		f.pending = map[string]struct{}{}
		f.order = nil
		f.mu.Unlock()
		return 0, false
	}
	if len(f.order) == 0 {
		f.mu.Unlock()
		return 0, false
	}
	n := len(f.order)
	if n > f.maxBatch() {
		n = f.maxBatch()
	}
	batch := append([]string(nil), f.order[:n]...)
	f.order = append([]string(nil), f.order[n:]...)
	for _, p := range batch {
		delete(f.pending, p)
	}
	// "at" is the earliest change in the batch: only memories older than
	// every change are marked stale (false negatives over false positives).
	// Remaining paths keep firstAt as a conservative lower bound.
	at := f.firstAt
	f.mu.Unlock()

	base, token, projectID, client, err := f.conn()
	if err != nil || !looksLikeUUID(projectID) {
		// Not connected yet (or project not resolved): keep the batch.
		f.requeue(batch, at)
		return 0, false
	}
	endpoint := base + "/v1/projects/" + url.PathEscape(projectID) + "/files-changed"
	body := map[string]any{"paths": batch, "at": at.UTC().Format(time.RFC3339)}
	f.mu.Lock()
	withHashes := !f.noHashes
	f.mu.Unlock()
	sentHashes := false
	if withHashes {
		if hashes := workspaceHashes(f.root, batch); len(hashes) > 0 {
			body["hashes"] = hashes
			sentHashes = true
		}
	}
	status, err := mirrorJSON(ctx, client, http.MethodPost, endpoint, token, body, nil)
	switch {
	case err != nil && status == http.StatusBadRequest && sentHashes:
		// An older server rejects the unknown hashes field: resend plain.
		f.mu.Lock()
		f.noHashes = true
		f.mu.Unlock()
		f.requeue(batch, at)
		return 0, true
	case err == nil:
		f.mu.Lock()
		f.sent += int64(len(batch))
		more := len(f.order) > 0
		f.mu.Unlock()
		return len(batch), more
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		f.enterBackoff("server has no files-changed route yet")
		return 0, false
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		f.enterBackoff("token lacks memory:write for files-changed")
		return 0, false
	default:
		f.requeue(batch, at)
		f.mu.Lock()
		logIt := f.now().Sub(f.lastErrLog) >= filesChangedErrLogGap
		if logIt {
			f.lastErrLog = f.now()
		}
		f.mu.Unlock()
		if logIt {
			log.Printf("daemon: files-changed report failed (will retry): %v", err)
		}
		return 0, false
	}
}

func (f *FilesChangedReporter) requeue(batch []string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keep []string
	for _, p := range batch {
		if _, dup := f.pending[p]; dup {
			continue
		}
		if len(f.pending)+len(keep) >= 4*f.maxBatch() {
			f.dropped++
			continue
		}
		keep = append(keep, p)
	}
	for _, p := range keep {
		f.pending[p] = struct{}{}
	}
	f.order = append(keep, f.order...)
	if !at.IsZero() && (f.firstAt.IsZero() || at.Before(f.firstAt)) {
		f.firstAt = at
	}
}

func (f *FilesChangedReporter) enterBackoff(reason string) {
	d := f.NotFoundBackoff
	if d <= 0 {
		d = filesChangedBackoff
	}
	f.mu.Lock()
	first := f.backoffUntil.IsZero()
	f.backoffUntil = f.now().Add(d)
	f.pending = map[string]struct{}{}
	f.order = nil
	f.mu.Unlock()
	if first {
		log.Printf("daemon: files-changed reporting paused for %s (%s)", d, reason)
	}
}

// BackoffUntil reports when reporting resumes (zero when not backing off).
func (f *FilesChangedReporter) BackoffUntil() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.backoffUntil
}
