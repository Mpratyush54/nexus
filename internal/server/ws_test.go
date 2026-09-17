package server

// Hub fan-out tests (nexus issue #13): project/session routing, presence,
// typing, backpressure, and the HTTP upgrade/auth gate. All hub tests run
// in-memory (no sockets); only the upgrade-gate tests touch HTTP.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
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

// --- RFC 6455 fragmentation tests (nexus issue #13 follow-up) ---------------

var wsTestMask = [4]byte{0x01, 0x02, 0x03, 0x04}

// encodeMaskedFrame builds one raw client→server frame (always masked).
func encodeMaskedFrame(fin bool, op byte, payload []byte) []byte {
	var out bytes.Buffer
	b0 := op & 0x0F
	if fin {
		b0 |= 0x80
	}
	out.WriteByte(b0)
	switch {
	case len(payload) <= 125:
		out.WriteByte(0x80 | byte(len(payload)))
	case len(payload) <= 65535:
		out.WriteByte(0x80 | 126)
		_ = binary.Write(&out, binary.BigEndian, uint16(len(payload)))
	default:
		out.WriteByte(0x80 | 127)
		_ = binary.Write(&out, binary.BigEndian, uint64(len(payload)))
	}
	out.Write(wsTestMask[:])
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ wsTestMask[i%4]
	}
	out.Write(masked)
	return out.Bytes()
}

// writeClientFrame writes one masked client frame onto w.
func writeClientFrame(t *testing.T, w io.Writer, fin bool, op byte, payload []byte) {
	t.Helper()
	if _, err := w.Write(encodeMaskedFrame(fin, op, payload)); err != nil {
		t.Fatalf("write client frame: %v", err)
	}
}

// readServerFrame reads one unmasked server→client frame.
func readServerFrame(t *testing.T, r io.Reader) (fin bool, op byte, payload []byte) {
	t.Helper()
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		t.Fatalf("read server frame header: %v", err)
	}
	fin = hdr[0]&0x80 != 0
	op = hdr[0] & 0x0F
	length := int64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("read server frame ext16: %v", err)
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			t.Fatalf("read server frame ext64: %v", err)
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if hdr[1]&0x80 != 0 {
		t.Fatal("server frames must not be masked")
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("read server frame payload: %v", err)
	}
	return fin, op, payload
}

// startWSReadLoop runs wsReadLoop over a net.Pipe; returns the test-side
// conn (test writes client frames here) and a done channel closed when the
// loop exits. Caller must close testConn at the end to stop the loop.
func startWSReadLoop(h *Hub, c *Client) (testConn net.Conn, done chan struct{}) {
	serverConn, clientConn := net.Pipe()
	rw := bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn))
	done = make(chan struct{})
	go func() {
		defer close(done)
		wsReadLoop(serverConn, rw, h, c)
	}()
	return clientConn, done
}

func drainSend(c *Client) []byte {
	select {
	case raw := <-c.Send:
		return raw
	default:
		return nil
	}
}

// wsReadFrame must surface the FIN bit (hdr[0] & 0x80), not just the opcode.
func TestWSReadFrameFINBit(t *testing.T) {
	raw := encodeMaskedFrame(false, wsOpText, []byte("hi"))
	rw := bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(raw)), bufio.NewWriter(io.Discard))
	op, fin, payload, err := wsReadFrame(rw)
	if err != nil {
		t.Fatalf("wsReadFrame FIN=0: %v", err)
	}
	if op != wsOpText || fin {
		t.Fatalf("FIN=0 frame: op=%#x fin=%v, want op=text fin=false", op, fin)
	}
	if string(payload) != "hi" {
		t.Fatalf("payload = %q, want %q", payload, "hi")
	}

	raw = encodeMaskedFrame(true, wsOpContinuation, []byte("yo"))
	rw = bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(raw)), bufio.NewWriter(io.Discard))
	op, fin, payload, err = wsReadFrame(rw)
	if err != nil {
		t.Fatalf("wsReadFrame FIN=1: %v", err)
	}
	if op != wsOpContinuation || !fin {
		t.Fatalf("FIN=1 frame: op=%#x fin=%v, want op=continuation fin=true", op, fin)
	}
	if string(payload) != "yo" {
		t.Fatalf("payload = %q, want %q", payload, "yo")
	}
}

// A JSON message split into text(FIN=0) + continuation(FIN=1) must dispatch
// exactly once, with the complete payload — no premature dispatch of the
// first fragment (which alone is invalid JSON).
func TestWSFragmentedMessageSingleDispatch(t *testing.T) {
	h := NewHub()
	c := newHubClient(h, "frag-user", "", "")
	testConn, done := startWSReadLoop(h, c)
	defer func() { _ = testConn.Close(); <-done }()

	full, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: "proj1", SessionID: "sess1"})
	split := len(full) / 2
	writeClientFrame(t, testConn, false, wsOpText, full[:split])

	// First fragment alone must NOT dispatch (no error reply for bad JSON).
	select {
	case extra := <-c.Send:
		t.Fatalf("premature dispatch after first fragment: %s", extra)
	case <-time.After(150 * time.Millisecond):
	}

	writeClientFrame(t, testConn, true, wsOpContinuation, full[split:])

	select {
	case raw := <-c.Send:
		var ack WSMessage
		if err := json.Unmarshal(raw, &ack); err != nil {
			t.Fatalf("decode ack: %v", err)
		}
		if ack.Type != WSMsgSubscribed || ack.ProjectID != "proj1" || ack.SessionID != "sess1" {
			t.Fatalf("ack = %+v; want subscribed proj1/sess1", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reassembled dispatch")
	}
	// Exactly one dispatch: nothing else pending.
	select {
	case extra := <-c.Send:
		t.Fatalf("extra dispatch after reassembly: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
	if c.ProjectID != "proj1" || c.SessionID != "sess1" {
		t.Fatalf("client scope = %q/%q; want proj1/sess1", c.ProjectID, c.SessionID)
	}
}

// Unfragmented single-frame messages (text FIN=1) must keep working.
func TestWSUnfragmentedStillWorks(t *testing.T) {
	h := NewHub()
	c := newHubClient(h, "u", "", "")
	testConn, done := startWSReadLoop(h, c)
	defer func() { _ = testConn.Close(); <-done }()

	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: "p", SessionID: "s"})
	writeClientFrame(t, testConn, true, wsOpText, raw)

	select {
	case got := <-c.Send:
		var ack WSMessage
		if err := json.Unmarshal(got, &ack); err != nil {
			t.Fatalf("decode ack: %v", err)
		}
		if ack.Type != WSMsgSubscribed {
			t.Fatalf("want subscribed ack, got %+v", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for unfragmented dispatch")
	}
}

// An interleaved ping between fragments must be answered with pong and must
// not corrupt the reassembly buffer.
func TestWSInterleavedPingPreservesFrag(t *testing.T) {
	h := NewHub()
	c := newHubClient(h, "ping-user", "", "")
	testConn, done := startWSReadLoop(h, c)
	defer func() { _ = testConn.Close(); <-done }()

	full, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: "proj1", SessionID: "sess1"})
	split := len(full) / 2
	writeClientFrame(t, testConn, false, wsOpText, full[:split])
	writeClientFrame(t, testConn, true, wsOpPing, []byte("ping1"))

	_ = testConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	fin, op, payload := readServerFrame(t, testConn)
	_ = testConn.SetReadDeadline(time.Time{})
	if op != wsOpPong || !fin || string(payload) != "ping1" {
		t.Fatalf("pong = op=%#x fin=%v payload=%q; want pong fin=true %q", op, fin, payload, "ping1")
	}

	writeClientFrame(t, testConn, true, wsOpContinuation, full[split:])

	select {
	case raw := <-c.Send:
		var ack WSMessage
		if err := json.Unmarshal(raw, &ack); err != nil {
			t.Fatalf("decode ack: %v", err)
		}
		if ack.Type != WSMsgSubscribed || ack.ProjectID != "proj1" {
			t.Fatalf("ack = %+v; want subscribed proj1", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dispatch after interleaved ping")
	}
	select {
	case extra := <-c.Send:
		t.Fatalf("extra dispatch after ping test: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// A continuation with no open fragment must be ignored, not dispatched.
func TestWSStrayContinuationIgnored(t *testing.T) {
	h := NewHub()
	c := newHubClient(h, "stray", "", "")
	testConn, done := startWSReadLoop(h, c)
	defer func() { _ = testConn.Close(); <-done }()

	writeClientFrame(t, testConn, true, wsOpContinuation, []byte(`{"type":"subscribe"}`))
	select {
	case extra := <-c.Send:
		t.Fatalf("stray continuation dispatched: %s", extra)
	case <-time.After(150 * time.Millisecond):
	}
	if got := drainSend(c); got != nil {
		t.Fatalf("unexpected message after stray continuation: %s", got)
	}
}
