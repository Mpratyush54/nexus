// Tests for the steer emitter bridge (issue #42): persistence + live
// fan-out through MemStore and Hub. All in-process, no sockets.
package server

import (
	"context"
	"testing"

	"central-memory/internal/store"
)

func TestSteerEmitPersistsEvent(t *testing.T) {
	st := store.NewMemStore()
	em := NewSteerEmitter(st, nil, "p1")
	em.Emit("AGENT_INTERRUPT_REQUESTED", map[string]any{"session_id": "s1", "run_id": "r1"})
	evs, err := st.ListEvents(context.Background(), "p1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].EventType != "AGENT_INTERRUPT_REQUESTED" {
		t.Fatalf("events = %+v, want one AGENT_INTERRUPT_REQUESTED", evs)
	}
	if evs[0].SessionID != "s1" {
		t.Fatalf("session = %q, want s1", evs[0].SessionID)
	}
}

func TestSteerEmitDegradedSinksNeverFail(t *testing.T) {
	var nilEm *SteerEmitter
	nilEm.Emit("X", nil) // silent sink

	st := store.NewMemStore()
	NewSteerEmitter(st, NewHub(), "p1").Emit("AGENT_PAUSED", map[string]any{"session_id": "s1"}) // no subscribers: no panic
	NewSteerEmitter(nil, NewHub(), "p1").Emit("AGENT_PAUSED", map[string]any{})                  // fan-out-only
	NewSteerEmitter(st, nil, "").Emit("AGENT_PAUSED", map[string]any{})                          // empty project: no-op
	NewSteerEmitter(st, nil, "p1").Emit("  ", map[string]any{})                                  // empty type: no-op

	evs, _ := st.ListEvents(context.Background(), "p1", 0, 10)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (only the first emission persists)", len(evs))
	}
}
