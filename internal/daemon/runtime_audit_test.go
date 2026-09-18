package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Audit tests for runtime.go wiring (issues #99/#115/#81, daemon side):
// interceptor-as-emitter seam, harvester/watcher/processor construction,
// fail-closed designation sync, Start/Stop lifecycle, SQLite fallback
// extraction, and the JSONL -> CONVERSATION_TURN -> PROPOSED chain.

func TestAuditInterceptorImplementsToolEmitter(t *testing.T) {
	// Compile-time seam: *Interceptor must satisfy ToolEventEmitter so the
	// Watcher can feed instruction-file changes into the Layer-1 queue.
	var _ ToolEventEmitter = NewInterceptor(4, nil)
	d, err := NewDaemon(t.TempDir(), "test-token-rt")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	w := NewWatcherWithStore(d.Root, nil, time.Second, d.Interceptor, "", nil)
	if w == nil {
		t.Fatal("NewWatcherWithStore with interceptor emitter returned nil")
	}
	// A watcher change event emitted via the interceptor must land in the
	// interceptor queue (nil-downstream mode).
	d.Interceptor.Emit(ToolEvent{Type: ToolEventFileRead, Payload: map[string]any{"path": "x"}})
	select {
	case ev := <-d.Interceptor.Events():
		if ev.Type != ToolEventFileRead {
			t.Fatalf("type = %s, want FILE_READ", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interceptor Emit did not enqueue")
	}
	var nilIn *Interceptor
	nilIn.Emit(ToolEvent{Type: ToolEventFileRead}) // nil-safe, must not panic
}

func TestAuditRuntimeWiring(t *testing.T) {
	if NewRuntime(nil, "p", nil) != nil {
		t.Error("NewRuntime(nil daemon) = non-nil, want nil")
	}
	d, err := NewDaemon(t.TempDir(), "test-token-rt")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	r := NewRuntime(d, "proj-a", nil)
	if r == nil {
		t.Fatal("NewRuntime returned nil")
	}
	if r.Harvester == nil || r.Watcher == nil || r.Processor == nil {
		t.Fatal("pipeline has nil Harvester/Watcher/Processor")
	}
	if r.Designation == nil || r.Designation.IsDesignated() {
		t.Error("nil designation must be fail-closed static false")
	}
	if len(r.Watcher.targets) != len(ExtendedWatchedTargets()) {
		t.Errorf("watcher targets = %d, want extended %d", len(r.Watcher.targets), len(ExtendedWatchedTargets()))
	}
	if r.Processor.Provider == nil {
		t.Error("processor provider is nil")
	}
	if r.harvestCh == nil {
		t.Fatal("harvest channel is nil")
	}
	// Harvester sink wired: an emitted Event must reach the runtime channel.
	r.Harvester.emit(Event{Type: EventConversationTurn})
	select {
	case ev := <-r.harvestCh:
		if ev.Type != EventConversationTurn {
			t.Fatalf("harvest type = %s, want CONVERSATION_TURN", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("harvester emitter not wired to runtime channel")
	}
}

func TestAuditRuntimeDesignationSync(t *testing.T) {
	d, err := NewDaemon(t.TempDir(), "test-token-rt")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	sd := NewStaticDesignation(false)
	r := NewRuntime(d, "proj-a", sd)
	if r.Processor.Designated {
		t.Fatal("processor starts designated, want fail-closed false")
	}
	r.SyncDesignation() // still false: no transition, no panic
	if r.Processor.Designated {
		t.Fatal("SyncDesignated flipped without provider change")
	}
	sd.Set(true)
	r.SyncDesignation()
	if !r.Processor.Designated {
		t.Fatal("SyncDesignation did not follow provider to true")
	}
	var nilR *Runtime
	nilR.SyncDesignation() // nil-safe
	if got := nilR.Dropped(); got != 0 {
		t.Fatalf("nil runtime Dropped = %d, want 0", got)
	}
}

func TestAuditRuntimeStartStopLifecycle(t *testing.T) {
	d, err := NewDaemon(t.TempDir(), "test-token-rt")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	r := NewRuntime(d, "proj-a", NewStaticDesignation(true))
	r.DesignationPoll = 10 * time.Millisecond
	r.DroppedLogEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Start(ctx) }()
	// Let harvester/watcher/tickers tick at least once, then stop.
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Runtime.Start did not return after cancel")
	}
	var nilR *Runtime
	nilR.Start(ctx) // nil-safe, must not panic
}

func TestAuditRuntimeEpisodeEndToEnd(t *testing.T) {
	// The exact window Runtime.Start flushes into ProcessToolEvents: a full
	// fail->read->fix->green arc must yield one episode_summary proposal.
	d, err := NewDaemon(t.TempDir(), "test-token-rt")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	r := NewRuntime(d, "proj-a", NewStaticDesignation(true))
	r.SyncDesignation()
	arc := []ToolEvent{
		{Type: ToolEventCommandExecuted, Payload: map[string]any{"command": "go", "args": []string{"test"}, "exit_code": 1, "output": "FAIL: TestFoo panic nil dereference"}},
		{Type: ToolEventFileRead, Payload: map[string]any{"path": "foo.go"}},
		{Type: ToolEventFileModified, Payload: map[string]any{"path": "foo.go", "diff": "- nil\n+ guard"}},
		{Type: ToolEventCommandExecuted, Payload: map[string]any{"command": "go", "args": []string{"test"}, "exit_code": 0, "output": "ok"}},
		{Type: ToolEventGitCommitted, Payload: map[string]any{"commit": "abc123", "message": "fix nil guard"}},
	}
	got, err := r.Processor.ProcessToolEvents(context.Background(), "proj-a", arc)
	if err != nil {
		t.Fatalf("ProcessToolEvents: %v", err)
	}
	if len(got) != 1 || got[0].Scope != ScopeEpisodeSummary {
		t.Fatalf("got %v, want one episode_summary proposal", got)
	}
	// Fail-closed: a non-designated runtime proposes nothing.
	r2 := NewRuntime(d, "proj-a", NewStaticDesignation(false))
	r2.SyncDesignation()
	if out, _ := r2.Processor.ProcessToolEvents(context.Background(), "proj-a", arc); len(out) != 0 {
		t.Fatalf("non-designated runtime proposed %d, want 0", len(out))
	}
}

func TestAuditJSONLToProposedChain(t *testing.T) {
	// #115 smoke in-process: JSONL conversation -> CONVERSATION_TURN event
	// -> PROPOSED memory via the designated processor.
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	writeLines(t, p,
		`{"role":"user","content":"I prefer verbose error messages with full stack traces for debugging"}`,
		`{"role":"assistant","content":"Noted, I will always include full stack traces in every error response"}`,
	)
	var evs []Event
	h := NewHarvester(dir, sliceEmitter{&evs})
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatalf("TailFile: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("no turns parsed from JSONL")
	}
	if len(evs) == 0 || evs[0].Type != EventConversationTurn {
		t.Fatalf("evs = %v, want CONVERSATION_TURN events", evs)
	}
	proc := NewProcessor(NewInMemoryStore(nil), 0, 0, true, HeuristicProvider{})
	props, err := proc.ProcessEvents(context.Background(), "proj-a", evs)
	if err != nil {
		t.Fatalf("ProcessEvents: %v", err)
	}
	if len(props) == 0 {
		t.Fatal("no proposals from conversation turns")
	}
}

func TestAuditHarvesterSetEmitter(t *testing.T) {
	var h *Harvester
	h.SetEmitter(nil) // nil-receiver safe
	h = NewHarvester(t.TempDir(), nil)
	var evs []Event
	h.SetEmitter(sliceEmitter{&evs})
	h.emit(Event{Type: EventSessionComplete})
	if len(evs) != 1 || evs[0].Type != EventSessionComplete {
		t.Fatalf("evs = %v, want one SESSION_TRANSCRIPT_COMPLETE", evs)
	}
	h.SetEmitter(nil)
	h.emit(Event{Type: EventSessionComplete})
	if len(evs) != 1 {
		t.Fatal("nil emitter still forwarded events")
	}
}

func TestAuditSQLiteFallbackExtractsTurns(t *testing.T) {
	// Synthetic Cursor/VSCode-style storage payload: role + long content.
	p := filepath.Join(t.TempDir(), "state.vscdb")
	blob := `garbage-prefix {"role":"user","content":"We decided to use Redis over Memcached because pubsub support wins for our queue"} garbage-suffix`
	if err := os.WriteFile(p, []byte(blob), 0o644); err != nil {
		t.Fatal(err)
	}
	turns := extractSQLiteFallback(p, time.Now().Add(-time.Hour))
	if len(turns) == 0 {
		t.Fatal("fallback extracted no turns from chat-like payload")
	}
	if !strings.Contains(turns[0].Content, "Redis") {
		t.Fatalf("turn content = %q, want Redis decision", turns[0].Content)
	}
	// Opaque fixture (no spaces / short blobs) stays liveness-only: nil.
	opaque := filepath.Join(t.TempDir(), "opaque.vscdb")
	if err := os.WriteFile(opaque, []byte("sqlite-format-data-more"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := extractSQLiteFallback(opaque, time.Now()); len(got) != 0 {
		t.Fatalf("opaque fixture yielded %d turns, want liveness-only nil", len(got))
	}
}
