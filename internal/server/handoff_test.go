package server

// Session handoff REST + hub fan-out tests (nexus issue #82, server side).
//
// POST /sessions/{id}/handoff builds a HandoffPackage and emits
// SESSION_HANDOFF_INITIATED (persisted to the event log + fanned out live);
// POST /sessions/{id}/handoff/accept flips it and emits
// SESSION_HANDOFF_ACCEPTED. The CLI seam (cmd/nexus) is out of scope here.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/handoff"
	"central-memory/internal/store"
)

func handoffTestSetup(t *testing.T) (*Server, *Hub, string, string) {
	t.Helper()
	s := newTestServer()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "handoff-proj")
	rec := doJSON(t, s, http.MethodPost, "/sessions", token, map[string]any{
		"title": "handoff session", "project_id": projectID,
	})
	var sess store.Session
	decodeBody(t, rec, &sess)
	h := NewHub()
	s.AttachHub(h)
	return s, h, sess.ID, token
}

func watchProject(t *testing.T, h *Hub, user, project string) *Client {
	t.Helper()
	c := newHubClient(h, user, "", "")
	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: project})
	if err := h.HandleClientMessage(c.ID, raw); err != nil {
		t.Fatalf("watch subscribe: %v", err)
	}
	if ack := readMsg(t, c); ack.Type != WSMsgSubscribed {
		t.Fatalf("want ack, got %+v", ack)
	}
	return c
}

func TestHandoffInitAndAccept(t *testing.T) {
	s, h, sessID, token := handoffTestSetup(t)
	// Resolve the project for the watcher via the session (store read).
	ss := s.Store.(store.SessionStore)
	sess, err := ss.GetSession(context.Background(), sessID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}

	watcher := watchProject(t, h, "bob", sess.ProjectID)

	rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff", token, map[string]any{
		"to_user": "bob",
		"task":    map[string]any{"id": "t1", "title": "finish auth", "status": "IN_PROGRESS"},
		"note":    "auth refactor half done",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("init status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var pkg handoff.HandoffPackage
	decodeBody(t, rec, &pkg)
	if pkg.ID == "" || pkg.ToUser != "bob" || pkg.FromUser != "alice" {
		t.Fatalf("unexpected package: %+v", pkg)
	}

	// Live fan-out: the watcher sees SESSION_HANDOFF_INITIATED.
	ev := readMsg(t, watcher)
	if ev.Type != WSMsgEvent || ev.EventType != handoff.EventHandoffInitiated {
		t.Fatalf("watcher got %+v, want %s", ev, handoff.EventHandoffInitiated)
	}

	// Persisted to the event log.
	evs, err := s.Store.ListEvents(context.Background(), sess.ProjectID, 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.EventType == handoff.EventHandoffInitiated {
			found = true
		}
	}
	if !found {
		t.Fatalf("handoff init not persisted, events = %+v", evs)
	}

	// Accept as bob.
	bobToken := loginAs(t, s, "bob")
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff/accept", bobToken, map[string]any{
		"handoff_id": pkg.ID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var accepted handoff.HandoffPackage
	decodeBody(t, rec, &accepted)
	if !accepted.Accepted || accepted.AcceptedBy != "bob" {
		t.Fatalf("unexpected accepted package: %+v", accepted)
	}
	ev = readMsg(t, watcher)
	if ev.Type != WSMsgEvent || ev.EventType != handoff.EventHandoffAccepted {
		t.Fatalf("watcher got %+v, want %s", ev, handoff.EventHandoffAccepted)
	}

	// Double accept conflicts; wrong recipient conflicts.
	rec = doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff/accept", bobToken, map[string]any{
		"handoff_id": pkg.ID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("double accept = %d, want 409", rec.Code)
	}
}

func TestHandoffValidation(t *testing.T) {
	s, _, sessID, token := handoffTestSetup(t)

	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff", token, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("init without to_user = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions/nope/handoff", token, map[string]any{"to_user": "bob"}); rec.Code != http.StatusNotFound {
		t.Fatalf("init unknown session = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff/accept", token, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("accept without handoff_id = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff/accept", token, map[string]any{"handoff_id": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("accept unknown handoff = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessID+"/handoff", "", map[string]any{"to_user": "bob"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated init = %d, want 401", rec.Code)
	}
}
