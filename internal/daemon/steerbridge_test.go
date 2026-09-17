package daemon

// Tests for the steer runner bridge (issue #42): run registry, out-of-band
// Signal, nil-safety, and the InterruptManager gate helper. All in-process,
// no daemon required.

import (
	"context"
	"testing"

	"central-memory/internal/steering"
)

func TestSteerBridgeSignalFiresCancel(t *testing.T) {
	b := NewSteerBridge()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := false
	b.TrackRun("run-1", func() { fired = true; cancel() })
	if err := b.Signal("run-1"); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if !fired {
		t.Fatal("Signal must fire the registered cancel")
	}
}

func TestSteerBridgeUnknownRunErrors(t *testing.T) {
	b := NewSteerBridge()
	if err := b.Signal("ghost"); err == nil {
		t.Fatal("unknown run: want error")
	}
	b.UntrackRun("ghost") // idempotent no-op
}

func TestSteerBridgeNilSafe(t *testing.T) {
	var b *SteerBridge
	b.TrackRun("r", func() {})
	b.UntrackRun("r")
	if err := b.Signal("r"); err != nil {
		t.Fatalf("nil bridge Signal must succeed, got %v", err)
	}
	if hold, p := GateBeforeToolCall(nil, "r"); hold || p != nil {
		t.Fatalf("nil manager gate = (%v, %v), want (false, nil)", hold, p)
	}
}

func TestGateBeforeToolCall(t *testing.T) {
	mgr := steering.NewInterruptManager()
	if hold, p := GateBeforeToolCall(mgr, "run-1"); hold || p != nil {
		t.Fatalf("unregistered run gate = (%v, %v), want (false, nil)", hold, p)
	}
}
