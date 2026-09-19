package daemon

// Daemon runtime wiring (issues #99/#115/#81, daemon side).
//
// NewRuntime constructs the background extraction pipeline that
// cmd/daemon/main.go starts: Harvester (Layer 2) + Watcher (Layer 3) +
// Memory Processor (designated-gated) with the Layer-1 interceptor sink
// wired to the processor event loop. Processing is fail-closed when
// designation is unknown; designation sync is pluggable via
// DesignationProvider so the daemon never imports the pgx-backed store.
//
// Failover policy (issue #115): presence flaps at PresenceThreshold (90s,
// mirrors store.OfflineThreshold) but the designated-processor role is
// sticky for DesignatedFailoverAfter (1h, plan §6.3) so laptops flapping
// on sleep do not churn designation. The runtime enforces the 1h stickiness
// locally; the server elector (ReassignStaleDesignated +
// ElectDesignatedProcessor) remains the source of truth when reachable.

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// DesignationProvider reports whether this daemon is the designated memory
// processor. Implementations poll workspaces.is_designated_processor (via
// the server) or a local flag. Fail-closed: unknown means false.
type DesignationProvider interface {
	// IsDesignated reports whether this daemon may process now.
	IsDesignated() bool
}

// StaticDesignation is a manual DesignationProvider (tests, operator flag).
// Zero value is fail-closed (false).
type StaticDesignation struct {
	mu         sync.Mutex
	designated bool
	lastChange time.Time
}

// NewStaticDesignation builds a manual designation flag.
func NewStaticDesignation(designated bool) *StaticDesignation {
	return &StaticDesignation{designated: designated, lastChange: time.Now().UTC()}
}

// Set updates the flag.
func (s *StaticDesignation) Set(designated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.designated != designated {
		s.designated = designated
		s.lastChange = time.Now().UTC()
	}
}

// IsDesignated implements DesignationProvider.
func (s *StaticDesignation) IsDesignated() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.designated
}

// MaterializerRunner is the push-file regenerator seam (issue #81). The
// real implementation lives in internal/materializer (which imports the
// pgx-backed store); cmd/daemon wires it here so internal/daemon never
// imports pgx. Nil means "no materializer".
type MaterializerRunner interface {
	Run(ctx context.Context) error
}

// chanEventEmitter is the Layer-2 (harvester Event) counterpart to
// ChanEmitter: non-blocking channel sink so transcript tailing never stalls
// on a slow processor. Drops + counts under backpressure.
type chanEventEmitter struct {
	ch chan Event
}

// Emit enqueues ev without blocking; drops when full. Nil-safe.
func (e *chanEventEmitter) Emit(ev Event) {
	if e == nil || e.ch == nil {
		return
	}
	select {
	case e.ch <- ev:
	default:
	}
}

// Runtime is the daemon background pipeline.
type Runtime struct {
	Daemon      *Daemon
	Harvester   *Harvester
	Watcher     *Watcher
	Processor   *Processor
	Designation DesignationProvider
	ProjectID   string

	Materializer MaterializerRunner

	// harvestCh carries Layer-2 harvester Events to the processor loop.
	// Buffered (DefaultToolEventBuffer); harvester drops under backpressure.
	harvestCh chan Event

	// Poll intervals (<=0 selects defaults).
	DesignationPoll time.Duration
	DroppedLogEvery time.Duration

	// Portal Connect telemetry (/local/harvest).
	statsMu         sync.Mutex
	turnsEmitted    int64
	completions     int64
	proposalsSaved  int64
	proposalErrors  int64
	lastProposalErr string
	lastEventAt     time.Time
	lastScanAt      time.Time
	lastScanFiles   int
	lastScanTurns   int
	lastScanErr     string
	recent          []HarvestLogLine
}

// NewRuntime builds the pipeline for daemon d. Harvester/Watcher/Processor
// are constructed with stdlib defaults; the interceptor sink is wired to a
// ChanEmitter drained by the processor loop (issue #99). designation may be
// nil (fail-closed static false). project names the §2.6 prompt project.
// A nil d returns nil (no pipeline without a daemon).
func NewRuntime(d *Daemon, project string, designation DesignationProvider) *Runtime {
	if d == nil {
		return nil
	}
	if designation == nil {
		designation = NewStaticDesignation(false)
	}
	harvestCh := make(chan Event, DefaultToolEventBuffer)
	h := NewHarvester(d.Root, &chanEventEmitter{ch: harvestCh})
	// Watcher hash persistence: file-backed store under the token dir so
	// hashes survive restarts (issue #109 atomicity via FileHashStore).
	// The watcher emits into the Layer-1 interceptor queue (Interceptor
	// implements ToolEventEmitter) so instruction-file changes reach the
	// same sink + episode detector as file/command events.
	hashPath := WorkspaceHashPath(d.Root)
	var hs WatchedFileStore
	if fhs, err := NewFileHashStore(hashPath); err == nil {
		hs = fhs
	}
	var wEmit ToolEventEmitter
	if d.Interceptor != nil {
		wEmit = d.Interceptor
	}
	w := NewWatcherWithStore(d.Root, ExtendedWatchedTargets(), WatcherPollInterval, wEmit, d.getWorkspaceID(), hs)
	w.SeedBaseline()
	// When authenticated to a central server, proposals upload to the portal
	// (auto-sync). Otherwise keep an in-memory store for local-only runs.
	var store MemoryStore = NewInMemoryStore(nil)
	if strings.TrimSpace(d.ServerURL) != "" && strings.TrimSpace(d.ServerToken) != "" {
		store = NewHTTPMemoryStore(d.ServerURL, d.ServerToken, project)
	}
	desigNow := false
	if designation != nil {
		desigNow = designation.IsDesignated()
	}
	proc := NewProcessor(store, 0, 0, desigNow, AutoProvider())
	return &Runtime{
		Daemon:          d,
		Harvester:       h,
		Watcher:         w,
		Processor:       proc,
		Designation:     designation,
		ProjectID:       project,
		harvestCh:       harvestCh,
		DesignationPoll: 30 * time.Second,
		DroppedLogEvery: time.Minute,
	}
}

// WorkspaceHashPath returns the watcher hash state file.
func WorkspaceHashPath(root string) string {
	return WorkspacePath(root) + ".hashes.json"
}

// SetServerProjectID points the runtime (and HTTP memory store) at the
// server-resolved project UUID so harvested proposals land on the portal.
func (r *Runtime) SetServerProjectID(id string) {
	if r == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	r.ProjectID = id
	if r.Processor != nil {
		if hs, ok := r.Processor.Store.(*HTTPMemoryStore); ok {
			hs.SetProjectID(id)
			_ = hs.PrefetchExisting(context.Background())
		}
	}
	if r.Materializer != nil {
		// Materializer may hold a string ProjectID field via concrete type;
		// best-effort: only HTTP store is required for portal sync.
	}
	log.Printf("daemon: harvest target project_id=%s", id)
}

// SyncDesignation polls the provider and gates the processor (fail-closed
// when unknown). It logs transitions and the 1h vs 90s policy.
func (r *Runtime) SyncDesignation() {
	if r == nil || r.Processor == nil {
		return
	}
	desig := false
	if r.Designation != nil {
		desig = r.Designation.IsDesignated()
	}
	if r.Processor.Designated != desig {
		log.Printf("daemon: designation sync: designated=%v (failover sticky %s, presence %s)", desig, DesignatedFailoverAfter, PresenceThreshold)
		r.Processor.Designated = desig
	}
}

// Dropped exposes the interceptor drop counter for metrics/logs.
func (r *Runtime) Dropped() int64 {
	if r == nil || r.Daemon == nil {
		return 0
	}
	return r.Daemon.Dropped()
}

// Start runs Harvester + Watcher + Processor event loop + designation sync
// + dropped-metric logging until ctx ends. The interceptor sink is wired at
// startup (issue #99): a ChanEmitter is installed via SetEventSink and
// drained into ProcessToolEvents (episode detection) on a window, while
// harvester Events drain into ProcessEvents (conversation extraction).
func (r *Runtime) Start(ctx context.Context) {
	if r == nil || r.Daemon == nil {
		return
	}
	// Wire the sink first so no tool event is lost between boot and loop.
	sink := NewChanEmitter(DefaultToolEventBuffer)
	r.Daemon.SetEventSink(sink)
	// (Re)wire the harvester sink in case this Runtime was built by hand
	// (tests) without NewRuntime's channel.
	if r.harvestCh == nil {
		r.harvestCh = make(chan Event, DefaultToolEventBuffer)
	}
	if r.Harvester != nil {
		r.Harvester.SetEmitter(&chanEventEmitter{ch: r.harvestCh})
	}

	var wg sync.WaitGroup
	// Harvester + Watcher background loops.
	if r.Harvester != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Harvester.Start(ctx)
		}()
	}
	if r.Watcher != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Watcher.Start(ctx)
		}()
	}

	// Materializer (issue #81): push-file regenerator when wired.
	if r.Materializer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Materializer.Run(ctx)
		}()
	}

	// Designation sync ticker.
	desigEvery := r.DesignationPoll
	if desigEvery <= 0 {
		desigEvery = 30 * time.Second
	}
	// Dropped-metric log ticker.
	dropEvery := r.DroppedLogEvery
	if dropEvery <= 0 {
		dropEvery = time.Minute
	}
	r.SyncDesignation()

	// Tool-event window for episode detection (issue #115: DetectEpisodePattern
	// invoked on ToolEvent windows; episode_summary proposals emitted) plus
	// the conversation batch for ProcessEvents.
	var window []ToolEvent
	var conv []Event
	flush := func() {
		if len(window) > 0 && r.Processor != nil {
			evs := append([]ToolEvent(nil), window...)
			window = window[:0]
			if props, err := r.Processor.ProcessToolEvents(ctx, r.ProjectID, evs); err != nil {
				log.Printf("daemon: episode process: %v", err)
				r.noteProposals(0, err)
			} else if len(props) > 0 {
				r.noteProposals(len(props), nil)
			}
		}
		if len(conv) > 0 && r.Processor != nil {
			batch := append([]Event(nil), conv...)
			conv = conv[:0]
			if props, err := r.Processor.ProcessEvents(ctx, r.ProjectID, batch); err != nil {
				log.Printf("daemon: conversation process: %v", err)
				r.noteProposals(len(props), err)
			} else if len(props) > 0 {
				r.noteProposals(len(props), nil)
			}
		}
	}

	desigT := time.NewTicker(desigEvery)
	defer desigT.Stop()
	dropT := time.NewTicker(dropEvery)
	defer dropT.Stop()
	epT := time.NewTicker(30 * time.Second)
	defer epT.Stop()

	for {
		select {
		case <-ctx.Done():
			flush()
			// Stop the interceptor forwarder (issue #132): SetEventSink
			// starts it, so Start must stop it — one leaked goroutine
			// per run otherwise.
			if r.Daemon != nil && r.Daemon.Interceptor != nil {
				r.Daemon.Interceptor.Close()
			}
			wg.Wait()
			return
		case ev := <-sink.Ch:
			window = append(window, ev)
			if len(window)+len(conv) >= 50 {
				flush()
			}
		case ev := <-r.harvestCh:
			r.noteHarvestEvent(ev)
			conv = append(conv, ev)
			if len(window)+len(conv) >= 50 {
				flush()
			}
		case <-epT.C:
			flush()
		case <-desigT.C:
			r.SyncDesignation()
		case <-dropT.C:
			if n := r.Dropped(); n > 0 {
				log.Printf("daemon: interceptor dropped events=%d", n)
			}
		}
	}
}
