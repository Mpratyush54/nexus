package steering

// Interrupt → pause → steer → resume flow plus conflicting-steerer
// rejection (nexus issue #22). Stdlib only, no sockets or processes: the
// RecordingBridge stands in for the SIGINT / MCP-cancel-token signal.

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func mustRegister(t *testing.T, m *InterruptManager, runID string, b DaemonBridge) {
	t.Helper()
	if err := m.Register(runID, b); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func eventTypes(evs []Event) []string {
	out := make([]string, len(evs), len(evs))
	for i, e := range evs {
		out[i] = e.EventType
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Full happy path: interrupt cancels the pending tool call, daemon acks the
// pause, the steer prompt is injected before the next tool call, resume
// re-arms the run.
func TestInterruptPauseSteerResumeFlow(t *testing.T) {
	m := NewInterruptManager()
	bridge := &RecordingBridge{}
	mustRegister(t, m, "sess1", bridge)

	before := m.Done("sess1")
	select {
	case <-before:
		t.Fatal("Done closed before any interrupt")
	default:
	}

	ctxBefore := m.Context("sess1")
	if ctxBefore == nil || ctxBefore.Err() != nil {
		t.Fatal("run context should start live")
	}

	// 1. Spectator hits Interrupt.
	if err := m.RequestInterrupt("sess1", "alice", "wrong direction"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if got := m.State("sess1"); got != StatePauseRequested {
		t.Fatalf("state = %q, want %q", got, StatePauseRequested)
	}
	if bridge.Count() != 1 {
		t.Fatalf("daemon Signal calls = %d, want 1", bridge.Count())
	}
	// Pending tool call observes cancellation both ways.
	select {
	case <-m.Done("sess1"):
	default:
		t.Fatal("Done should be closed after interrupt")
	}
	if err := m.Context("sess1").Err(); err == nil {
		t.Fatal("run context should be cancelled after interrupt")
	}
	// Gate blocks tool calls while pausing.
	if err := m.Gate("sess1"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Gate = %v, want ErrPaused", err)
	}

	// 2. Daemon acks the pause.
	if err := m.AcknowledgePaused("sess1"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}
	if got := m.State("sess1"); got != StatePaused {
		t.Fatalf("state = %q, want %q", got, StatePaused)
	}

	// 3. Steer injects a high-priority correction.
	const correction = "Stop, don't refactor that database model, use the existing schema"
	if err := m.Steer("sess1", "alice", correction); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if got := m.QueueDepth("sess1"); got != 1 {
		t.Fatalf("queue depth = %d, want 1", got)
	}

	// 4. Agent ingests the prompt BEFORE its next tool call.
	got, ok := m.TakeNextPrompt("sess1")
	if !ok || got.Prompt != correction || got.SteererID != "alice" {
		t.Fatalf("TakeNextPrompt = %+v,%v; want the correction from alice", got, ok)
	}
	if got := m.QueueDepth("sess1"); got != 0 {
		t.Fatalf("queue depth after ingest = %d, want 0", got)
	}

	// 5. Owner resumes; run is re-armed.
	if err := m.Resume("sess1", "alice"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := m.State("sess1"); got != StateRunning {
		t.Fatalf("state = %q, want %q", got, StateRunning)
	}
	if err := m.Gate("sess1"); err != nil {
		t.Fatalf("Gate after resume = %v, want nil", err)
	}
	select {
	case <-m.Done("sess1"):
		t.Fatal("Done should be re-armed (open) after resume")
	default:
	}
	if err := m.Context("sess1").Err(); err != nil {
		t.Fatalf("resumed context should be live, got %v", err)
	}

	// Event ledger matches the WS fan-out order.
	want := []string{EventInterruptRequested, EventPaused, EventSteerPrompt, EventResumed}
	if got := eventTypes(m.Events("sess1")); !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, et := range want {
		if !IsSteeringEvent(et) {
			t.Fatalf("%q should be a steering event", et)
		}
	}
}

// A second spectator must not hijack a held run: interrupt/steer/resume from
// anyone but the owner are rejected, while the owner's own queued prompts
// are accepted in order.
func TestConflictingSteerRejected(t *testing.T) {
	m := NewInterruptManager()
	mustRegister(t, m, "sess9", &RecordingBridge{})

	if err := m.RequestInterrupt("sess9", "alice", "hold on"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if err := m.AcknowledgePaused("sess9"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}

	// Bob tries everything: all rejected with ErrSteerConflict.
	if err := m.Steer("sess9", "bob", "do my thing instead"); !errors.Is(err, ErrSteerConflict) {
		t.Fatalf("bob Steer = %v, want ErrSteerConflict", err)
	}
	if err := m.Resume("sess9", "bob"); !errors.Is(err, ErrSteerConflict) {
		t.Fatalf("bob Resume = %v, want ErrSteerConflict", err)
	}
	if got := m.QueueDepth("sess9"); got != 0 {
		t.Fatalf("bob's rejected steer queued %d prompts, want 0", got)
	}

	// Owner's prompts queue FIFO (priority over normal work, in order).
	if err := m.Steer("sess9", "alice", "first: keep schema"); err != nil {
		t.Fatalf("alice Steer 1: %v", err)
	}
	if err := m.Steer("sess9", "alice", "second: add index"); err != nil {
		t.Fatalf("alice Steer 2: %v", err)
	}
	first, _ := m.TakeNextPrompt("sess9")
	second, _ := m.TakeNextPrompt("sess9")
	if first.Prompt != "first: keep schema" || second.Prompt != "second: add index" {
		t.Fatalf("FIFO order broken: %q then %q", first.Prompt, second.Prompt)
	}

	// Owner resumes; a stray second interrupt on a running run is invalid,
	// and steering a running run is invalid too.
	if err := m.Resume("sess9", "alice"); err != nil {
		t.Fatalf("alice Resume: %v", err)
	}
	if err := m.Resume("sess9", "alice"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("double Resume = %v, want ErrInvalidState", err)
	}
	if err := m.Steer("sess9", "alice", "too late"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Steer while running = %v, want ErrInvalidState", err)
	}
}

// Finished runs are retained only up to MaxCompletedRuns: the
// least-recently-finished run is evicted first, live runs are never
// evicted, and an evicted runID can be re-registered fresh.
func TestCompletedRunEvictionBound(t *testing.T) {
	m := NewInterruptManager()
	total := MaxCompletedRuns + 10
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("run-%03d", i)
		mustRegister(t, m, id, nil)
		if err := m.Unregister(id); err != nil {
			t.Fatalf("Unregister %s: %v", id, err)
		}
	}
	m.mu.Lock()
	retained := len(m.runs)
	m.mu.Unlock()
	if retained != MaxCompletedRuns {
		t.Fatalf("retained finished runs = %d, want %d", retained, MaxCompletedRuns)
	}
	// Oldest-finished evicted (no post-mortem reads), newest retained.
	if evs := m.Events("run-000"); evs != nil {
		t.Fatalf("evicted run-000 should read nil events, got %d", len(evs))
	}
	if got := m.State("run-000"); got != "" {
		t.Fatalf("evicted run-000 state = %q, want unknown", got)
	}
	last := fmt.Sprintf("run-%03d", total-1)
	if got := m.State(last); got != StateFinished {
		t.Fatalf("newest %s state = %q, want %q", last, got, StateFinished)
	}
	// Evicted runID re-registers fresh.
	mustRegister(t, m, "run-000", nil)
	if got := m.State("run-000"); got != StateRunning {
		t.Fatalf("re-registered run-000 state = %q, want %q", got, StateRunning)
	}
	// Live runs are never evicted by later finishes.
	mustRegister(t, m, "live", nil)
	for i := 0; i < MaxCompletedRuns+5; i++ {
		id := fmt.Sprintf("churn-%03d", i)
		mustRegister(t, m, id, nil)
		if err := m.Unregister(id); err != nil {
			t.Fatalf("Unregister %s: %v", id, err)
		}
	}
	if got := m.State("live"); got != StateRunning {
		t.Fatalf("live run state = %q after churn, want %q", got, StateRunning)
	}
	// Post-mortem event reads survive Unregister (within the cap).
	mustRegister(t, m, "pm", nil)
	if err := m.RequestInterrupt("pm", "alice", "x"); err != nil {
		t.Fatalf("RequestInterrupt pm: %v", err)
	}
	if err := m.Unregister("pm"); err != nil {
		t.Fatalf("Unregister pm: %v", err)
	}
	if evs := m.Events("pm"); len(evs) != 1 || evs[0].EventType != EventInterruptRequested {
		t.Fatalf("post-mortem events for pm = %v, want one interrupt", eventTypes(evs))
	}
}

// Per-run event history keeps only the most recent MaxEventsPerRun events.
func TestPerRunHistoryBound(t *testing.T) {
	m := NewInterruptManager()
	mustRegister(t, m, "hist", nil)
	cycles := MaxEventsPerRun/4 + 25 // 4 events per cycle
	for i := 0; i < cycles; i++ {
		if err := m.RequestInterrupt("hist", "alice", "again"); err != nil {
			t.Fatalf("cycle %d RequestInterrupt: %v", i, err)
		}
		if err := m.AcknowledgePaused("hist"); err != nil {
			t.Fatalf("cycle %d AcknowledgePaused: %v", i, err)
		}
		if err := m.Steer("hist", "alice", fmt.Sprintf("prompt %d", i)); err != nil {
			t.Fatalf("cycle %d Steer: %v", i, err)
		}
		if _, ok := m.TakeNextPrompt("hist"); !ok {
			t.Fatalf("cycle %d TakeNextPrompt: queue empty", i)
		}
		if err := m.Resume("hist", "alice"); err != nil {
			t.Fatalf("cycle %d Resume: %v", i, err)
		}
	}
	if got := len(m.Events("hist")); got != MaxEventsPerRun {
		t.Fatalf("history len = %d, want cap %d", got, MaxEventsPerRun)
	}
	// Newest events survive: the tail must hold the last cycle's resume.
	evs := m.Events("hist")
	if evs[len(evs)-1].EventType != EventResumed {
		t.Fatalf("tail event = %q, want %q", evs[len(evs)-1].EventType, EventResumed)
	}
}

// A full prompt queue rejects Steer with ErrQueueFull; draining unblocks it.
func TestQueueFullRejected(t *testing.T) {
	m := NewInterruptManager()
	mustRegister(t, m, "q", nil)
	if err := m.RequestInterrupt("q", "alice", "hold"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if err := m.AcknowledgePaused("q"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}
	for i := 0; i < MaxQueueDepth; i++ {
		if err := m.Steer("q", "alice", fmt.Sprintf("p%d", i)); err != nil {
			t.Fatalf("Steer %d: %v", i, err)
		}
	}
	if err := m.Steer("q", "alice", "one too many"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("over-cap Steer = %v, want ErrQueueFull", err)
	}
	if got := m.QueueDepth("q"); got != MaxQueueDepth {
		t.Fatalf("queue depth = %d, want cap %d", got, MaxQueueDepth)
	}
	// FIFO order intact across the cap, and one drain frees one slot.
	first, ok := m.TakeNextPrompt("q")
	if !ok || first.Prompt != "p0" {
		t.Fatalf("TakeNextPrompt = %+v,%v; want p0", first, ok)
	}
	if err := m.Steer("q", "alice", "fits again"); err != nil {
		t.Fatalf("Steer after drain: %v", err)
	}
	if err := m.Steer("q", "alice", "over again"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("over-cap Steer after refill = %v, want ErrQueueFull", err)
	}
}

// Pop-all stability: full drain yields FIFO order, zero depth, closed pop,
// and releases the backing array (no popped-prefix leak); the run stays
// usable afterwards.
func TestTakeNextPromptPopAllStability(t *testing.T) {
	m := NewInterruptManager()
	mustRegister(t, m, "pop", nil)
	if err := m.RequestInterrupt("pop", "alice", "hold"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	// Zero-latency steering lands before the ack; pause_requested accepts it.
	n := 3*64 + 10 // force several compaction windows past head>=64
	// Steer in batches of MaxQueueDepth, draining between batches.
	pushed := 0
	for pushed < n {
		batch := MaxQueueDepth
		if pushed+batch > n {
			batch = n - pushed
		}
		for i := 0; i < batch; i++ {
			if err := m.Steer("pop", "alice", fmt.Sprintf("m%d", pushed+i)); err != nil {
				t.Fatalf("Steer %d: %v", pushed+i, err)
			}
		}
		pushed += batch
		for i := 0; i < batch; i++ {
			got, ok := m.TakeNextPrompt("pop")
			if !ok {
				t.Fatalf("TakeNextPrompt %d: queue empty", pushed-batch+i)
			}
			if want := fmt.Sprintf("m%d", pushed-batch+i); got.Prompt != want {
				t.Fatalf("pop %d = %q, want %q", pushed-batch+i, got.Prompt, want)
			}
		}
	}
	if got := m.QueueDepth("pop"); got != 0 {
		t.Fatalf("depth after drain = %d, want 0", got)
	}
	if _, ok := m.TakeNextPrompt("pop"); ok {
		t.Fatal("TakeNextPrompt on empty queue should report false")
	}
	m.mu.Lock()
	backing := len(m.runs["pop"].queue)
	m.mu.Unlock()
	if backing != 0 {
		t.Fatalf("backing array len after drain = %d, want 0 (prefix leak)", backing)
	}
	// Run still usable: steer more and pop.
	if err := m.Steer("pop", "alice", "after"); err != nil {
		t.Fatalf("Steer after drain: %v", err)
	}
	got, ok := m.TakeNextPrompt("pop")
	if !ok || got.Prompt != "after" {
		t.Fatalf("TakeNextPrompt after drain = %+v,%v; want after", got, ok)
	}
}

// reentrantBridge calls back into the manager from inside Signal. If Signal
// were invoked while holding mu, these re-entrant calls would deadlock
// (sync.Mutex is not re-entrant). It also records that it ran.
type reentrantBridge struct {
	mu      sync.Mutex
	calls   int
	manager **InterruptManager // set after construction to close the loop
}

func (b *reentrantBridge) Signal(runID string) error {
	b.mu.Lock()
	b.calls++
	mgr := *b.manager
	b.mu.Unlock()
	// Re-enter the manager: any of these deadlock if the manager holds mu
	// across Signal.
	_ = mgr.State(runID)
	_ = mgr.QueueDepth(runID)
	_ = mgr.Events(runID)
	_ = mgr.Gate(runID)
	return nil
}

func (b *reentrantBridge) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// Signal must not be invoked under lock: a bridge that calls back into the
// manager must complete (no deadlock), fast. Run with -race in CI.
func TestSignalNotInvokedUnderLock(t *testing.T) {
	var m *InterruptManager
	bridge := &reentrantBridge{manager: &m}
	m = NewInterruptManager()
	mustRegister(t, m, "re", bridge)

	done := make(chan error, 1)
	go func() { done <- m.RequestInterrupt("re", "alice", "reentrancy probe") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RequestInterrupt with re-entrant bridge: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RequestInterrupt deadlocked: Signal appears to hold Manager.mu")
	}
	if bridge.count() != 1 {
		t.Fatalf("bridge calls = %d, want 1", bridge.count())
	}
	if got := m.State("re"); got != StatePauseRequested {
		t.Fatalf("state = %q, want %q", got, StatePauseRequested)
	}
}

// A failing Signal leaves the run running (reservation rolled back) and a
// retry with a working bridge succeeds.
func TestSignalFailureRollsBack(t *testing.T) {
	m := NewInterruptManager()
	boom := errors.New("boom")
	mustRegister(t, m, "fb", FuncBridge{Fn: func(string) error { return boom }})
	if err := m.RequestInterrupt("fb", "alice", "x"); !errors.Is(err, boom) {
		t.Fatalf("RequestInterrupt = %v, want bridge error", err)
	}
	if got := m.State("fb"); got != StateRunning {
		t.Fatalf("state after failed signal = %q, want %q", got, StateRunning)
	}
	select {
	case <-m.Done("fb"):
		t.Fatal("Done closed despite failed signal")
	default:
	}
	// Retry with a working bridge succeeds.
	m.mu.Lock()
	m.runs["fb"].bridge = NoopBridge{}
	m.mu.Unlock()
	if err := m.RequestInterrupt("fb", "alice", "x"); err != nil {
		t.Fatalf("retry RequestInterrupt: %v", err)
	}
	if got := m.State("fb"); got != StatePauseRequested {
		t.Fatalf("state after retry = %q, want %q", got, StatePauseRequested)
	}
}

// Steering an idle (running, uninterrupted) run is meaningless: Steer and
// Resume without a prior interrupt are rejected, and blank prompts never
// queue.
func TestSteerWithoutInterruptRejected(t *testing.T) {
	m := NewInterruptManager()
	mustRegister(t, m, "sess7", nil)

	if err := m.Steer("sess7", "alice", "redirect!"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Steer while running = %v, want ErrInvalidState", err)
	}
	if err := m.Resume("sess7", "alice"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Resume while running = %v, want ErrInvalidState", err)
	}
	if err := m.RequestInterrupt("sess7", "alice", "x"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if err := m.Steer("sess7", "alice", "   "); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("blank Steer = %v, want ErrEmptyPrompt", err)
	}
	if err := m.Steer("sess7", "carol", "hijack"); !errors.Is(err, ErrSteerConflict) {
		t.Fatalf("other-steerer Steer while pause_requested = %v, want ErrSteerConflict", err)
	}
}
