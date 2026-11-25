package daemon

// SharedScanner is the single, machine-wide transcript reader.
//
// Before it existed, every approved workspace ran its own Harvester, and
// every Harvester walked all agent folders every 30 seconds, re-read the same
// transcripts with its own cursors and re-scanned the same IDE databases. With
// a handful of workspaces that multiplied disk and CPU load until the machine
// stalled. The shared scanner instead:
//
//   - reads each transcript once, no matter how many workspaces are approved;
//   - wakes on OS file-change notifications (fsnotify), with a full reconcile
//     walk only every few minutes to catch anything a notification missed;
//   - decides once per transcript which workspace owns it (cached) and never
//     reads transcripts that belong to no approved workspace;
//   - persists read cursors, so restarting the app does not re-read history;
//   - caps read throughput while importing history so the disk stays usable.
//
// Workspace runtimes attach their Harvester. The attached Harvester keeps its
// emitter, pause state, status counters and OpenCode extractor; the scanner
// does the file I/O and hands each event to the owning workspace(s).

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"central-memory/internal/platform"
)

const (
	sharedReconcileInterval = 5 * time.Minute
	sharedPollFallback      = time.Minute // when file notifications are unavailable
	sharedIdleInterval      = 30 * time.Second
	sharedDebounce          = 750 * time.Millisecond
	sharedSQLiteDebounce    = 30 * time.Second
	sharedOpenCodeDebounce  = 15 * time.Second
	sharedMaxWatches        = 4000
	// sharedWatchBuffer is the change-notification buffer per watched
	// folder. On Windows fsnotify allocates its default 64 KiB for every
	// watched directory, and agent transcript trees have thousands of
	// folders (every Claude chat has its own subagents/ folder): 4000
	// watches held 250 MiB of heap for good, the live heap seen in real
	// use. Transcript folders see a handful of appends at a time; an
	// overflow is reported as an error and answered with a full
	// reconcile, so nothing is missed.
	sharedWatchBuffer = 4096
	// routePeekBytes is how far attribution may look into a transcript. A
	// negative decision for a smaller file is re-evaluated once it grows.
	routePeekBytes = 512 << 10
	// sharedReadBytesPerSecond caps history import throughput.
	sharedReadBytesPerSecond = 32 << 20
)

type routeEntry struct {
	owners []*Harvester
	size   int64
	gen    uint64
	seen   uint64 // reconcile sequence that last saw the file
}

// SharedScanner reads agent transcripts once for all attached workspaces.
type SharedScanner struct {
	mu        sync.Mutex
	engine    *Harvester
	attached  []*Harvester
	gen       uint64
	scanSeq   uint64
	routes    map[string]*routeEntry
	turns     map[*Harvester]int
	statePath string
	saved     uint64
	cancel    context.CancelFunc
	done      chan struct{}
	kick      chan struct{}
	lastOC    time.Time
	lastSave  time.Time
	// rwatch is the Windows recursive watcher while run is active.
	rwatch *recursiveWatcher
	// sourceList overrides ResolveSources (tests).
	sourceList []TranscriptSource
}

func (s *SharedScanner) sources() []TranscriptSource {
	if s.sourceList != nil {
		return s.sourceList
	}
	return ResolveSources()
}

var (
	defaultSharedOnce sync.Once
	defaultShared     *SharedScanner
)

// DefaultSharedScanner returns the process-wide scanner used by the desktop
// core. Its cursor file lives in the Nexus config directory.
func DefaultSharedScanner() *SharedScanner {
	defaultSharedOnce.Do(func() {
		path := ""
		if dir, err := platform.ConfigDir(); err == nil {
			path = filepath.Join(dir, "capture-cursors.json")
		}
		defaultShared = NewSharedScanner(path)
	})
	return defaultShared
}

// NewSharedScanner builds a scanner. statePath ("" = no persistence) stores
// read cursors across restarts.
func NewSharedScanner(statePath string) *SharedScanner {
	return &SharedScanner{
		routes:    map[string]*routeEntry{},
		turns:     map[*Harvester]int{},
		statePath: statePath,
		kick:      make(chan struct{}, 1),
	}
}

// Attach registers a workspace harvester and starts the scanner if needed.
func (s *SharedScanner) Attach(h *Harvester) {
	if s == nil || h == nil {
		return
	}
	s.mu.Lock()
	for _, existing := range s.attached {
		if existing == h {
			s.mu.Unlock()
			return
		}
	}
	s.attached = append(s.attached, h)
	s.gen++
	start := s.cancel == nil
	if start {
		s.engine = s.newEngine()
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel, s.done = cancel, make(chan struct{})
		go s.run(ctx, s.engine, s.done)
	}
	s.mu.Unlock()
	if !start {
		s.ReconcileNow()
	}
}

// Detach removes a workspace harvester; the scanner stops with the last one.
func (s *SharedScanner) Detach(h *Harvester) {
	if s == nil || h == nil {
		return
	}
	s.mu.Lock()
	kept := s.attached[:0]
	for _, existing := range s.attached {
		if existing != h {
			kept = append(kept, existing)
		}
	}
	s.attached = kept
	s.gen++
	delete(s.turns, h)
	var cancel context.CancelFunc
	var done chan struct{}
	if len(s.attached) == 0 && s.cancel != nil {
		cancel, done = s.cancel, s.done
		s.cancel, s.done = nil, nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Attached reports whether h is currently fed by this scanner.
func (s *SharedScanner) Attached(h *Harvester) bool {
	if s == nil || h == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.attached {
		if existing == h {
			return true
		}
	}
	return false
}

// ReconcileNow asks the running scanner for an immediate full pass.
func (s *SharedScanner) ReconcileNow() {
	if s == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *SharedScanner) newEngine() *Harvester {
	e := NewHarvesterWithPoll("", s, sharedReconcileInterval, 0)
	var sources []TranscriptSource
	for _, src := range s.sources() {
		// OpenCode's global database is read per workspace by each attached
		// harvester's own SQL extractor (it filters rows by project folder).
		if src.Agent == "opencode" && src.Format == FormatSQLite {
			continue
		}
		sources = append(sources, src)
	}
	e.Sources = sources
	e.routeFilter = s.route
	// A first import of history is paced by the upload pipeline (the engine
	// waits while each workspace uploads its turns), so one pass can outlast
	// an app session. Save cursors during the pass, not only after it, or
	// every restart starts the import over.
	e.readThrottle = func(n int) {
		throttleReads(n)
		s.maybeSaveCursors(e)
	}
	if saved := s.loadCursors(); len(saved) > 0 {
		e.restoreCursors(saved)
	}
	return e
}

// throttleReads sleeps in proportion to bytes read, capping import
// throughput. Ordinary appends (a few KB) cost nothing noticeable.
func throttleReads(n int) {
	if n < 256<<10 {
		return
	}
	time.Sleep(time.Duration(float64(n) / float64(sharedReadBytesPerSecond) * float64(time.Second)))
}

// route is the engine's attribution hook: true when at least one attached,
// unpaused workspace owns path. Decisions are cached per file.
func (s *SharedScanner) route(path string, src TranscriptSource) bool {
	var size int64
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	s.mu.Lock()
	entry := s.routes[path]
	gen, seq := s.gen, s.scanSeq
	attached := append([]*Harvester(nil), s.attached...)
	s.mu.Unlock()

	fresh := entry != nil && entry.gen == gen &&
		(len(entry.owners) > 0 || entry.size >= routePeekBytes || entry.size == size)
	if !fresh {
		var owners []*Harvester
		for _, h := range attached {
			if src.Global || h.MatchesTranscript(path, src.CwdMatch) {
				owners = append(owners, h)
			}
		}
		entry = &routeEntry{owners: owners, size: size, gen: gen}
	}
	entry.seen = seq
	s.mu.Lock()
	s.routes[path] = entry
	s.mu.Unlock()
	for _, h := range entry.owners {
		if !h.IsPaused() {
			return true
		}
	}
	return false
}

// Emit implements EventEmitter for the engine: deliver each event to the
// workspaces that own its transcript.
func (s *SharedScanner) Emit(ev Event) {
	path, _ := ev.Payload["path"].(string)
	s.mu.Lock()
	entry := s.routes[path]
	var owners []*Harvester
	if entry != nil {
		owners = append(owners, entry.owners...)
	}
	for _, h := range owners {
		if ev.Type == EventConversationTurn && !h.IsPaused() {
			s.turns[h]++
		}
	}
	s.mu.Unlock()
	for _, h := range owners {
		if !h.IsPaused() {
			h.emit(ev)
		}
	}
}

func (s *SharedScanner) run(ctx context.Context, engine *Harvester, done chan struct{}) {
	defer close(done)
	defer s.saveCursors(engine, true)

	// Windows: one recursive watch per source folder (recursive_watch_*.go).
	// Elsewhere: fsnotify, folder by folder.
	var watcher *fsnotify.Watcher
	var rw *recursiveWatcher
	if recursiveWatchSupported {
		rw, _ = newRecursiveWatcher()
	}
	if rw != nil {
		defer rw.Close()
	} else if w, err := fsnotify.NewWatcher(); err != nil {
		log.Printf("capture: file notifications unavailable, polling every %s: %v", sharedPollFallback, err)
	} else {
		watcher = w
		defer watcher.Close()
	}
	s.rwatch = rw
	watched := map[string]bool{}
	s.reconcile(engine, watcher, watched)

	interval := sharedReconcileInterval
	if watcher == nil && rw == nil {
		interval = sharedPollFallback
	}
	reconcileT := time.NewTicker(interval)
	defer reconcileT.Stop()
	idleT := time.NewTicker(sharedIdleInterval)
	defer idleT.Stop()
	flushT := time.NewTicker(250 * time.Millisecond)
	defer flushT.Stop()

	pending := map[string]time.Time{} // path -> earliest time to process
	lastSQLite := map[string]time.Time{}
	var events <-chan fsnotify.Event
	var errs <-chan error
	if watcher != nil {
		events, errs = watcher.Events, watcher.Errors
	} else if rw != nil {
		events, errs = rw.Events(), rw.Errors()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			s.reconcile(engine, watcher, watched)
		case <-reconcileT.C:
			s.reconcile(engine, watcher, watched)
		case <-idleT.C:
			engine.CheckIdle()
			for _, h := range s.attachedSnapshot() {
				h.CheckIdle()
			}
			s.saveCursors(engine, false)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			// Typically a notification buffer overflow: catch up by walking.
			log.Printf("capture: file notification error, reconciling: %v", err)
			s.ReconcileNow()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					if watcher != nil {
						s.watchTree(watcher, watched, ev.Name, 4)
					}
					continue
				}
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			path := sqliteMainFile(ev.Name)
			due := time.Now().Add(sharedDebounce)
			if isSQLiteFile(path) {
				if next := lastSQLite[path].Add(sharedSQLiteDebounce); next.After(due) {
					due = next
				}
			}
			if prev, ok := pending[path]; !ok || due.Before(prev) {
				pending[path] = due
			}
		case now := <-flushT.C:
			for path, due := range pending {
				if now.Before(due) {
					continue
				}
				delete(pending, path)
				if isSQLiteFile(path) {
					lastSQLite[path] = now
				}
				s.handleFile(engine, path)
			}
		}
	}
}

func (s *SharedScanner) attachedSnapshot() []*Harvester {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Harvester(nil), s.attached...)
}

// handleFile processes one changed file with the same rules as the walk.
func (s *SharedScanner) handleFile(engine *Harvester, path string) {
	if strings.EqualFold(filepath.Base(path), "opencode.db") {
		s.trackOpenCode(false)
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	for _, src := range engine.Sources {
		if !sourceContains(src, path) {
			continue
		}
		skipped := false
		for _, dir := range src.Dirs {
			if sourceContains(TranscriptSource{Dirs: []string{dir}}, path) && inSkippedDirUnder(dir, path) {
				skipped = true
			}
		}
		if !skipped {
			engine.considerFile(src, path, info)
		}
	}
}

// trackOpenCode lets each attached workspace read its own rows from the
// global OpenCode database, at most once per debounce window.
func (s *SharedScanner) trackOpenCode(force bool) {
	s.mu.Lock()
	if !force && time.Since(s.lastOC) < sharedOpenCodeDebounce {
		s.mu.Unlock()
		return
	}
	s.lastOC = time.Now()
	attached := append([]*Harvester(nil), s.attached...)
	s.mu.Unlock()
	for _, src := range s.sources() {
		if src.Agent != "opencode" || src.Format != FormatSQLite {
			continue
		}
		for _, dir := range src.Dirs {
			db := filepath.Join(dir, "opencode.db")
			st, err := os.Stat(db)
			if err != nil || st.IsDir() {
				continue
			}
			for _, h := range attached {
				if !h.IsPaused() {
					h.considerFile(src, db, st)
				}
			}
		}
	}
}

// reconcile walks every source once, refreshes notification watches, prunes
// routes for vanished files and publishes per-workspace status.
func (s *SharedScanner) reconcile(engine *Harvester, watcher *fsnotify.Watcher, watched map[string]bool) {
	s.mu.Lock()
	s.scanSeq++
	seq := s.scanSeq
	s.mu.Unlock()

	_, _, scanErr := engine.ScanAndTailCounted()
	s.trackOpenCode(true)
	if watcher != nil {
		for _, src := range s.sources() {
			for _, dir := range src.Dirs {
				s.watchTree(watcher, watched, dir, maxWalkDepth)
			}
		}
	} else if s.rwatch != nil {
		// One watch per existing source folder (a folder created later is
		// picked up by the next reconcile).
		for _, src := range s.sources() {
			for _, dir := range src.Dirs {
				if st, err := os.Stat(dir); err == nil && st.IsDir() {
					_ = s.rwatch.AddRoot(dir)
				}
			}
		}
	}

	errText := ""
	if scanErr != nil {
		errText = scanErr.Error()
	}
	s.mu.Lock()
	for path, entry := range s.routes {
		if entry.seen != seq {
			if _, err := os.Stat(path); err != nil {
				delete(s.routes, path)
			}
		}
	}
	s.mu.Unlock()
	s.publish(engine, errText, time.Now().UTC())
	s.saveCursors(engine, false)
}

// publish gives each attached workspace its view of the routed transcripts.
// at is the completed-pass time; zero means "still importing" and keeps the
// previous completion time, so status can show progress during a long first
// import without claiming it finished.
func (s *SharedScanner) publish(engine *Harvester, errText string, at time.Time) {
	s.mu.Lock()
	type view struct {
		files  int
		agents map[string]int
		hits   []HarvestFileHit
	}
	views := map[*Harvester]*view{}
	for _, h := range s.attached {
		views[h] = &view{agents: map[string]int{}}
	}
	engine.mu.Lock()
	for path, entry := range s.routes {
		agent := engine.agents[path]
		for _, h := range entry.owners {
			v := views[h]
			if v == nil {
				continue
			}
			v.files++
			v.agents[agent]++
			if len(v.hits) < 40 {
				v.hits = append(v.hits, HarvestFileHit{Agent: agent, Path: path, Name: filepath.Base(path)})
			}
		}
	}
	engine.mu.Unlock()
	turns := s.turns
	s.turns = map[*Harvester]int{}
	s.mu.Unlock()

	for h, v := range views {
		// OpenCode rows are counted by the attached harvester itself.
		h.setSharedScanStats(at, v.files, turns[h], v.agents, v.hits, errText)
	}
}

// watchTree adds notification watches for dir and its subdirectories, with
// the same skip rules and depth bound as the walk.
func (s *SharedScanner) watchTree(w *fsnotify.Watcher, watched map[string]bool, dir string, depth int) {
	if w == nil || dir == "" {
		return
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return
	}
	var walk func(string, int)
	walk = func(d string, left int) {
		if len(watched) >= sharedMaxWatches {
			return
		}
		key := strings.ToLower(filepath.Clean(d))
		if !watched[key] {
			if err := w.AddWith(d, fsnotify.WithBufferSize(sharedWatchBuffer)); err != nil {
				return
			}
			watched[key] = true
		}
		if left <= 0 {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && !harvesterSkipDirs[e.Name()] {
				walk(filepath.Join(d, e.Name()), left-1)
			}
		}
	}
	walk(dir, depth)
}

type cursorFile struct {
	Version int              `json:"version"`
	Cursors map[string]int64 `json:"cursors"`
}

func (s *SharedScanner) loadCursors() map[string]int64 {
	if s.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return nil
	}
	var f cursorFile
	if json.Unmarshal(data, &f) != nil || f.Version != 1 {
		return nil
	}
	return f.Cursors
}

// cursorSaveInterval bounds how much import progress a restart can lose.
var cursorSaveInterval = 15 * time.Second

// maybeSaveCursors persists cursors at most once per cursorSaveInterval.
func (s *SharedScanner) maybeSaveCursors(engine *Harvester) {
	s.mu.Lock()
	due := time.Since(s.lastSave) >= cursorSaveInterval
	if due {
		s.lastSave = time.Now()
	}
	s.mu.Unlock()
	if due {
		s.saveCursors(engine, false)
		s.publish(engine, "", time.Time{}) // progress during a long import
	}
}

// saveCursors writes cursors atomically when they changed (or force).
func (s *SharedScanner) saveCursors(engine *Harvester, force bool) {
	if s.statePath == "" || engine == nil {
		return
	}
	cursors, version := engine.cursorSnapshot()
	s.mu.Lock()
	unchanged := version == s.saved
	s.mu.Unlock()
	if unchanged && !force {
		return
	}
	data, err := json.Marshal(cursorFile{Version: 1, Cursors: cursors})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o700); err != nil {
		return
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		_ = os.Remove(tmp)
		return
	}
	s.mu.Lock()
	s.saved = version
	s.mu.Unlock()
}

// sqliteMainFile maps WAL/SHM/journal sidecars to their database file.
func sqliteMainFile(path string) string {
	lower := strings.ToLower(path)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if strings.HasSuffix(lower, suffix) {
			return path[:len(path)-len(suffix)]
		}
	}
	return path
}

func isSQLiteFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3", ".vscdb":
		return true
	}
	return false
}

// sourceContains reports whether path lies under one of src's directories.
func sourceContains(src TranscriptSource, path string) bool {
	clean := strings.ToLower(filepath.Clean(path))
	for _, dir := range src.Dirs {
		d := strings.ToLower(filepath.Clean(dir))
		if d != "" && strings.HasPrefix(clean, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// inSkippedDirUnder reports a path inside a folder the transcript walk
// skips (node_modules, .git, build output, agent scratch folders), counting
// only folders below dir, or deeper than the walk goes. A recursive watch
// reports those too.
func inSkippedDirUnder(dir, p string) bool {
	rel, err := filepath.Rel(dir, filepath.Dir(p))
	if err != nil || rel == "." {
		return false
	}
	segs := strings.Split(rel, string(filepath.Separator))
	if len(segs) > maxWalkDepth {
		return true // deeper than the walk goes
	}
	for _, seg := range segs {
		if harvesterSkipDirs[seg] {
			return true
		}
	}
	return false
}
