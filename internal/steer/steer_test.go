package steer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a mutex-guarded Emitter capturing event order for assertions.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) Emit(eventType string, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, eventType)
}

func (r *recorder) ordered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func newTestController(rec *recorder, bridge RunnerBridge) *Controller {
	var em Emitter
	if rec != nil {
		em = rec
	}
	return NewController(em, bridge)
}

// TestInterruptToPaused walks the core acceptance path: watcher interrupts,
// the run context fires (cancel propagation), the runner acknowledges, and
// the emitted order is INTERRUPT_REQUESTED then PAUSED.
func TestInterruptToPaused(t *testing.T) {
	rec := &recorder{}
	bridgeCalls := 0
	c := newTestController(rec, BridgeFunc(func(ctx context.Context, sessionID, runID string) error {
		bridgeCalls++
		if sessionID != "s1" || runID != "r1" {
			t.Errorf("bridge got session=%q run=%q, want s1/r1", sessionID, runID)
		}
		return nil
	}))

	runCtx, err := c.StartRun(context.Background(), "s1", "r1")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if st, _ := c.State("s1"); st != StateRunning {
		t.Fatalf("state = %q, want running", st)
	}

	if err := c.RequestInterrupt("s1", "bob", "wrong direction"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if st, _ := c.State("s1"); st != StateInterruptRequested {
		t.Fatalf("state = %q, want interrupt_requested", st)
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("run context not cancelled after RequestInterrupt (cancel did not propagate)")
	}
	if bridgeCalls != 1 {
		t.Fatalf("bridge calls = %d, want 1", bridgeCalls)
	}

	if err := c.AcknowledgePaused("s1"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}
	if st, _ := c.State("s1"); st != StatePaused {
		t.Fatalf("state = %q, want paused", st)
	}

	got := rec.ordered()
	want := []string{EventAgentInterruptRequested, EventAgentPaused}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

// TestSteerQueuePriority proves priority injection: a redirect queued while
// paused is handed to the runner at the very next gate (before any tool
// call), consumed exactly once, and the run resumes corrected.
func TestSteerQueuePriority(t *testing.T) {
	rec := &recorder{}
	c := newTestController(rec, nil)

	if _, err := c.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := c.RequestInterrupt("s1", "bob", ""); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if err := c.AcknowledgePaused("s1"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}
	if err := c.EnqueueSteer("s1", "use Redis, not Memcached", "bob"); err != nil {
		t.Fatalf("EnqueueSteer: %v", err)
	}
	if !c.Pending("s1") {
		t.Fatal("Pending = false after EnqueueSteer, want true")
	}

	// First gate: paused AND carrying the redirect — the runner pauses its
	// pending tool call and ingests the steer first.
	gate := c.BeforeToolCall("s1")
	if !gate.Paused {
		t.Error("gate.Paused = false while run is paused, want true")
	}
	if gate.Steer == nil || gate.Steer.Prompt != "use Redis, not Memcached" {
		t.Fatalf("gate.Steer = %+v, want the queued redirect", gate.Steer)
	}

	// Consumed exactly once: the next gate carries nothing.
	again := c.BeforeToolCall("s1")
	if again.Steer != nil {
		t.Errorf("second gate redelivered steer %+v, want nil (single delivery)", again.Steer)
	}

	if err := c.Resume("s1", "bob"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st, _ := c.State("s1"); st != StateRunning {
		t.Fatalf("state = %q, want running after Resume", st)
	}
	live := c.BeforeToolCall("s1")
	if live.Paused || live.Steer != nil {
		t.Errorf("post-resume gate = %+v, want {false nil}", live)
	}

	got := rec.ordered()
	want := []string{EventAgentInterruptRequested, EventAgentPaused, EventAgentSteerPrompt, EventAgentResumed}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

// TestResumeGuards ensures Resume only releases a Paused run: the pause
// handshake cannot be skipped, and a running run cannot be "resumed".
func TestResumeGuards(t *testing.T) {
	c := newTestController(nil, nil)
	if _, err := c.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if err := c.Resume("s1", "bob"); !errors.Is(err, ErrNotPaused) {
		t.Errorf("Resume while running = %v, want ErrNotPaused", err)
	}
	if err := c.RequestInterrupt("s1", "bob", ""); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if err := c.Resume("s1", "bob"); !errors.Is(err, ErrNotPaused) {
		t.Errorf("Resume while interrupt_requested = %v, want ErrNotPaused (ack first)", err)
	}
	if err := c.AcknowledgePaused("s1"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}
	if err := c.Resume("s1", "bob"); err != nil {
		t.Errorf("Resume while paused = %v, want nil", err)
	}
}

// TestConcurrentSteerSingleWinner is the spectator-lock test: 16 watchers
// steer at once and exactly one wins; the rest observe ErrSteerPending.
func TestConcurrentSteerSingleWinner(t *testing.T) {
	c := newTestController(nil, nil)
	if _, err := c.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := c.RequestInterrupt("s1", "bob", ""); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if err := c.AcknowledgePaused("s1"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}

	const racers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	wins := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wins <- c.EnqueueSteer("s1", "redirect", "watcher")
		}()
	}
	close(start)
	wg.Wait()
	close(wins)

	succeeded, conflicted := 0, 0
	for err := range wins {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrSteerPending):
			conflicted++
		default:
			t.Errorf("unexpected steer error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != racers-1 {
		t.Fatalf("steer race: %d winners + %d conflicts, want 1 + %d", succeeded, conflicted, racers-1)
	}
}

// TestConcurrentInterruptSingleWinner applies the same lock to interrupts:
// one winner, every other spectator sees ErrAlreadyInterrupted.
func TestConcurrentInterruptSingleWinner(t *testing.T) {
	c := newTestController(nil, nil)
	if _, err := c.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	const racers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wins := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wins <- c.RequestInterrupt("s1", "watcher", "")
		}()
	}
	close(start)
	wg.Wait()
	close(wins)

	succeeded, conflicted := 0, 0
	for err := range wins {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAlreadyInterrupted):
			conflicted++
		default:
			t.Errorf("unexpected interrupt error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != racers-1 {
		t.Fatalf("interrupt race: %d winners + %d conflicts, want 1 + %d", succeeded, conflicted, racers-1)
	}
}

// TestCancelPropagation verifies both directions: RequestInterrupt cancels
// the run context even with a nil bridge (context-only mode), and parent
// cancellation flows into the run context.
func TestCancelPropagation(t *testing.T) {
	t.Run("interrupt cancels run context without bridge", func(t *testing.T) {
		c := newTestController(nil, nil)
		runCtx, err := c.StartRun(context.Background(), "s1", "r1")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		if err := c.RequestInterrupt("s1", "bob", ""); err != nil {
			t.Fatalf("RequestInterrupt: %v", err)
		}
		select {
		case <-runCtx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("run context not done 2s after interrupt")
		}
	})

	t.Run("parent cancel reaches run context", func(t *testing.T) {
		c := newTestController(nil, nil)
		parent, stop := context.WithCancel(context.Background())
		runCtx, err := c.StartRun(parent, "s1", "r1")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		stop()
		select {
		case <-runCtx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("run context survived parent cancellation")
		}
	})
}

// TestSteerWhileRunning ensures a redirect issued against a running agent
// (no interrupt first) is still held at the next tool-call gate rather
// than applied mid-tool.
func TestSteerWhileRunning(t *testing.T) {
	c := newTestController(nil, nil)
	if _, err := c.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := c.EnqueueSteer("s1", "stop using rm -rf", "alice"); err != nil {
		t.Fatalf("EnqueueSteer while running: %v", err)
	}
	gate := c.BeforeToolCall("s1")
	if gate.Paused {
		t.Error("gate.Paused = true for a running run, want false")
	}
	if gate.Steer == nil || !strings.Contains(gate.Steer.Prompt, "rm -rf") {
		t.Fatalf("gate.Steer = %+v, want the redirect before the tool call", gate.Steer)
	}
}

// TestNoActiveRunErrors covers unknown-session behavior across the API.
func TestNoActiveRunErrors(t *testing.T) {
	c := newTestController(nil, nil)
	if err := c.RequestInterrupt("ghost", "bob", ""); !errors.Is(err, ErrNoActiveRun) {
		t.Errorf("interrupt ghost = %v, want ErrNoActiveRun", err)
	}
	if err := c.AcknowledgePaused("ghost"); !errors.Is(err, ErrNoActiveRun) {
		t.Errorf("ack ghost = %v, want ErrNoActiveRun", err)
	}
	if err := c.EnqueueSteer("ghost", "hi", "bob"); !errors.Is(err, ErrNoActiveRun) {
		t.Errorf("steer ghost = %v, want ErrNoActiveRun", err)
	}
	if err := c.Resume("ghost", "bob"); !errors.Is(err, ErrNoActiveRun) {
		t.Errorf("resume ghost = %v, want ErrNoActiveRun", err)
	}
	if _, ok := c.State("ghost"); ok {
		t.Error("State(ghost) ok = true, want false")
	}
	if gate := c.BeforeToolCall("ghost"); gate.Paused || gate.Steer != nil {
		t.Errorf("ghost gate = %+v, want zero", gate)
	}
}

// TestValidationEdges covers duplicate runs, blank prompts, double pause
// acks, prompt truncation, and EndRun cleanup.
func TestValidationEdges(t *testing.T) {
	c := newTestController(nil, nil)
	if _, err := c.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := c.StartRun(context.Background(), "s1", "r2"); !errors.Is(err, ErrRunActive) {
		t.Errorf("second StartRun = %v, want ErrRunActive", err)
	}
	if err := c.EnqueueSteer("s1", "   ", "bob"); !errors.Is(err, ErrEmptyPrompt) {
		t.Errorf("blank steer = %v, want ErrEmptyPrompt", err)
	}
	if err := c.AcknowledgePaused("s1"); !errors.Is(err, ErrNotAwaitingPause) {
		t.Errorf("ack without interrupt = %v, want ErrNotAwaitingPause", err)
	}

	big := strings.Repeat("x", MaxSteerPromptChars+100)
	if err := c.EnqueueSteer("s1", big, "bob"); err != nil {
		t.Fatalf("EnqueueSteer oversized: %v", err)
	}
	gate := c.BeforeToolCall("s1")
	if gate.Steer == nil || len(gate.Steer.Prompt) > MaxSteerPromptChars+50 {
		t.Fatalf("oversized steer not capped, len=%d", len(gate.Steer.Prompt))
	}
	if !strings.Contains(gate.Steer.Prompt, "[truncated]") {
		t.Error("capped steer missing truncation marker")
	}

	c.EndRun("s1") // idempotent cleanup
	c.EndRun("s1")
	if _, ok := c.State("s1"); ok {
		t.Error("State after EndRun ok = true, want false")
	}
}
