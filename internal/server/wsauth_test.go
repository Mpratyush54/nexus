package server

// WebSocket project/session authorization tests (nexus issue #90).
//
// The hub must never trust client-supplied project/session IDs: every
// subscribe and every action is authorized through the store-backed
// authorizer (project must exist; sessions must belong to the project with
// the user as participant/creator). Cross-project subscription and
// injection attempts fail with a clear error.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func authzTestSetup(t *testing.T) (*Server, *Hub, string, string, string, string) {
	t.Helper()
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	projA := resolveTestProject(t, s, alice, "authz-proj-a")
	projB := resolveTestProject(t, s, alice, "authz-proj-b")

	// Session in projA created by alice (creator is always authorized).
	rec := doJSON(t, s, http.MethodPost, "/sessions", alice, map[string]any{
		"title": "authz session", "project_id": projA,
	})
	var sess store.Session
	decodeBody(t, rec, &sess)

	h := NewHub()
	s.AttachHub(h)
	// Mallory and the watcher participate via workspaces on projB so
	// project-only subscribes pass the membership boundary (issue #141).
	// Alice is already a member of both projects via resolveTestProject.
	for _, u := range []string{"mallory", "watcher"} {
		if err := s.Store.RegisterWorkspace(t.Context(), &store.Workspace{
			ProjectID: projB, UserID: u,
			MachineID: "m-" + u, Path: "/tmp/ws-" + u,
		}); err != nil {
			t.Fatalf("RegisterWorkspace %s: %v", u, err)
		}
	}
	return s, h, projA, projB, sess.ID, alice
}

func subscribeAs(t *testing.T, h *Hub, c *Client, project, session string) error {
	t.Helper()
	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: project, SessionID: session})
	return h.HandleClientMessage(c.ID, raw)
}

// A stranger cannot enter another project's guarded session, and traffic
// never crosses project scopes.
func TestWSAuthzCrossProjectSessionRejected(t *testing.T) {
	s, h, projA, projB, sessA, alice := authzTestSetup(t)

	// Alice joins: the session now has a participant, closing first-join.
	rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessA+"/join", alice, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("join status = %d, body = %s", rec.Code, rec.Body.String())
	}

	mallory := newHubClient(h, "mallory", "", "")
	if err := subscribeAs(t, h, mallory, projA, sessA); err != errWSForbidden {
		t.Fatalf("cross-project session subscribe err = %v, want errWSForbidden", err)
	}
	if reply := readMsg(t, mallory); reply.Type != WSMsgError {
		t.Fatalf("want error reply, got %+v", reply)
	}

	// Mallory scopes to her own project instead: allowed, but projA
	// traffic never reaches her.
	if err := subscribeAs(t, h, mallory, projB, ""); err != nil {
		t.Fatalf("own-project subscribe: %v", err)
	}
	if ack := readMsg(t, mallory); ack.Type != WSMsgSubscribed {
		t.Fatalf("want subscribed ack, got %+v", ack)
	}

	aliceC := newHubClient(h, "alice", "", "")
	if err := subscribeAs(t, h, aliceC, projA, sessA); err != nil {
		t.Fatalf("creator subscribe: %v", err)
	}
	readMsg(t, aliceC) // drain ack

	raw, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: "SECRET",
		Payload: map[string]any{"text": "projA only"}})
	if err := h.HandleClientMessage(aliceC.ID, raw); err != nil {
		t.Fatalf("authorized action: %v", err)
	}
	select {
	case extra := <-mallory.Send:
		t.Fatalf("cross-project leak: %s", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

// Guarded sessions reject strangers; creators/participants pass.
func TestWSAuthzMembershipEnforced(t *testing.T) {
	s, h, projA, _, sessA, alice := authzTestSetup(t)

	rec := doJSON(t, s, http.MethodPost, "/sessions/"+sessA+"/join", alice, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("join status = %d, body = %s", rec.Code, rec.Body.String())
	}

	mallory := newHubClient(h, "mallory", "", "")
	if err := subscribeAs(t, h, mallory, projA, sessA); err == nil {
		t.Fatal("non-member subscribe to guarded session must fail")
	} else if err != errWSForbidden {
		t.Fatalf("err = %v, want errWSForbidden", err)
	}
	// Error reply goes to the requester only.
	reply := readMsg(t, mallory)
	if reply.Type != WSMsgError {
		t.Fatalf("want error reply, got %+v", reply)
	}

	aliceC := newHubClient(h, "alice", "", "")
	if err := subscribeAs(t, h, aliceC, projA, sessA); err != nil {
		t.Fatalf("creator subscribe: %v", err)
	}
	ack := readMsg(t, aliceC)
	if ack.Type != WSMsgSubscribed {
		t.Fatalf("want subscribed ack, got %+v", ack)
	}
}

func TestWSAuthzUnknownProjectRejected(t *testing.T) {
	_, h, _, _, _, _ := authzTestSetup(t)
	c := newHubClient(h, "ghost", "", "")
	if err := subscribeAs(t, h, c, "proj-does-not-exist", ""); err == nil {
		t.Fatal("subscribe to unknown project must fail")
	}
	if err := subscribeAs(t, h, c, "", ""); err == nil {
		t.Fatal("subscribe with empty project must fail")
	}
}

func TestWSAuthzActionRequiresAuthorizedScope(t *testing.T) {
	_, h, _, projB, _, _ := authzTestSetup(t)
	// Client that never subscribed has no scope: authorizer fails closed.
	ghost := newHubClient(h, "ghost", "", "")
	raw, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: "MESSAGE_SENT",
		Payload: map[string]any{"text": "injection"}})
	if err := h.HandleClientMessage(ghost.ID, raw); err == nil {
		t.Fatal("action without authorized scope must fail")
	}
	reply := readMsg(t, ghost)
	if reply.Type != WSMsgError {
		t.Fatalf("want error reply, got %+v", reply)
	}

	// Project-scoped subscriber acts inside its own scope fine.
	watcher := newHubClient(h, "watcher", "", "")
	if err := subscribeAs(t, h, watcher, projB, ""); err != nil {
		t.Fatalf("project subscribe: %v", err)
	}
	readMsg(t, watcher) // drain ack
	raw2, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: "PING",
		Payload: map[string]any{}})
	if err := h.HandleClientMessage(watcher.ID, raw2); err != nil {
		t.Fatalf("authorized project action must succeed: %v", err)
	}
}
