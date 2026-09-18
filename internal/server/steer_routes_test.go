package server

// SteerEmitter/InterruptManager route + WS wiring tests (nexus issue #42).
//
// The four POST /sessions/{id}/steer/* routes drive the manager state
// machine and emit AGENT_* events (persisted + fanned out); WS actions
// carrying steering event types route through the same manager via the hub
// steer hook instead of broadcasting raw.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/steering"
	"central-memory/internal/store"
)

func steerTestSetup(t *testing.T) (*Server, *Hub, *steering.InterruptManager, string, string) {
	t.Helper()
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "steer-proj")
	rec := doJSON(t, s, http.MethodPost, "/sessions", token, map[string]any{
		"title": "steer session", "project_id": projectID,
	})
	var sess store.Session
	decodeBody(t, rec, &sess)
	h := NewHub()
	m := steering.NewInterruptManager()
	s.AttachSteering(m, h)
	return s, h, m, sess.ID, token
}

func TestSteerRoutesDriveManager(t *testing.T) {
	s, _, m, runID, token := steerTestSetup(t)

	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+runID+"/steer/interrupt", token, map[string]any{"reason": "wrong direction"}); rec.Code != http.StatusOK {
		t.Fatalf("interrupt = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := m.State(runID); got != steering.StatePauseRequested {
		t.Fatalf("state = %q, want pause_requested", got)
	}

	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+runID+"/steer/prompt", token, map[string]any{"prompt": "use tabs"}); rec.Code != http.StatusOK {
		t.Fatalf("prompt = %d, body = %s", rec.Code, rec.Body.String())
	}
	if d := m.QueueDepth(runID); d != 1 {
		t.Fatalf("queue depth = %d, want 1", d)
	}

	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+runID+"/steer/ack", token, map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("ack = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := m.State(runID); got != steering.StatePaused {
		t.Fatalf("state = %q, want paused", got)
	}

	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+runID+"/steer/resume", token, map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := m.State(runID); got != steering.StateRunning {
		t.Fatalf("state = %q, want running", got)
	}

	// Steering events were persisted (run_id == session id).
	evs, err := s.Store.ListEvents(context.Background(), s.hubProjectOf(t, runID), 0, 20)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.EventType] = true
	}
	for _, want := range []string{steering.EventInterruptRequested, steering.EventSteerPrompt, steering.EventPaused, steering.EventResumed} {
		if !seen[want] {
			t.Errorf("missing persisted event %s (have %v)", want, seen)
		}
	}
}

func TestSteerRoutesNeedManager(t *testing.T) {
	s := newTestServer() // no AttachSteering: routes 501
	token := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodPost, "/sessions/x/steer/interrupt", token, map[string]any{"reason": "r"})
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured interrupt = %d, want 501", rec.Code)
	}
}

// WS actions with steering event types drive the manager; plain actions
// still broadcast.
func TestSteerWSHookRoutesManager(t *testing.T) {
	s, h, m, runID, token := steerTestSetup(t)
	_ = s
	projectID := s.hubProjectOf(t, runID)

	alice := newHubClient(h, "alice", "", "")
	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: projectID, SessionID: runID})
	_ = token
	if err := h.HandleClientMessage(alice.ID, raw); err != nil {
		// Session subscribe requires membership; project-only always works.
		raw2, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: projectID})
		if err := h.HandleClientMessage(alice.ID, raw2); err != nil {
			t.Fatalf("project subscribe: %v", err)
		}
		readMsg(t, alice)
	} else {
		readMsg(t, alice)
	}

	bob := newHubClient(h, "bob", "", "")
	rawB, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: projectID})
	if err := h.HandleClientMessage(bob.ID, rawB); err != nil {
		t.Fatalf("bob subscribe: %v", err)
	}
	readMsg(t, bob)

	// Steering action: drives the manager, fans out as an event (no raw
	// broadcast of the action envelope).
	act, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: steering.EventInterruptRequested,
		Payload: map[string]any{"run_id": runID, "reason": "ws steer"}})
	if err := h.HandleClientMessage(alice.ID, act); err != nil {
		t.Fatalf("steer action: %v", err)
	}
	if got := m.State(runID); got != steering.StatePauseRequested {
		t.Fatalf("manager state = %q, want pause_requested", got)
	}
	// Both subscribers see the emitted AGENT_INTERRUPT_REQUESTED event.
	for _, c := range []*Client{alice, bob} {
		ev := readMsg(t, c)
		if ev.Type != WSMsgEvent || ev.EventType != steering.EventInterruptRequested {
			t.Fatalf("client got %+v, want %s event", ev, steering.EventInterruptRequested)
		}
	}

	// Plain action still broadcasts to the room (not swallowed by the hook).
	plain, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: "CHAT",
		Payload: map[string]any{"text": "hi"}})
	if err := h.HandleClientMessage(alice.ID, plain); err != nil {
		t.Fatalf("plain action: %v", err)
	}
	ev := readMsg(t, bob)
	if ev.Type != WSMsgEvent || ev.EventType != "CHAT" {
		t.Fatalf("bob got %+v, want CHAT broadcast", ev)
	}
}

// hubProjectOf resolves a session's project for event-log assertions.
func (s *Server) hubProjectOf(t *testing.T, sessionID string) string {
	t.Helper()
	ss, ok := s.sessionStore()
	if !ok {
		t.Fatal("session store required")
	}
	sess, err := ss.GetSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return sess.ProjectID
}
