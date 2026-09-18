package steering

import (
	"errors"
	"testing"
)

func TestAuditSteeringEventsAndConstants(t *testing.T) {
	for _, ev := range []string{EventInterruptRequested, EventSteerPrompt, EventPaused, EventResumed} {
		if !IsSteeringEvent(ev) {
			t.Errorf("IsSteeringEvent(%q) = false", ev)
		}
	}
	if IsSteeringEvent("NOPE") || IsSteeringEvent("") {
		t.Error("unknown events must return false")
	}
	if WSMsgAction != "action" || WSMsgEvent != "event" {
		t.Errorf("envelope constants = %q/%q, want action/event", WSMsgAction, WSMsgEvent)
	}
}

func TestAuditStateMachineHappyPath(t *testing.T) {
	m := NewInterruptManager()
	if err := m.Register("run-1", nil); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := m.State("run-1"); got != StateRunning {
		t.Fatalf("initial state = %q, want running", got)
	}
	if err := m.Gate("run-1"); err != nil {
		t.Fatalf("Gate while running must be nil: %v", err)
	}
	br := &RecordingBridge{}
	m2 := NewInterruptManager()
	if err := m2.Register("r", br); err != nil {
		t.Fatal(err)
	}
	if err := m2.RequestInterrupt("r", "alice", "stop"); err != nil {
		t.Fatalf("RequestInterrupt: %v", err)
	}
	if br.Count() != 1 {
		t.Errorf("daemon bridge signaled %d times, want 1", br.Count())
	}
	if got := m2.State("r"); got != StatePauseRequested {
		t.Fatalf("after interrupt = %q, want pause_requested", got)
	}
	if err := m2.Gate("r"); !errors.Is(err, ErrPaused) {
		t.Fatalf("Gate while pause_requested = %v, want ErrPaused", err)
	}
	// Zero-latency steer before ack is legal.
	if err := m2.Steer("r", "alice", "prompt-1"); err != nil {
		t.Fatalf("Steer pre-ack: %v", err)
	}
	if err := m2.AcknowledgePaused("r"); err != nil {
		t.Fatalf("AcknowledgePaused: %v", err)
	}
	if got := m2.State("r"); got != StatePaused {
		t.Fatalf("after ack = %q, want paused", got)
	}
	if err := m2.Steer("r", "alice", "prompt-2"); err != nil {
		t.Fatalf("Steer while paused: %v", err)
	}
	if d := m2.QueueDepth("r"); d != 2 {
		t.Fatalf("queue depth = %d, want 2", d)
	}
	// FIFO order.
	p1, ok := m2.TakeNextPrompt("r")
	if !ok || p1.Prompt != "prompt-1" {
		t.Fatalf("FIFO first = %+v ok=%v", p1, ok)
	}
	p2, ok := m2.TakeNextPrompt("r")
	if !ok || p2.Prompt != "prompt-2" {
		t.Fatalf("FIFO second = %+v ok=%v", p2, ok)
	}
	if _, ok := m2.TakeNextPrompt("r"); ok {
		t.Fatal("empty queue must return ok=false")
	}
	// Done/Context re-arm on resume.
	doneBefore := m2.Done("r")
	select {
	case <-doneBefore:
	default:
		t.Error("Done must be closed after interrupt")
	}
	if m2.Context("r").Err() == nil {
		t.Error("Context must be cancelled after interrupt")
	}
	if err := m2.Resume("r", "alice"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := m2.State("r"); got != StateRunning {
		t.Fatalf("after resume = %q, want running", got)
	}
	select {
	case <-m2.Done("r"):
		t.Error("Done must be open (re-armed) after resume")
	default:
	}
	if m2.Context("r").Err() != nil {
		t.Error("Context must be re-armed after resume")
	}
	if err := m2.Gate("r"); err != nil {
		t.Fatalf("Gate after resume: %v", err)
	}
	evs := m2.Events("r")
	want := []string{EventInterruptRequested, EventSteerPrompt, EventPaused, EventSteerPrompt, EventResumed}
	if len(evs) != len(want) {
		t.Fatalf("events = %d, want %d: %+v", len(evs), len(want), evs)
	}
	for i, w := range want {
		if evs[i].EventType != w {
			t.Errorf("event[%d] = %q, want %q", i, evs[i].EventType, w)
		}
	}
	if err := m2.Unregister("r"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if got := m2.State("r"); got != StateFinished {
		t.Errorf("after unregister = %q, want finished", got)
	}
}

func TestAuditSingleSteererLock(t *testing.T) {
	m := NewInterruptManager()
	if err := m.Register("r", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RequestInterrupt("r", "alice", ""); err != nil {
		t.Fatal(err)
	}
	// Different steerer rejected everywhere while held.
	if err := m.Steer("r", "bob", "x"); !errors.Is(err, ErrSteerConflict) {
		t.Errorf("Steer by bob = %v, want ErrSteerConflict", err)
	}
	if err := m.Resume("r", "bob"); !errors.Is(err, ErrSteerConflict) {
		t.Errorf("Resume by bob = %v, want ErrSteerConflict", err)
	}
	if err := m.RequestInterrupt("r", "bob", ""); !errors.Is(err, ErrSteerConflict) {
		t.Errorf("second interrupt by bob = %v, want ErrSteerConflict", err)
	}
	// Same owner may queue multiple prompts.
	if err := m.Steer("r", "alice", "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Steer("r", "alice", "b"); err != nil {
		t.Fatal(err)
	}
	if d := m.QueueDepth("r"); d != 2 {
		t.Errorf("same-owner prompts must queue: depth=%d", d)
	}
}

func TestAuditInvalidTransitions(t *testing.T) {
	m := NewInterruptManager()
	if err := m.Register("r", nil); err != nil {
		t.Fatal(err)
	}
	// Steer/Resume/Ack while running are illegal.
	if err := m.Steer("r", "alice", "x"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Steer while running = %v, want ErrInvalidState", err)
	}
	if err := m.Resume("r", "alice"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Resume while running = %v, want ErrInvalidState", err)
	}
	if err := m.AcknowledgePaused("r"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Ack while running = %v, want ErrInvalidState", err)
	}
	if err := m.Steer("r", "alice", "   "); !errors.Is(err, ErrEmptyPrompt) {
		t.Errorf("blank prompt = %v, want ErrEmptyPrompt", err)
	}
	// Unknown runs.
	if err := m.RequestInterrupt("ghost", "a", ""); !errors.Is(err, ErrNoRun) {
		t.Errorf("unknown interrupt = %v", err)
	}
	if err := m.Unregister("ghost"); !errors.Is(err, ErrNoRun) {
		t.Errorf("unknown unregister = %v", err)
	}
	if err := m.Gate("ghost"); !errors.Is(err, ErrNoRun) {
		t.Errorf("unknown gate = %v", err)
	}
	if m.Done("ghost") != nil || m.Context("ghost") != nil || m.State("ghost") != "" || m.QueueDepth("ghost") != 0 || m.Events("ghost") != nil {
		t.Error("unknown-run accessors must be nil/empty")
	}
	// Validation.
	if err := m.Register("  ", nil); err == nil {
		t.Error("blank run_id must be rejected")
	}
	if err := m.Register("r2", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Register("r2", nil); err == nil {
		t.Error("double register of live run must fail")
	}
	if err := m.RequestInterrupt("r2", "  ", ""); err == nil {
		t.Error("blank steerer must be rejected")
	}
	// Bridge error propagates; state unchanged.
	m3 := NewInterruptManager()
	_ = m3.Register("rb", FuncBridge{Fn: func(string) error { return errors.New("sigint boom") }})
	if err := m3.RequestInterrupt("rb", "a", ""); err == nil {
		t.Error("bridge error must propagate")
	}
	if got := m3.State("rb"); got != StateRunning {
		t.Errorf("failed signal must leave running, got %q", got)
	}
	// Nil-fn FuncBridge + NoopBridge are silent.
	_ = NewInterruptManager()
	if err := (FuncBridge{}).Signal("x"); err != nil {
		t.Errorf("nil FuncBridge: %v", err)
	}
	if err := (NoopBridge{}).Signal("x"); err != nil {
		t.Errorf("NoopBridge: %v", err)
	}
}
