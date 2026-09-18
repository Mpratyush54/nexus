// Package server — ws.go: real-time WebSocket hub (Phase 3, nexus issue #13).
//
// Transport is stdlib-only on purpose (no nhooyr.io/websocket, no
// golang.org/x/net): the handshake is plain net/http + Hijacker, frames are
// minimal RFC 6455 (text/close/ping/pong), and the Hub itself is transport
// agnostic so unit tests exercise fan-out without opening sockets.
//
// Wire-up: call s.AttachHub(h) once; it registers "GET /ws" on the server
// mux. Auth reuses the JWT stub (Authorization: Bearer <token>, or ?token=
// for browser WebSocket clients that cannot set headers).
package server

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- protocol ---------------------------------------------------------------

// Client → server message types.
const (
	WSMsgSubscribe = "subscribe" // {project_id, session_id?}
	WSMsgAction    = "action"    // {event_type, payload} — fanned out as event
	WSMsgPresence  = "presence"  // {status: online|typing|idle|offline}
)

// Server → client message types.
const (
	WSMsgEvent         = "event"          // {event_type, payload, user_id}
	WSMsgPresenceEvent = "presence"       // {user_id, status}
	WSMsgMemoryUpdate  = "memory_update"  // {item, action: proposed|confirmed|rejected}
	WSMsgEpisodeUpdate = "episode_update" // {episode, action: opened|updated|resolved}
	WSMsgSubscribed    = "subscribed"     // ack for subscribe
	WSMsgError         = "error"          // {message}
)

// Presence statuses (issue #13: online, typing, idle, offline).
const (
	PresenceOnline  = "online"
	PresenceTyping  = "typing"
	PresenceIdle    = "idle"
	PresenceOffline = "offline"
)

// WSMessage is the single JSON envelope in both directions. Fields are
// optional per type; see the constants above for which apply.
type WSMessage struct {
	Type      string         `json:"type"`
	ProjectID string         `json:"project_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	EventType string         `json:"event_type,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	UserID    string         `json:"user_id,omitempty"`
	Status    string         `json:"status,omitempty"`
	Action    string         `json:"action,omitempty"`
	Item      any            `json:"item,omitempty"`
	Episode   any            `json:"episode,omitempty"`
	Message   string         `json:"message,omitempty"`
}

// validPresenceStatus reports whether s is a known presence status.
func validPresenceStatus(s string) bool {
	switch s {
	case PresenceOnline, PresenceTyping, PresenceIdle, PresenceOffline:
		return true
	}
	return false
}

// --- hub --------------------------------------------------------------------

// SendBufferSize bounds each client's outbound queue. Broadcasts never block:
// a full queue means the connection is too slow, so the message is dropped
// and counted (see Dropped) instead of stalling the hub.
const SendBufferSize = 64

// TypingTTL is how long a "typing" status stays live without a refresh.
const TypingTTL = 6 * time.Second

// Client is one connected socket (or one in-memory test endpoint).
type Client struct {
	ID        string
	UserID    string
	TokenID   string // api token id when auth was via nxs_*; empty for JWT sessions
	ProjectID string
	SessionID string

	status      string
	typingUntil time.Time
	lastSeen    time.Time

	Send chan []byte // outbound frames (JSON-encoded WSMessage)

	// mu guards closed; wmu serializes all socket frame writes
	// (wsWriteLoop data frames vs wsReadLoop pong/close replies).
	mu     sync.Mutex
	closed bool
	wmu    sync.Mutex

	dropped atomic.Int64
}

// safeSend queues raw without panicking on closed channels.
func (c *Client) safeSend(raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.Send <- raw:
	default:
		c.dropped.Add(1)
	}
}

// markClosed flags the client closed; caller must hold h.mu.Lock and must
// close(c.Send) exactly once after marking.
func (c *Client) markClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// Status returns the client's effective presence (expired typing demotes to
// online so UIs never stick on "typing…" after a disconnect).
func (c *Client) Status(now time.Time) string {
	if c.status == PresenceTyping && !c.typingUntil.IsZero() && now.After(c.typingUntil) {
		return PresenceOnline
	}
	return c.status
}

// Hub fans events and presence out to subscribed clients. All methods are
// safe for concurrent use.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client
	authz   WSAuthorizer
	// throttle gates inbound actions (issue #93). Nil allows all.
	throttle func(userID, projectID string) bool
	// steerHook routes steering actions through the steering manager
	// (issue #42). Nil falls through to broadcast.
	steerHook SteerHook

	now func() time.Time // overridable in tests
}

// SteerHook routes one action through an external state machine (steering).
// Returning handled=true means the hook owned the message (replies/fan-out
// done by the hook); handled=false falls through to broadcast.
type SteerHook func(clientID, userID, eventType string, payload map[string]any) (handled bool, err error)

// SetSteerHook sets the steering action hook. Nil restores broadcast-only.
func (h *Hub) SetSteerHook(fn SteerHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steerHook = fn
}

func (h *Hub) getSteerHook() SteerHook {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.steerHook
}

// SetThrottle sets the inbound-action rate gate. Nil restores allow-all.
func (h *Hub) SetThrottle(fn func(userID, projectID string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.throttle = fn
}

func (h *Hub) throttled(userID, projectID string) bool {
	h.mu.RLock()
	fn := h.throttle
	h.mu.RUnlock()
	if fn == nil {
		return false
	}
	return !fn(userID, projectID)
}

var (
	errWSProjectRequired = errWSError("project_id is required")
	errWSForbidden       = errWSError("not authorized for project/session scope")
	errWSRateLimited     = errWSError("rate limit exceeded")
)

type errWSError string

func (e errWSError) Error() string { return string(e) }

// NewHub returns an empty hub using real time.
func NewHub() *Hub {
	return &Hub{clients: make(map[string]*Client), now: time.Now}
}

// Add registers a client (status → online, lastSeen → now). A duplicate ID
// gracefully retires the previous client so its goroutines exit instead of
// leaking (issue #101).
func (h *Hub) Add(c *Client) {
	if c.Send == nil {
		c.Send = make(chan []byte, SendBufferSize)
	}
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.clients[c.ID]; ok && old != c {
		old.markClosed()
		func() {
			defer func() { _ = recover() }()
			close(old.Send)
		}()
	}
	c.mu.Lock()
	c.closed = false
	c.mu.Unlock()
	c.status = PresenceOnline
	c.lastSeen = now
	h.clients[c.ID] = c
}

// Remove unregisters a client and closes its Send channel exactly once.
func (h *Hub) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[id]; ok {
		delete(h.clients, id)
		c.markClosed()
		func() {
			defer func() { _ = recover() }()
			close(c.Send)
		}()
	}
}

// DropByToken forcibly disconnects every client bound to an API token id
// (issue #161 instant revoke). Returns how many sockets were dropped.
func (h *Hub) DropByToken(tokenID string) int {
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" || h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for id, c := range h.clients {
		if c.TokenID != tokenID {
			continue
		}
		delete(h.clients, id)
		c.markClosed()
		func() {
			defer func() { _ = recover() }()
			close(c.Send)
		}()
		n++
	}
	return n
}

// ClientCount reports the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// PresenceSnapshot is one live user on a project channel.
type PresenceSnapshot struct {
	UserID    string `json:"user_id"`
	Status    string `json:"status"`
	ProjectID string `json:"project_id,omitempty"`
}

// PresenceForProject lists unique online/typing/idle users subscribed to
// projectID. Offline clients are omitted. A nil hub returns nil.
func (h *Hub) PresenceForProject(projectID string) []PresenceSnapshot {
	if h == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	now := h.now()
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := make(map[string]PresenceSnapshot)
	for _, c := range h.clients {
		if c == nil || strings.TrimSpace(c.UserID) == "" || c.ProjectID != projectID {
			continue
		}
		st := c.Status(now)
		if st == PresenceOffline {
			continue
		}
		prev, ok := seen[c.UserID]
		if ok && prev.Status == PresenceTyping {
			continue
		}
		seen[c.UserID] = PresenceSnapshot{UserID: c.UserID, Status: st, ProjectID: c.ProjectID}
	}
	out := make([]PresenceSnapshot, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

// Get returns the client with id, or nil.
func (h *Hub) Get(id string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[id]
}

// Dropped reports how many messages were shed for a slow client.
func (h *Hub) Dropped(id string) int64 {
	if c := h.Get(id); c != nil {
		return c.dropped.Load()
	}
	return 0
}

// Subscribe scopes a client to a project and optional session channel.
// Authorization (issue #90) runs before mutation: the client's UserID is
// checked against the requested scope via the hub authorizer.
func (h *Hub) Subscribe(id, projectID, sessionID string) error {
	if strings.TrimSpace(projectID) == "" {
		return errors.New("project_id is required")
	}
	h.mu.RLock()
	c, ok := h.clients[id]
	userID := ""
	if ok {
		userID = c.UserID
	}
	h.mu.RUnlock()
	if !ok {
		return errors.New("client not connected")
	}
	if err := h.authorize(userID, projectID, sessionID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok = h.clients[id]
	if !ok {
		return errors.New("client not connected")
	}
	c.ProjectID = projectID
	c.SessionID = sessionID
	c.lastSeen = h.now()
	return nil
}

// Heartbeat refreshes a client's liveness clock (called on any inbound
// frame, plus daemon heartbeats forwarded by the server).
func (h *Hub) Heartbeat(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[id]; ok {
		c.lastSeen = h.now()
	}
}

// SetPresence updates a client's status and fans the change out to everyone
// sharing its project or session (excluding the client itself).
func (h *Hub) SetPresence(id, status string) error {
	if !validPresenceStatus(status) {
		return fmt.Errorf("unknown presence status %q", status)
	}
	now := h.now()
	h.mu.Lock()
	c, ok := h.clients[id]
	if !ok {
		h.mu.Unlock()
		return errors.New("client not connected")
	}
	c.status = status
	c.lastSeen = now
	if status == PresenceTyping {
		c.typingUntil = now.Add(TypingTTL)
	} else {
		c.typingUntil = time.Time{}
	}
	msg := WSMessage{Type: WSMsgPresenceEvent, UserID: c.UserID, Status: status,
		ProjectID: c.ProjectID, SessionID: c.SessionID}
	h.mu.Unlock()
	h.broadcast(msg, c.ProjectID, c.SessionID, id)
	return nil
}

// Broadcast delivers msg to every client matching projectID or sessionID
// (empty scope fields are wildcards that match nothing — pass the sender's
// scope explicitly). Each client receives at most one copy. Sends never
// block: slow clients drop the message and their drop counter increments.
func (h *Hub) Broadcast(msg WSMessage, projectID, sessionID string) {
	h.broadcast(msg, projectID, sessionID, "")
}

// BroadcastToProject delivers msg to all clients subscribed to projectID.
func (h *Hub) BroadcastToProject(projectID string, msg WSMessage) {
	h.broadcast(msg, projectID, "", "")
}

// BroadcastToSession delivers msg to all clients in sessionID.
func (h *Hub) BroadcastToSession(sessionID string, msg WSMessage) {
	h.broadcast(msg, "", sessionID, "")
}

// PublishEvent is the fan-out entry for freshly appended events: one copy
// per interested client, whether they subscribed by project or by session.
func (h *Hub) PublishEvent(projectID, sessionID, eventType string, payload map[string]any, userID string) {
	h.broadcast(WSMessage{Type: WSMsgEvent, ProjectID: projectID, SessionID: sessionID,
		EventType: eventType, Payload: payload, UserID: userID},
		projectID, sessionID, "")
}

// PublishMemoryUpdate fans out a memory lifecycle change.
func (h *Hub) PublishMemoryUpdate(projectID, sessionID string, item any, action string) {
	h.broadcast(WSMessage{Type: WSMsgMemoryUpdate, ProjectID: projectID, SessionID: sessionID,
		Item: item, Action: action}, projectID, sessionID, "")
}

// PublishEpisodeUpdate fans out an episode lifecycle change.
func (h *Hub) PublishEpisodeUpdate(projectID, sessionID string, episode any, action string) {
	h.broadcast(WSMessage{Type: WSMsgEpisodeUpdate, ProjectID: projectID, SessionID: sessionID,
		Episode: episode, Action: action}, projectID, sessionID, "")
}

func (h *Hub) broadcast(msg WSMessage, projectID, sessionID, excludeID string) {
	if projectID == "" && sessionID == "" {
		return
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		if c.ID == excludeID {
			continue
		}
		match := (projectID != "" && c.ProjectID == projectID) ||
			(sessionID != "" && c.SessionID == sessionID)
		if !match {
			continue
		}
		// Send while holding RLock so Remove (Lock) cannot close the
		// channel mid-send; safeSend additionally guards the closed flag.
		c.safeSend(raw)
	}
}

// HandleClientMessage routes one inbound JSON message from id. Subscribe and
// presence mutate hub state; action is fanned out as an event to the
// sender's project/session scope. Replies (ack/error) go to the sender only.
func (h *Hub) HandleClientMessage(id string, raw []byte) error {
	var msg WSMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		h.reply(id, WSMessage{Type: WSMsgError, Message: "invalid JSON: " + err.Error()})
		return err
	}
	switch msg.Type {
	case WSMsgSubscribe:
		if err := h.Subscribe(id, msg.ProjectID, msg.SessionID); err != nil {
			h.reply(id, WSMessage{Type: WSMsgError, Message: err.Error()})
			return err
		}
		h.reply(id, WSMessage{Type: WSMsgSubscribed, ProjectID: msg.ProjectID, SessionID: msg.SessionID})
		return nil
	case WSMsgPresence:
		if err := h.SetPresence(id, msg.Status); err != nil {
			h.reply(id, WSMessage{Type: WSMsgError, Message: err.Error()})
			return err
		}
		return nil
	case WSMsgAction:
		if strings.TrimSpace(msg.EventType) == "" {
			err := errors.New("event_type is required for action")
			h.reply(id, WSMessage{Type: WSMsgError, Message: err.Error()})
			return err
		}
		c := h.Get(id)
		if c == nil {
			return errors.New("client not connected")
		}
		// Authorize the sender's current scope on every action (issue #90):
		// a client that never subscribed (or was moved) cannot inject.
		if err := h.authorize(c.UserID, c.ProjectID, c.SessionID); err != nil {
			h.reply(id, WSMessage{Type: WSMsgError, Message: err.Error()})
			return err
		}
		if h.throttled(c.UserID, c.ProjectID) {
			h.reply(id, WSMessage{Type: WSMsgError, Message: "rate limit exceeded"})
			return errWSRateLimited
		}
		// Steering actions ride the same envelope (issue #42): the hook
		// drives the manager + emitter and owns replies/fan-out.
		if hook := h.getSteerHook(); hook != nil {
			handled, herr := hook(c.ID, c.UserID, msg.EventType, msg.Payload)
			if herr != nil {
				h.reply(id, WSMessage{Type: WSMsgError, Message: herr.Error()})
				return herr
			}
			if handled {
				h.Heartbeat(id)
				return nil
			}
		}
		h.broadcast(WSMessage{Type: WSMsgEvent, ProjectID: c.ProjectID, SessionID: c.SessionID,
			EventType: msg.EventType, Payload: msg.Payload, UserID: c.UserID},
			c.ProjectID, c.SessionID, id)
		h.Heartbeat(id)
		return nil
	default:
		err := fmt.Errorf("unknown message type %q", msg.Type)
		h.reply(id, WSMessage{Type: WSMsgError, Message: err.Error()})
		return err
	}
}

func (h *Hub) reply(id string, msg WSMessage) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	// Hold RLock across the send so Remove cannot close mid-send.
	h.mu.RLock()
	defer h.mu.RUnlock()
	if c, ok := h.clients[id]; ok {
		c.safeSend(raw)
	}
}

// SweepOffline marks clients silent for longer than timeout as offline and
// fans out their offline presence; it also demotes stale "typing" to
// "online" so indicators clear. It returns the IDs it marked offline.
func (h *Hub) SweepOffline(timeout time.Duration) []string {
	now := h.now()
	type marked struct {
		clientID, userID, projectID, sessionID string
		typingDemote                           bool
	}
	var out []marked
	h.mu.Lock()
	for _, c := range h.clients {
		switch {
		case c.status != PresenceOffline && now.Sub(c.lastSeen) > timeout:
			c.status = PresenceOffline
			out = append(out, marked{c.ID, c.UserID, c.ProjectID, c.SessionID, false})
		case c.status == PresenceTyping && !c.typingUntil.IsZero() && now.After(c.typingUntil):
			c.status = PresenceOnline
			out = append(out, marked{c.ID, c.UserID, c.ProjectID, c.SessionID, true})
		}
	}
	h.mu.Unlock()
	var offlineIDs []string
	for _, m := range out {
		status := PresenceOffline
		if m.typingDemote {
			status = PresenceOnline
		} else {
			offlineIDs = append(offlineIDs, m.clientID)
		}
		h.broadcast(WSMessage{Type: WSMsgPresenceEvent, UserID: m.userID,
			Status: status, ProjectID: m.projectID, SessionID: m.sessionID},
			m.projectID, m.sessionID, m.clientID)
	}
	return offlineIDs
}

// --- HTTP wiring ------------------------------------------------------------

// AttachHub registers the WebSocket endpoint on the server mux. It is
// separate from registerRoutes so tests can opt in without changing the
// Phase 1 route table in routes.go. It also wires the hub's authz (issue
// #90) and rate-limit throttle (issue #93) to this server's store/gates.
func (s *Server) AttachHub(h *Hub) {
	h.AuthorizeHub(NewStoreAuthorizer(s.Store))
	s.steerMu.Lock()
	s.hub = h
	s.steerMu.Unlock()
	h.SetThrottle(func(_, projectID string) bool {
		if projectID == "" {
			projectID = "global"
		}
		return s.eventAllowed(projectID)
	})
	s.Mux.HandleFunc("GET /ws", s.serveWS(h))
}

// serveWS authenticates, upgrades to WebSocket (stdlib Hijack), and pumps
// messages between the socket and the hub.
//
// Token transport (issue #134): browsers cannot set Authorization headers
// on native WebSocket, so ?token= is accepted as a fallback (the dashboard
// depends on it). REST routes reject query tokens. Server request logging
// records r.URL.Path only (never the query), and operators terminating TLS
// at a reverse proxy should strip/log accordingly.
func (s *Server) serveWS(h *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" && token != "" {
			authHeader = "Bearer " + token
		}
		// Reuse resolveBearer so API tokens (nxs_*) work on WS too.
		req := r.Clone(r.Context())
		req.Header.Set("Authorization", authHeader)
		id, err := s.resolveBearer(req)
		if err != nil {
			if err == ErrExpiredToken {
				writeError(w, http.StatusUnauthorized, "token expired")
				return
			}
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		conn, rw, err := upgradeToWebSocket(w, r)
		if err != nil {
			// upgradeToWebSocket already wrote the failure status.
			s.Log.Printf("ws upgrade failed for %q: %v", id.Sub, err)
			return
		}
		c := &Client{
			ID:      newWSClientID(),
			UserID:  id.Sub,
			TokenID: id.TokenID,
			Send:    make(chan []byte, SendBufferSize),
		}
		h.Add(c)
		defer func() {
			h.Remove(c.ID)
			// Tell the room this user went offline (best effort).
			h.broadcast(WSMessage{Type: WSMsgPresenceEvent, UserID: c.UserID,
				Status: PresenceOffline, ProjectID: c.ProjectID, SessionID: c.SessionID},
				c.ProjectID, c.SessionID, "")
		}()
		// Online announcement happens on subscribe (scope is known then).
		go wsWriteLoop(conn, rw, c)
		wsReadLoop(conn, rw, h, c)
	}
}

// --- minimal RFC 6455 -------------------------------------------------------

const wsGUID = "258EAFA5-E914-47DA-95CA-C594E22C51336"

// MaxWSMessageBytes caps one inbound frame payload (DoS guard).
const MaxWSMessageBytes = 1 << 20

// wsPingPeriod sets how often the server pings idle sockets; wsPongWait is
// how long a pong may take before the read loop drops the connection.
const (
	wsPingPeriod = 30 * time.Second
	wsPongWait   = 90 * time.Second
)

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
)

var errWSClosed = errors.New("websocket closed")

// upgradeToWebSocket validates the upgrade request, hijacks the connection,
// and completes the 101 handshake. On validation failure it writes the HTTP
// error itself and returns a non-nil error.
func upgradeToWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "websocket requires GET")
		return nil, nil, errors.New("method must be GET")
	}
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		writeError(w, http.StatusUpgradeRequired, "websocket upgrade required")
		return nil, nil, errors.New("not a websocket upgrade request")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		writeError(w, http.StatusBadRequest, "unsupported websocket version (want 13)")
		return nil, nil, errors.New("unsupported websocket version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing Sec-WebSocket-Key")
		return nil, nil, errors.New("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "hijack not supported")
		return nil, nil, errors.New("hijack not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

func headerHasToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// wsReadLoop pumps client → hub until error/close. Every frame refreshes the
// client's heartbeat; ping is answered with pong; close is echoed.
// Fragmented text messages (RFC 6455 §5.4) are reassembled in frag and only
// dispatched to HandleClientMessage once the final (FIN=1) frame arrives.
// Control frames (ping/pong/close) are handled inline and never buffered
// into frag, so they may appear interleaved between fragments.
func wsReadLoop(conn net.Conn, rw *bufio.ReadWriter, h *Hub, c *Client) {
	defer conn.Close()
	var frag []byte // reassembly buffer for fragmented text messages
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		op, fin, payload, err := wsReadFrame(rw)
		if err != nil {
			return
		}
		h.Heartbeat(c.ID)
		if op >= wsOpClose && !fin {
			// Control frames MUST NOT be fragmented (RFC 6455 §5.5).
			return
		}
		switch op {
		case wsOpText, wsOpContinuation:
			if op == wsOpText {
				if frag != nil {
					// New data frame while reassembly is in progress:
					// protocol error per RFC 6455 §5.4 (a new message
					// must not start before the previous one finishes).
					// Drop the incomplete buffer and start over with
					// this frame so one bad peer cannot wedge the loop.
					frag = nil
				}
				if !fin {
					// First fragment: buffer and wait for continuations.
					// Ensure frag is non-nil even for empty payloads so
					// a later continuation can tell "started" from "idle".
					if frag == nil {
						frag = make([]byte, 0, len(payload))
					}
					frag = append(frag, payload...)
					if len(frag) > MaxWSMessageBytes {
						frag = nil // shed oversize reassembly, keep loop alive
					}
					continue
				}
				// Unfragmented text message (no reassembly in progress).
				_ = h.HandleClientMessage(c.ID, payload)
			} else {
				// Continuation frame.
				if frag == nil {
					// Continuation with nothing to continue:
					// protocol error per RFC 6455 §5.4 — ignore the frame.
					continue
				}
				frag = append(frag, payload...)
				if len(frag) > MaxWSMessageBytes {
					frag = nil // shed oversize reassembly, keep loop alive
					continue
				}
				if !fin {
					continue // more fragments to come
				}
				msg := frag
				frag = nil
				_ = h.HandleClientMessage(c.ID, msg)
			}
		case wsOpPing:
			c.wmu.Lock()
			_ = wsWriteFrame(rw, wsOpPong, payload)
			c.wmu.Unlock()
		case wsOpPong:
			// Heartbeat already refreshed above.
		case wsOpClose:
			c.wmu.Lock()
			_ = wsWriteFrame(rw, wsOpClose, payload)
			c.wmu.Unlock()
			return
		}
	}
}

// wsWriteLoop pumps hub → client, plus periodic pings. It exits when Send is
// closed (hub Remove) or the socket fails. All frame writes serialize on
// c.wmu with wsReadLoop control replies (issue #101).
func wsWriteLoop(conn net.Conn, rw *bufio.ReadWriter, c *Client) {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	defer conn.Close()
	for {
		select {
		case msg, ok := <-c.Send:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			c.wmu.Lock()
			err := wsWriteFrame(rw, wsOpText, msg)
			c.wmu.Unlock()
			if err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			c.wmu.Lock()
			err := wsWriteFrame(rw, wsOpPing, nil)
			c.wmu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// wsReadFrame reads one client→server frame (which must be masked per
// RFC 6455 §5.3) and returns its opcode, FIN bit, and unmasked payload.
// Control frames are limited to 125 bytes by the protocol.
func wsReadFrame(rw *bufio.ReadWriter) (op byte, fin bool, payload []byte, err error) {
	hdr, err := readExact(rw, 2)
	if err != nil {
		return 0, false, nil, err
	}
	if hdr[0]&0x70 != 0 {
		return 0, false, nil, errors.New("unsupported websocket RSV bits")
	}
	fin = hdr[0]&0x80 != 0
	op = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7F)
	switch length {
	case 126:
		ext, err := readExact(rw, 2)
		if err != nil {
			return 0, false, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext, err := readExact(rw, 8)
		if err != nil {
			return 0, false, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext))
		if length < 0 {
			return 0, false, nil, errors.New("oversize websocket frame")
		}
	}
	if op >= wsOpClose && length > 125 {
		return 0, false, nil, errors.New("oversize websocket control frame")
	}
	if length > MaxWSMessageBytes {
		return 0, false, nil, errors.New("websocket message too large")
	}
	if !masked {
		return 0, false, nil, errors.New("client frames must be masked")
	}
	mask, err := readExact(rw, 4)
	if err != nil {
		return 0, false, nil, err
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(rw, payload); err != nil {
		return 0, false, nil, errWSClosed
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	switch op {
	case wsOpText, wsOpContinuation, wsOpPing, wsOpPong, wsOpClose:
		return op, fin, payload, nil
	default:
		return 0, false, nil, fmt.Errorf("unsupported websocket opcode %d", op)
	}
}

// wsWriteFrame writes one server→client frame (never masked).
func wsWriteFrame(rw *bufio.ReadWriter, op byte, payload []byte) error {
	var hdr [10]byte
	hdr[0] = 0x80 | op // FIN + opcode
	n := 2
	switch l := len(payload); {
	case l <= 125:
		hdr[1] = byte(l)
	case l <= 65535:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(l))
		n = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(l))
		n = 10
	}
	if _, err := rw.Write(hdr[:n]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := rw.Write(payload); err != nil {
			return err
		}
	}
	return rw.Flush()
}

func readExact(rw *bufio.ReadWriter, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rw, buf); err != nil {
		return nil, errWSClosed
	}
	return buf, nil
}

func newWSClientID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("ws_%x", b[:])
}
