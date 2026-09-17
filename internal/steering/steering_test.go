package steering

// Interrupt → pause → steer → resume flow plus conflicting-steerer
// rejection (nexus issue #22). Stdlib only, no sockets or processes: the
// RecordingBridge stands in for the SIGINT / MCP-cancel-token signal.

import (
	"errors"
	"testing"
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
