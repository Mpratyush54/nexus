package server

// Hub fan-out tests (nexus issue #13): project/session routing, presence,
// typing, backpressure, and the HTTP upgrade/auth gate. All hub tests run
// in-memory (no sockets); only the upgrade-gate tests touch HTTP.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func newHubClient(h *Hub, user, project, session string) *Client {
	c := &Client{ID: newWSClientID(), UserID: user, ProjectID: project, SessionID: session}
	h.Add(c)
	return c
}

func readMsg(t *testing.T, c *Client) WSMessage {
	t.Helper()
	select {
	case raw := <-c.Send:
		var m WSMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode hub message: %v", err)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hub message")
		return WSMessage{}
	}
}

func mustSubscribe(t *testing.T, h *Hub, c *Client, project, session string) {
	t.Helper()
	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: project, SessionID: session})
	if err := h.HandleClientMessage(c.ID, raw); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	ack := readMsg(t, c)
	if ack.Type != WSMsgSubscribed {
		t.Fatalf("want subscribed ack, got %+v", ack)
	}
}

// Alice sends an action; Bob (same session) receives it as an event.
func TestHubSessionFanOut(t *testing.T) {
	h := NewHub()
	alice := newHubClient(h, "alice", "", "")
	bob := newHubClient(h, "bob", "", "")
	mustSubscribe(t, h, alice, "proj1", "sess1")
	mustSubscribe(t, h, bob, "proj1", "sess1")

	raw, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: "MESSAGE_SENT",
		Payload: map[string]any{"text": "hello bob"}})
	if err := h.HandleClientMessage(alice.ID, raw); err != nil {
		t.Fatalf("action: %v", err)
	}
	// Bob gets the event; Alice (sender) does not get an echo.
	ev := readMsg(t, bob)
	if ev.Type != WSMsgEvent || ev.EventType != "MESSAGE_SENT" || ev.UserID != "alice" {
		t.Fatalf("bob got %+v; want MESSAGE_SENT from alice", ev)
	}
	select {
	case extra := <-alice.Send:
		t.Fatalf("sender got unexpected echo: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// Project broadcast reaches session members; session broadcast skips
// project-only subscribers; other projects get nothing.
func TestHubProjectVsSessionRouting(t *testing.T) {
	h := NewHub()
	inSession := newHubClient(h, "s", "proj1", "sess1")
	projOnly := newHubClient(h, "p", "proj1", "")
	other := newHubClient(h, "o", "proj2", "")
	_ = inSession
	_ = projOnly
	_ = other
	// Drain the subscribe acks where used; here clients were pre-scoped, so
	// no acks are pending.

	h.BroadcastToProject("proj1", WSMessage{Type: WSMsgMemoryUpdate, Action: "proposed"})
	if m := readMsg(t, inSession); m.Type != WSMsgMemoryUpdate {
		t.Fatalf("session member missed project broadcast: %+v", m)
	}
	if m := readMsg(t, projOnly); m.Type != WSMsgMemoryUpdate {
		t.Fatalf("project subscriber missed project broadcast: %+v", m)
	}
	select {
	case extra := <-other.Send:
		t.Fatalf("other project got message: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}

	h.BroadcastToSession("sess1", WSMessage{Type: WSMsgEpisodeUpdate, Action: "opened"})
	if m := readMsg(t, inSession); m.Type != WSMsgEpisodeUpdate {
		t.Fatalf("session member missed session broadcast: %+v", m)
	}
	select {
	case extra := <-projOnly.Send:
		t.Fatalf("project-only client got session message: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHubPresenceTyping(t *testing.T) {
	h := NewHub()
	alice := newHubClient(h, "alice", "proj1", "sess1")
	bob := newHubClient(h, "bob", "proj1", "sess1")

	raw, _ := json.Marshal(WSMessage{Type: WSMsgPresence, Status: PresenceTyping})
	if err := h.HandleClientMessage(alice.ID, raw); err != nil {
		t.Fatalf("typing: %v", err)
	}
	p := readMsg(t, bob)
	if p.Type != WSMsgPresenceEvent || p.Status != PresenceTyping || p.UserID != "alice" {
		t.Fatalf("bob got %+v; want typing presence for alice", p)
	}
	select {
	case extra := <-alice.Send:
		t.Fatalf("sender got own presence echo: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}

	// Unknown status → error reply to sender, no fan-out.
	bad, _ := json.Marshal(WSMessage{Type: WSMsgPresence, Status: "invisible"})
	if err := h.HandleClientMessage(alice.ID, bad); err == nil {
		t.Fatal("expected unknown presence status to fail")
	}
	errReply := readMsg(t, alice)
	if errReply.Type != WSMsgError {
		t.Fatalf("want error reply, got %+v", errReply)
	}
	select {
	case extra := <-bob.Send:
		t.Fatalf("bob got fan-out for bad presence: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// Slow consumers must not stall the hub: full buffers shed + count drops.
func TestHubBackpressure(t *testing.T) {
	h := NewHub()
	fast := newHubClient(h, "fast", "proj1", "")
	slow := newHubClient(h, "slow", "proj1", "")
	// Fill slow's buffer to the brim.
	for i := 0; i < SendBufferSize; i++ {
		slow.Send <- []byte(`{"type":"fill"}`)
	}
	done := make(chan struct{})
	go func() {
		h.BroadcastToProject("proj1", WSMessage{Type: WSMsgEvent, EventType: "X"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on slow client")
	}
	if got := h.Dropped(slow.ID); got != 1 {
		t.Fatalf("slow client dropped = %d, want 1", got)
	}
	if got := h.Dropped(fast.ID); got != 0 {
		t.Fatalf("fast client dropped = %d, want 0", got)
	}
}

// SweepOffline marks stale clients offline; typing demotes to online.
func TestHubSweepOffline(t *testing.T) {
	now := time.Now()
	h := &Hub{clients: make(map[string]*Client), now: func() time.Time { return now }}
	stale := newHubClient(h, "stale", "proj1", "sess1")
	watcher := newHubClient(h, "watcher", "proj1", "sess1")
	stale.lastSeen = now.Add(-10 * time.Minute)

	ids := h.SweepOffline(90 * time.Second)
	if len(ids) != 1 || ids[0] != stale.ID {
		t.Fatalf("swept = %v; want [%s]", ids, stale.ID)
	}
	p := readMsg(t, watcher)
	if p.Type != WSMsgPresenceEvent || p.Status != PresenceOffline || p.UserID != "stale" {
		t.Fatalf("watcher got %+v; want offline for stale", p)
	}
}

// /ws requires auth and a real WebSocket upgrade.
func TestWSUpgradeGate(t *testing.T) {
	s := NewServer(store.NewMemStore())
	s.AttachHub(NewHub())
	token := loginAs(t, s, "alice")

	// No token → 401.
	rec := doJSON(t, s, http.MethodGet, "/ws", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rec.Code)
	}
	// Token but plain GET (no Upgrade headers) → 426.
	r := doAuthedGet(t, s, token)
	if r != http.StatusUpgradeRequired {
		t.Fatalf("plain-GET status = %d, want 426", r)
	}
}

// doAuthedGet performs an authenticated plain GET /ws (no upgrade headers).
func doAuthedGet(t *testing.T, s *Server, token string) int {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w := &captureWriter{header: http.Header{}}
	s.ServeHTTP(w, r)
	return w.code
}

type captureWriter struct {
	header http.Header
	code   int
}

func (w *captureWriter) Header() http.Header { return w.header }
func (w *captureWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return len(b), nil
}
func (w *captureWriter) WriteHeader(code int) { w.code = code }
