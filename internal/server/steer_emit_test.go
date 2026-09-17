// Tests for the steer emitter bridge (issue #42). All DB-free: a fake
// SteerEventStore scripts persistence, a real Hub observes fan-out.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"central-memory/internal/steer"
	"central-memory/internal/store"
)

// fakeSteerStore scripts SteerEventStore: records params, returns a canned
// row, or fails when failErr is set.
type fakeSteerStore struct {
	got    []store.AppendEventParams
	row    *store.Event
	failEr error
	calls  int
}

func (f *fakeSteerStore) AppendEvent(_ context.Context, p store.AppendEventParams) (*store.Event, error) {
	f.calls++
	f.got = append(f.got, p)
	if f.failEr != nil {
		return nil, f.failEr
	}
	if f.row != nil {
		return f.row, nil
	}
	return &store.Event{
		ID: 1, ProjectID: p.ProjectID, SessionID: p.SessionID,
		EventType: p.EventType, Payload: json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// panicSteerStore panics on AppendEvent (sink isolation probe).
type panicSteerStore struct{ fakeSteerStore }

func (p *panicSteerStore) AppendEvent(ctx context.Context, params store.AppendEventParams) (*store.Event, error) {
	panic("store: steer sink panic")
}

// drainHubClient drops buffered frames (e.g. the Register presence hello).
func drainHubClient(c *Client) {
	for {
		select {
		case <-c.Send:
		default:
			return
		}
	}
}

// nextServerMessage reads one frame off c (failing the test on timeout).
func nextServerMessage(t *testing.T, c *Client) ServerMessage {
	t.Helper()
	select {
	case raw := <-c.Send:
		var m ServerMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("fan-out frame is not JSON: %v", err)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for fan-out frame")
		return ServerMessage{}
	}
}

func TestSteerEmitterPersistsAndFansOut(t *testing.T) {
	hub := NewHub(HubOptions{})
	watcher := hub.NewClient("dash", "p1", "", nil) // project-level watcher sees every session
	hub.Register(watcher)
	drainHubClient(watcher)

	fake := &fakeSteerStore{}
	em := NewSteerEmitter(fake, hub, "p1")
	em.Emit("AGENT_STEER_PROMPT", map[string]any{
		"session_id": "s1", "run_id": "r1", "prompt": "use tabs", "from": "dash",
	})

	if fake.calls != 1 {
		t.Fatalf("AppendEvent calls = %d, want 1", fake.calls)
	}
	got := fake.got[0]
	if got.ProjectID != "p1" || got.SessionID != "s1" || got.EventType != "AGENT_STEER_PROMPT" {
		t.Errorf("append params wrong: %+v", got)
	}
	if !store.IsValidEventType(got.EventType) {
		t.Errorf("appended type %q must be a registered §2.1 type (issue #42)", got.EventType)
	}

	msg := nextServerMessage(t, watcher)
	if msg.Type != MsgEvent {
		t.Fatalf("frame type = %q, want %q", msg.Type, MsgEvent)
	}
	if !strings.Contains(string(msg.Event), "AGENT_STEER_PROMPT") {
		t.Errorf("event frame missing steering type: %s", msg.Event)
	}
}

func TestSteerEmitterControllerEndToEnd(t *testing.T) {
	// The seam proof: a steer Controller driving this Emitter persists and
	// fans out without any adapter beyond NewSteerEmitter.
	hub := NewHub(HubOptions{})
	watcher := hub.NewClient("dash", "p1", "s1", nil)
	hub.Register(watcher)
	drainHubClient(watcher)

	fake := &fakeSteerStore{}
	em := NewSteerEmitter(fake, hub, "p1")
	var _ steer.Emitter = em // seam assertion (also compile-time in steer_emit.go)

	ctrl := steer.NewController(em, nil)
	if _, err := ctrl.StartRun(context.Background(), "s1", "r1"); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.RequestInterrupt("s1", "dash", "wrong direction"); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.AcknowledgePaused("s1"); err != nil {
		t.Fatal(err)
	}

	if fake.calls != 2 {
		t.Fatalf("AppendEvent calls = %d, want 2 (interrupt + paused)", fake.calls)
	}
	for i, typ := range []string{"AGENT_INTERRUPT_REQUESTED", "AGENT_PAUSED"} {
		if fake.got[i].EventType != typ {
			t.Errorf("append[%d] type = %q, want %q", i, fake.got[i].EventType, typ)
		}
	}
	for _, typ := range []string{"AGENT_INTERRUPT_REQUESTED", "AGENT_PAUSED"} {
		msg := nextServerMessage(t, watcher)
		if msg.Type != MsgEvent || !strings.Contains(string(msg.Event), typ) {
			t.Errorf("frame missing %s: %+v", typ, msg)
		}
	}
}

func TestSteerEmitterNilHubPersistOnly(t *testing.T) {
	fake := &fakeSteerStore{}
	em := NewSteerEmitter(fake, nil, "p1")
	em.Emit("AGENT_PAUSED", map[string]any{"session_id": "s1", "run_id": "r1"})
	if fake.calls != 1 || fake.got[0].EventType != "AGENT_PAUSED" {
		t.Errorf("persist-only emit wrong: calls=%d got=%+v", fake.calls, fake.got)
	}
}

func TestSteerEmitterNilStoreEnvelopeFallback(t *testing.T) {
	hub := NewHub(HubOptions{})
	watcher := hub.NewClient("dash", "p1", "s1", nil)
	hub.Register(watcher)
	drainHubClient(watcher)

	NewSteerEmitter(nil, hub, "p1").Emit("AGENT_RESUMED", map[string]any{
		"session_id": "s1", "run_id": "r1", "resumed_by": "dash",
	})
	msg := nextServerMessage(t, watcher)
	if msg.Type != MsgEvent || !strings.Contains(string(msg.Event), "AGENT_RESUMED") {
		t.Errorf("envelope fallback frame wrong: %+v", msg)
	}
}

func TestSteerEmitterStoreErrorStillFansOut(t *testing.T) {
	hub := NewHub(HubOptions{})
	watcher := hub.NewClient("dash", "p1", "s1", nil)
	hub.Register(watcher)
	drainHubClient(watcher)

	fake := &fakeSteerStore{failEr: errors.New("db down")}
	NewSteerEmitter(fake, hub, "p1").Emit("AGENT_PAUSED", map[string]any{"session_id": "s1"})
	msg := nextServerMessage(t, watcher) // live continuity despite persistence failure
	if msg.Type != MsgEvent || !strings.Contains(string(msg.Event), "AGENT_PAUSED") {
		t.Errorf("fallback frame wrong after store error: %+v", msg)
	}
}

func TestSteerEmitterDegradedNeverPanics(t *testing.T) {
	var nilEm *SteerEmitter
	nilEm.Emit("AGENT_PAUSED", map[string]any{"session_id": "s1"}) // nil receiver

	NewSteerEmitter(&fakeSteerStore{}, nil, "p1").Emit("", map[string]any{"session_id": "s1"})  // blank type
	NewSteerEmitter(&fakeSteerStore{}, nil, "  ").Emit("AGENT_PAUSED", nil)                     // blank project
	NewSteerEmitter(&fakeSteerStore{}, nil, "p1").Emit("AGENT_PAUSED", nil)                     // nil payload
	NewSteerEmitter(&panicSteerStore{}, nil, "p1").Emit("AGENT_PAUSED", map[string]any{"x": 1}) // panicking store
	hub := NewHub(HubOptions{})
	NewSteerEmitter(&panicSteerStore{}, hub, "p1").Emit("AGENT_PAUSED", map[string]any{"x": 1}) // panic + hub
}
