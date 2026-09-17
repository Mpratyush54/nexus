// Tests for the WebSocket hub (issue #13, plan §§3.2, 3.4). All DB-free:
// hub tests drive Hub synchronously through Client.Send channels (fake
// conns, no network); handler tests swap wsUpgrader for a stub. Clocks are
// controllable, so presence transitions run in milliseconds and fan-out is
// asserted sub-second.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeWSConn is a channel-backed WSConn: tests push inbound frames and pull
// outbound frames without touching the network.
type fakeWSConn struct {
	mu       sync.Mutex
	inbound  chan []byte
	outbound chan []byte
	closed   chan struct{}
	once     sync.Once
	pong     func()
	pongs    int
}

func newFakeWSConn() *fakeWSConn {
	return &fakeWSConn{
		inbound:  make(chan []byte, 16),
		outbound: make(chan []byte, 64),
		closed:   make(chan struct{}),
	}
}

func (f *fakeWSConn) ReadText() ([]byte, error) {
	select {
	case m := <-f.inbound:
		return m, nil
	case <-f.closed:
		return nil, ErrWSClosed
	}
}

func (f *fakeWSConn) WriteText(p []byte) error {
	cp := append([]byte(nil), p...)
	select {
	case f.outbound <- cp:
		return nil
	case <-f.closed:
		return errors.New("closed")
	}
}

func (f *fakeWSConn) Ping() error { return nil }

func (f *fakeWSConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeWSConn) SetWriteDeadline(time.Time) error { return nil }

func (f *fakeWSConn) SetPongHandler(h func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pong = h
}

func (f *fakeWSConn) firePong() {
	f.mu.Lock()
	h := f.pong
	f.pongs++
	f.mu.Unlock()
	if h != nil {
		h()
	}
}

func (f *fakeWSConn) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// stubUpgrader captures created conns so handler tests can drive and close
// the server side of the connection.
type stubUpgrader struct {
	mu    sync.Mutex
	conns []*fakeWSConn
}

func (s *stubUpgrader) Upgrade(w http.ResponseWriter, r *http.Request) (WSConn, error) {
	c := newFakeWSConn()
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	return c, nil
}

func (s *stubUpgrader) last() *fakeWSConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) == 0 {
		return nil
	}
	return s.conns[len(s.conns)-1]
}

func (s *stubUpgrader) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func readOne(t *testing.T, c *Client, d time.Duration) ServerMessage {
	t.Helper()
	select {
	case raw := <-c.Send:
		var m ServerMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode server frame: %v (%s)", err, raw)
		}
		return m
	case <-time.After(d):
		t.Fatalf("timeout waiting for frame for client %s", c.ID)
		return ServerMessage{}
	}
}

func drainClient(c *Client) {
	for {
		select {
		case <-c.Send:
		default:
			return
		}
	}
}

func expectSilent(t *testing.T, c *Client, d time.Duration) {
	t.Helper()
	select {
	case raw := <-c.Send:
		t.Fatalf("unexpected frame for client %s: %s", c.ID, raw)
	case <-time.After(d):
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timeout waiting: %s", msg)
	}
}

func findChange(changes []PresenceChange, user, to string) bool {
	for _, ch := range changes {
		if ch.UserID == user && ch.To == to {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Fan-out (§3.2 protocol)
// ---------------------------------------------------------------------------

// TestHubFanoutSubSecond: two subscribers in one project/session both get an
// event in <1s; a client in another project gets nothing.
func TestHubFanoutSubSecond(t *testing.T) {
	hub := NewHub(HubOptions{})
	a := hub.NewClient("alice", "p1", "s1", nil)
	b := hub.NewClient("bob", "p1", "s1", nil)
	other := hub.NewClient("mallory", "p2", "s1", nil)
	hub.Register(a)
	hub.Register(b)
	hub.Register(other)
	drainClient(a)
	drainClient(b)
	drainClient(other)

	start := time.Now()
	n, err := hub.PublishEvent("p1", "s1", map[string]any{
		"event_type": "MESSAGE_SENT", "payload": map[string]string{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if n != 2 {
		t.Fatalf("receivers = %d, want 2", n)
	}
	ma := readOne(t, a, 500*time.Millisecond)
	mb := readOne(t, b, 500*time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatalf("fan-out took %v, want <1s", time.Since(start))
	}
	for i, m := range []ServerMessage{ma, mb} {
		if m.Type != MsgEvent {
			t.Fatalf("frame[%d].type = %q, want event", i, m.Type)
		}
		var evt struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal(m.Event, &evt); err != nil || evt.EventType != "MESSAGE_SENT" {
			t.Fatalf("frame[%d].event = %s, want MESSAGE_SENT", i, m.Event)
		}
	}
	expectSilent(t, other, 50*time.Millisecond)
}

// TestHubSessionIsolation: session-scoped events reach only the matching
// session (+ project-level watchers); project-wide events reach everyone in
// the project.
func TestHubSessionIsolation(t *testing.T) {
	hub := NewHub(HubOptions{})
	a := hub.NewClient("alice", "p1", "s1", nil)
	b := hub.NewClient("bob", "p1", "s2", nil)
	watch := hub.NewClient("dash", "p1", "", nil) // project-level watcher
	hub.Register(a)
	hub.Register(b)
	hub.Register(watch)
	drainClient(a)
	drainClient(b)
	drainClient(watch)

	if n, err := hub.PublishEvent("p1", "s1", map[string]string{"m": "1"}); err != nil || n != 2 {
		t.Fatalf("session publish: n=%d err=%v, want n=2", n, err)
	}
	if m := readOne(t, a, 300*time.Millisecond); m.Type != MsgEvent {
		t.Fatalf("a got %q, want event", m.Type)
	}
	if m := readOne(t, watch, 300*time.Millisecond); m.Type != MsgEvent {
		t.Fatalf("watcher got %q, want event", m.Type)
	}
	expectSilent(t, b, 50*time.Millisecond)

	if n, err := hub.PublishEvent("p1", "", map[string]string{"m": "wide"}); err != nil || n != 3 {
		t.Fatalf("project-wide publish: n=%d err=%v, want n=3", n, err)
	}
	for _, c := range []*Client{a, b, watch} {
		if m := readOne(t, c, 300*time.Millisecond); m.Type != MsgEvent {
			t.Fatalf("%s got %q for project-wide, want event", c.ID, m.Type)
		}
	}
}

// TestHubPublishMemoryEpisode covers memory_update/episode_update fan-out
// plus action-vocabulary validation.
func TestHubPublishMemoryEpisode(t *testing.T) {
	hub := NewHub(HubOptions{})
	a := hub.NewClient("alice", "p1", "s1", nil)
	hub.Register(a)
	drainClient(a)

	if _, err := hub.PublishMemoryUpdate("p1", "s1", map[string]string{"key": "k"}, "bogus"); err == nil {
		t.Fatal("bad memory action accepted")
	}
	if _, err := hub.PublishEpisodeUpdate("p1", "s1", map[string]string{"t": "e"}, "bogus"); err == nil {
		t.Fatal("bad episode action accepted")
	}
	if _, err := hub.PublishEvent("", "s1", map[string]string{"m": "x"}); err == nil {
		t.Fatal("empty project publish accepted")
	}

	n, err := hub.PublishMemoryUpdate("p1", "s1", map[string]string{"key": "testing/x"}, "confirmed")
	if err != nil || n != 1 {
		t.Fatalf("memory publish: n=%d err=%v", n, err)
	}
	m := readOne(t, a, 300*time.Millisecond)
	if m.Type != MsgMemoryUpdate || m.Action != "confirmed" {
		t.Fatalf("got %+v, want memory_update/confirmed", m)
	}
	var item map[string]string
	if err := json.Unmarshal(m.Item, &item); err != nil || item["key"] != "testing/x" {
		t.Fatalf("item = %s", m.Item)
	}

	n, err = hub.PublishEpisodeUpdate("p1", "", map[string]string{"title": "bug"}, "opened")
	if err != nil || n != 1 {
		t.Fatalf("episode publish: n=%d err=%v", n, err)
	}
	m = readOne(t, a, 300*time.Millisecond)
	if m.Type != MsgEpisodeUpdate || m.Action != "opened" {
		t.Fatalf("got %+v, want episode_update/opened", m)
	}
}

// TestHubSlowClientDrop: a client that never drains sheds load (bounded
// Send) and is evicted past the drop budget; healthy fan-out is unaffected.
func TestHubSlowClientDrop(t *testing.T) {
	hub := NewHub(HubOptions{SendBufferSize: 1, MaxDropsBeforeEvict: 4})
	slow := hub.NewClient("slow", "p1", "s1", nil)
	fast := hub.NewClient("fast", "p1", "s1", nil)
	hub.Register(slow)
	hub.Register(fast)
	// Register's hello fills slow's size-1 buffer; fast drains freely.
	drainClient(fast)

	for i := 0; i < 6; i++ {
		if _, err := hub.PublishEvent("p1", "s1", map[string]int{"n": i}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		// Fast keeps up; slow never reads.
		for {
			select {
			case <-fast.Send:
			default:
				goto drained
			}
		}
	drained:
	}
	if got := slow.Dropped(); got < 4 {
		t.Fatalf("slow dropped = %d, want >= 4", got)
	}
	if n := len(slow.Send); n > 1 {
		t.Fatalf("slow outbox len = %d, want <= cap 1", n)
	}
	waitFor(t, time.Second, func() bool { return hub.ClientCount() == 1 }, "slow client evicted")
	if hub.ClientCount() != 1 {
		t.Fatalf("count = %d, want 1 (fast survives)", hub.ClientCount())
	}
}

// ---------------------------------------------------------------------------
// Presence (§3.4)
// ---------------------------------------------------------------------------

// TestPresenceTransitions: online → typing → (decay) online → idle →
// offline, with every step announced to peers and visible in snapshots.
func TestPresenceTransitions(t *testing.T) {
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	now := base
	hub := NewHub(HubOptions{
		Now:           func() time.Time { return now },
		IdleAfter:     50 * time.Millisecond,
		TypingTimeout: 20 * time.Millisecond,
		OfflineAfter:  100 * time.Millisecond,
	})
	a := hub.NewClient("alice", "p1", "s1", nil)
	b := hub.NewClient("bob", "p1", "s1", nil)
	hub.Register(a)
	drainClient(a)
	hub.Register(b)
	drainClient(b)

	// Bob's register announces online to Alice.
	m := readOne(t, a, 300*time.Millisecond)
	if m.Type != MsgPresence || m.UserID != "bob" || m.Status != PresenceOnline {
		t.Fatalf("hello = %+v, want presence/bob/online", m)
	}
	if got := hub.PresenceSnapshot("p1", "s1")["bob"]; got != PresenceOnline {
		t.Fatalf("snapshot bob = %q, want online", got)
	}

	// Bob types → Alice sees typing.
	if err := hub.HandleClientMessage(b, []byte(`{"type":"presence","status":"typing"}`)); err != nil {
		t.Fatalf("typing: %v", err)
	}
	m = readOne(t, a, 300*time.Millisecond)
	if m.Status != PresenceTyping || m.UserID != "bob" {
		t.Fatalf("typing frame = %+v", m)
	}

	// Typing decays to online past TypingTimeout.
	now = base.Add(30 * time.Millisecond)
	changes := hub.ReapStale(now)
	if !findChange(changes, "bob", PresenceOnline) {
		t.Fatalf("no typing→online decay in %+v", changes)
	}
	m = readOne(t, a, 300*time.Millisecond)
	if m.Status != PresenceOnline {
		t.Fatalf("decay frame = %+v, want online", m)
	}

	// Silence past IdleAfter → idle.
	now = base.Add(90 * time.Millisecond)
	changes = hub.ReapStale(now)
	if !findChange(changes, "bob", PresenceIdle) {
		t.Fatalf("no online→idle in %+v", changes)
	}
	// Alice also idles here; skip to Bob's frame.
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		m = readOne(t, a, time.Until(deadline))
		if m.UserID == "bob" {
			break
		}
	}
	if m.Status != PresenceIdle {
		t.Fatalf("idle frame = %+v", m)
	}
	if got := hub.PresenceSnapshot("p1", "s1")["bob"]; got != PresenceIdle {
		t.Fatalf("snapshot bob = %q, want idle", got)
	}

	// Keep Alice alive so only Bob is reaped below: her action refreshes
	// lastActive (and broadcasts one event frame to drain).
	if err := hub.HandleClientMessage(a, []byte(`{"type":"action","event_type":"MESSAGE_SENT","payload":{"text":"still here"}}`)); err != nil {
		t.Fatalf("alice action: %v", err)
	}
	drainClient(a)
	drainClient(b)

	// Silence past OfflineAfter → evicted + offline announced.
	now = base.Add(120 * time.Millisecond)
	changes = hub.ReapStale(now)
	if !findChange(changes, "bob", PresenceOffline) {
		t.Fatalf("no →offline in %+v", changes)
	}
	m = readOne(t, a, 300*time.Millisecond)
	if m.UserID != "bob" || m.Status != PresenceOffline {
		t.Fatalf("offline frame = %+v", m)
	}
	if hub.ClientCount() != 1 {
		t.Fatalf("count = %d, want 1 (alice survives)", hub.ClientCount())
	}
	if _, ok := hub.PresenceSnapshot("p1", "s1")["bob"]; ok {
		t.Fatal("bob still in snapshot after eviction")
	}
}

// TestPresenceHeartbeatRevival: an idle client revives to online on
// heartbeat (pong path) and the revival is announced.
func TestPresenceHeartbeatRevival(t *testing.T) {
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	now := base
	hub := NewHub(HubOptions{
		Now:           func() time.Time { return now },
		IdleAfter:     50 * time.Millisecond,
		TypingTimeout: 20 * time.Millisecond,
		OfflineAfter:  10 * time.Second,
	})
	a := hub.NewClient("alice", "p1", "s1", nil)
	b := hub.NewClient("bob", "p1", "s1", nil)
	hub.Register(a)
	hub.Register(b)
	drainClient(a)
	drainClient(b)

	now = base.Add(100 * time.Millisecond) // past IdleAfter (50ms), well below OfflineAfter (10s)
	changes := hub.ReapStale(now)
	if !findChange(changes, "bob", PresenceIdle) {
		t.Fatalf("bob did not idle: %+v", changes)
	}
	drainClient(a)

	hub.NoteHeartbeat("bob", "p1", "s1")
	if got := hub.PresenceSnapshot("p1", "s1")["bob"]; got != PresenceOnline {
		t.Fatalf("snapshot bob = %q, want online after heartbeat", got)
	}
	m := readOne(t, a, 300*time.Millisecond)
	if m.UserID != "bob" || m.Status != PresenceOnline {
		t.Fatalf("revival frame = %+v", m)
	}
}

// ---------------------------------------------------------------------------
// Client message validation
// ---------------------------------------------------------------------------

// TestWSClientMessageValidation: malformed/unknown/underspecified frames are
// rejected (→ "error" reply upstream); well-formed ones apply.
func TestWSClientMessageValidation(t *testing.T) {
	hub := NewHub(HubOptions{})
	c := hub.NewClient("alice", "", "", nil)
	hub.Register(c)

	bad := []struct {
		name string
		raw  string
		want string
	}{
		{"not json", `{`, "invalid JSON"},
		{"unknown type", `{"type":"teleport"}`, "unknown message type"},
		{"empty type", `{}`, "unknown message type"},
		{"subscribe w/o project", `{"type":"subscribe"}`, "subscribe requires project_id"},
		{"presence w/o subscribe", `{"type":"presence","status":"typing"}`, "subscribe before sending presence"},
		{"action w/o subscribe", `{"type":"action","event_type":"MESSAGE_SENT"}`, "subscribe before sending actions"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := hub.HandleClientMessage(c, []byte(tc.raw))
			if err == nil {
				t.Fatal("accepted, want error")
			}
			if got := string(NewErrorMessage(err.Error())); got == "" {
				t.Fatal("error frame is empty")
			}
		})
	}

	// Subscribe, then presence/action-scoped rejections.
	if err := hub.HandleClientMessage(c, []byte(`{"type":"subscribe","project_id":"p1","session_id":"s1"}`)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bad presence", `{"type":"presence","status":"offline"}`},
		{"action w/o type", `{"type":"action"}`},
		{"action bad type", `{"type":"action","event_type":"NOPE"}`},
		{"action bad json", `{"type":"action","event_type":"MESSAGE_SENT","payload":{"a":}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := hub.HandleClientMessage(c, []byte(tc.raw)); err == nil {
				t.Fatal("accepted, want error")
			}
		})
	}

	// Well-formed frames apply: presence flips status, action fans out.
	if err := hub.HandleClientMessage(c, []byte(`{"type":"presence","status":"typing"}`)); err != nil {
		t.Fatalf("presence: %v", err)
	}
	if got := c.Status(); got != PresenceTyping {
		t.Fatalf("status = %q, want typing", got)
	}
	drainClient(c)
	if err := hub.HandleClientMessage(c, []byte(`{"type":"action","event_type":"MESSAGE_SENT","payload":{"text":"hi"}}`)); err != nil {
		t.Fatalf("action: %v", err)
	}
	found := false
	for {
		select {
		case raw := <-c.Send:
			var m ServerMessage
			_ = json.Unmarshal(raw, &m)
			if m.Type == MsgEvent {
				found = true
			}
		default:
			goto done
		}
	}
done:
	if !found {
		t.Fatal("action did not fan out an event frame to the sender's scope")
	}
}

// ---------------------------------------------------------------------------
// Upgrade + JWT auth (stub transport)
// ---------------------------------------------------------------------------

// TestWSUpgradeAuth: /ws rejects missing/bad JWTs with 401, accepts both
// the Authorization header and the ?token= browser fallback, registers the
// client with its query subscription, and unregisters on disconnect.
func TestWSUpgradeAuth(t *testing.T) {
	h := newHarness(t)
	h.srv.EnableWS()
	h.srv.EnableWS() // idempotent: no panic, no duplicate pattern

	old := wsUpgrader
	defer func() { wsUpgrader = old }()
	stub := &stubUpgrader{}
	wsUpgrader = stub
	hub := h.srv.Hub()

	serve := func(target string, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := serve("/ws", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	if rec := serve("/ws?token=bogus", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: status = %d, want 401", rec.Code)
	}
	if rec := serve("/ws", "Bearer bogus"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad header token: status = %d, want 401", rec.Code)
	}

	// Query-token path (no Authorization header): the connection lives in
	// the stub, so the handler blocks in its read loop — run it async.
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve("/ws?token="+h.token+"&project_id=p1&session_id=s1", "")
	}()
	waitFor(t, time.Second, func() bool { return hub.ClientCount() == 1 }, "query-token client registered")
	if stub.count() != 1 {
		t.Fatalf("upgrades = %d, want 1", stub.count())
	}
	if got := hub.PresenceSnapshot("p1", "s1")["user-1"]; got != PresenceOnline {
		t.Fatalf("snapshot user-1 = %q, want online", got)
	}

	// Header path works too.
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		serve("/ws?project_id=p1", "Bearer "+h.token)
	}()
	waitFor(t, time.Second, func() bool { return hub.ClientCount() == 2 }, "header client registered")

	// Disconnects unregister (offline announced, pumps unwind).
	stub.mu.Lock()
	conns := append([]*fakeWSConn(nil), stub.conns...)
	stub.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	waitFor(t, time.Second, func() bool { return hub.ClientCount() == 0 }, "clients unregistered")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("query-token handler did not return after close")
	}
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("header handler did not return after close")
	}
}
