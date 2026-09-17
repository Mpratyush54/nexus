// WebSocket hub: live fan-out + presence (issue #13, plan §§3.2, 3.4).
//
// Protocol (§3.2, transcribed exactly — plus one pragmatic extension):
//
//	Client → Server
//	  {"type": "subscribe", "project_id": "...", "session_id": "..."}
//	  {"type": "action", "event_type": "MESSAGE_SENT", "payload": {...}}
//	  {"type": "presence", "status": "typing"}
//
//	Server → Client
//	  {"type": "event", "event": {/* event row or action envelope */}}
//	  {"type": "presence", "user_id": "...", "status": "online|typing|idle|offline"}
//	  {"type": "memory_update", "item": {/* memory item */}, "action": "proposed|confirmed|rejected"}
//	  {"type": "episode_update", "episode": {/* episode */}, "action": "opened|updated|resolved"}
//	  {"type": "error", "error": "..."}   // EXTENSION: per-message rejections
//	                                      // (bad subscribe/action/presence). The plan
//	                                      // defines no error shape; without one a client
//	                                      // can never tell a typo from a drop.
//
// Architecture (see docs/decisions/ADR-013-*.md for the why):
//
//   - Transport-agnostic Hub: Register/Unregister/Broadcast/PublishEvent (+
//     PublishPresence/PublishMemoryUpdate/PublishEpisodeUpdate) operate on
//     Client records with buffered Send channels. The hub never touches the
//     network; pumps in serveWSConn move bytes between Send and a WSConn.
//   - Thin upgrade adapter: the Upgrader interface (Upgrade → WSConn) is the
//     only seam between HTTP and the hub. The default hijackUpgrader speaks
//     RFC 6455 using only the stdlib (Hijacker + sha1 + framing) so this
//     issue adds ZERO dependencies (nhooyr.io/websocket is NOT in go.mod).
//     A future nhooyr/gorilla adapter implements the same 7-method WSConn.
//   - JWT auth happens BEFORE upgrade in handleWS: `Authorization: Bearer`
//     first, `?token=` query fallback (browsers cannot set WS headers).
//     GET /ws is therefore exempted from the withAuth middleware (see the
//     init below) and authenticates inside the handler — exactly one place.
//
// Routing: a message for (project, session) reaches a client iff the project
// matches AND (the message is project-wide OR the client is a project-level
// watcher OR the sessions match). Project-level watchers (empty session_id,
// e.g. dashboards) see everything in their project; session members see
// their session plus project-wide broadcasts.
//
// Backpressure: every Broadcast is non-blocking. A full Send buffer means
// the client is slow: the message is dropped, the client's drop counter
// increments, and past MaxDropsBeforeEvict the client is evicted (slow
// clients must never stall fan-out for everyone else).
//
// Presence (§3.4): online at register / any activity; typing on explicit
// presence messages (decays to online after TypingTimeout); idle after
// IdleAfter (5m) without activity; offline on unregister or on ReapStale
// past OfflineAfter (90s, shared with store.OfflineAfter). Heartbeats feed
// in via NoteHeartbeat (wire pong handler + future REST-heartbeat wiring).
//
// EXTENSION WITHOUT EDITS: this file touches no other server file. The
// route exemption uses init() on the same-package openPaths map, EnableWS
// registers GET /ws on the same-package mux, and per-Server hubs live in a
// sync.Map. server.go/routes.go are byte-identical (verify: git status).
package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
)

// init exempts the WebSocket endpoint from the generic Bearer middleware so
// handleWS can accept EITHER `Authorization: Bearer <JWT>` (native clients)
// OR `?token=<JWT>` (browsers, which cannot set headers on a WS upgrade).
// Auth is still mandatory — it just happens inside handleWS (verified with
// s.verifyToken against the server clock) instead of withAuth.
func init() {
	openPaths["GET /ws"] = true
}

// ---------------------------------------------------------------------------
// Protocol types (§3.2)
// ---------------------------------------------------------------------------

// Client → server message types.
const (
	MsgSubscribe = "subscribe"
	MsgAction    = "action"
	MsgPresence  = "presence"
)

// Server → client message types (MsgError is the documented extension).
const (
	MsgEvent         = "event"
	MsgMemoryUpdate  = "memory_update"
	MsgEpisodeUpdate = "episode_update"
	MsgError         = "error"
)

// Presence states (§3.4).
const (
	PresenceOnline  = "online"
	PresenceTyping  = "typing"
	PresenceIdle    = "idle"
	PresenceOffline = "offline"
)

// Wire timing. WSPingPeriod < WSPongWait so a live peer is always pinged
// before its read deadline fires.
const (
	WSWriteWait       = 10 * time.Second
	WSPongWait        = 60 * time.Second
	WSPingPeriod      = 30 * time.Second
	WSMaxMessageBytes = 64 * 1024
)

// Hub defaults: 5m idle matches plan §3.4 ("idle if no action for 5min").
const (
	WSDefaultSendBuffer = 64
	WSDefaultMaxDrops   = 16
	WSTypingTimeout     = 30 * time.Second
	WSIdleAfter         = 5 * time.Minute
)

// ErrWSClosed is returned by ReadText when the peer completes the close
// handshake (or the connection is gone).
var ErrWSClosed = errors.New("server: websocket connection closed")

// ClientMessage is one client → server frame (plan §3.2).
type ClientMessage struct {
	Type      string          `json:"type"`
	ProjectID string          `json:"project_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	EventType string          `json:"event_type,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Status    string          `json:"status,omitempty"`
}

// ServerMessage is one server → client frame (plan §3.2 + error extension).
type ServerMessage struct {
	Type    string          `json:"type"`
	Event   json.RawMessage `json:"event,omitempty"`
	UserID  string          `json:"user_id,omitempty"`
	Status  string          `json:"status,omitempty"`
	Item    json.RawMessage `json:"item,omitempty"`
	Episode json.RawMessage `json:"episode,omitempty"`
	Action  string          `json:"action,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// mustRaw marshals v for embedding in a ServerMessage. json.RawMessage and
// []byte pass through after a validity check so callers can forward
// store rows without re-encoding.
func mustRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, errors.New("server: ws payload is required")
	}
	switch t := v.(type) {
	case json.RawMessage:
		if len(t) == 0 {
			return json.RawMessage("{}"), nil
		}
		if !json.Valid(t) {
			return nil, errors.New("server: ws payload RawMessage is not valid JSON")
		}
		return t, nil
	case []byte:
		if len(t) == 0 {
			return json.RawMessage("{}"), nil
		}
		if !json.Valid(t) {
			return nil, errors.New("server: ws payload bytes are not valid JSON")
		}
		return json.RawMessage(t), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}

// NewEventMessage wraps an event row/envelope as an "event" frame.
func NewEventMessage(event any) ([]byte, error) {
	raw, err := mustRaw(event)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ServerMessage{Type: MsgEvent, Event: raw})
}

// NewPresenceMessage wraps a presence state as a "presence" frame.
func NewPresenceMessage(userID, status string) ([]byte, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("server: ws presence requires a user id")
	}
	switch status {
	case PresenceOnline, PresenceTyping, PresenceIdle, PresenceOffline:
	default:
		return nil, errors.New("server: ws unknown presence status " + status)
	}
	return json.Marshal(ServerMessage{Type: MsgPresence, UserID: userID, Status: status})
}

// allowedMemoryActions / allowedEpisodeActions mirror the §3.2 protocol
// vocabularies (server → client direction).
var (
	allowedMemoryActions = map[string]bool{
		"proposed": true, "confirmed": true, "rejected": true,
	}
	allowedEpisodeActions = map[string]bool{
		"opened": true, "updated": true, "resolved": true,
	}
)

// allowedClientPresence is what a client may CLAIM about itself. "offline"
// is server-asserted only (unregister/reap) — a client declaring itself
// offline is a protocol error.
var allowedClientPresence = map[string]bool{
	PresenceTyping: true, PresenceOnline: true, PresenceIdle: true,
}

// NewMemoryUpdateMessage wraps a memory item transition.
func NewMemoryUpdateMessage(item any, action string) ([]byte, error) {
	if !allowedMemoryActions[action] {
		return nil, errors.New("server: ws unknown memory action " + action)
	}
	raw, err := mustRaw(item)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ServerMessage{Type: MsgMemoryUpdate, Item: raw, Action: action})
}

// NewEpisodeUpdateMessage wraps an episode transition.
func NewEpisodeUpdateMessage(episode any, action string) ([]byte, error) {
	if !allowedEpisodeActions[action] {
		return nil, errors.New("server: ws unknown episode action " + action)
	}
	raw, err := mustRaw(episode)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ServerMessage{Type: MsgEpisodeUpdate, Episode: raw, Action: action})
}

// NewErrorMessage builds an "error" frame. It never fails: if errText is
// blank a generic message is used, and the struct always marshals.
func NewErrorMessage(errText string) []byte {
	if strings.TrimSpace(errText) == "" {
		errText = "invalid message"
	}
	b, _ := json.Marshal(ServerMessage{Type: MsgError, Error: errText})
	return b
}

// ---------------------------------------------------------------------------
// Hub (transport-agnostic fan-out + presence)
// ---------------------------------------------------------------------------

// HubOptions tunes a Hub. Zero value = production defaults. Tests shrink
// IdleAfter/TypingTimeout/OfflineAfter to drive transitions without sleeping.
type HubOptions struct {
	Now                 func() time.Time
	SendBufferSize      int
	MaxDropsBeforeEvict int
	IdleAfter           time.Duration
	TypingTimeout       time.Duration
	OfflineAfter        time.Duration
}

// Hub fans events out to subscribed clients. It performs no I/O itself: use
// Publish* to push bytes into client Send channels and serveWSConn pumps to
// move them over the wire. Safe for concurrent use.
type Hub struct {
	mu            sync.RWMutex
	clients       map[*Client]struct{}
	now           func() time.Time
	sendBuffer    int
	maxDrops      int
	idleAfter     time.Duration
	typingTimeout time.Duration
	offlineAfter  time.Duration
}

// NewHub builds a Hub over the given options (defaults filled in).
func NewHub(opts HubOptions) *Hub {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	h := &Hub{
		clients:       map[*Client]struct{}{},
		now:           now,
		sendBuffer:    opts.SendBufferSize,
		maxDrops:      opts.MaxDropsBeforeEvict,
		idleAfter:     opts.IdleAfter,
		typingTimeout: opts.TypingTimeout,
		offlineAfter:  opts.OfflineAfter,
	}
	if h.sendBuffer <= 0 {
		h.sendBuffer = WSDefaultSendBuffer
	}
	if h.maxDrops <= 0 {
		h.maxDrops = WSDefaultMaxDrops
	}
	if h.idleAfter <= 0 {
		h.idleAfter = WSIdleAfter
	}
	if h.typingTimeout <= 0 {
		h.typingTimeout = WSTypingTimeout
	}
	// OfflineAfter defaults to the store package's heartbeat budget so WS
	// presence and workspace presence share one definition of "silent".
	if h.offlineAfter <= 0 {
		h.offlineAfter = store.OfflineAfter
	}
	return h
}

// Client is one subscribed connection. Send is the backpressured outbox:
// the hub never blocks on it (see Broadcast). conn is nil in unit tests,
// which drive the hub synchronously through Send.
//
// Locking: ProjectID/SessionID are guarded by the hub mutex (mutated only
// by subscribe under hub lock, read by Broadcast under hub read lock —
// always hub-before-client ordering). Presence fields are guarded by mu.
type Client struct {
	ID        string
	ProjectID string
	SessionID string
	Send      chan []byte
	conn      WSConn
	hub       *Hub

	mu            sync.Mutex
	status        string
	lastActive    time.Time
	lastHeartbeat time.Time
	dropped       int

	once sync.Once
	done chan struct{}
}

// NewClient creates an unregistered client. Call Register to subscribe it.
func (h *Hub) NewClient(userID, projectID, sessionID string, conn WSConn) *Client {
	now := h.now()
	return &Client{
		ID:            strings.TrimSpace(userID),
		ProjectID:     strings.TrimSpace(projectID),
		SessionID:     strings.TrimSpace(sessionID),
		Send:          make(chan []byte, h.sendBuffer),
		conn:          conn,
		hub:           h,
		status:        PresenceOnline,
		lastActive:    now,
		lastHeartbeat: now,
		done:          make(chan struct{}),
	}
}

// Status reports the client's current presence state.
func (c *Client) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Dropped reports how many messages were shed for this client (backpressure
// signal; at MaxDropsBeforeEvict the hub evicts the client).
func (c *Client) Dropped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Done closes when the client is unregistered; wire pumps select on it.
func (c *Client) Done() <-chan struct{} { return c.done }

// matches reports whether a (project, session) message reaches c. Called
// with the hub lock held.
func (c *Client) matches(projectID, sessionID string) bool {
	if c.ProjectID != projectID {
		return false
	}
	if sessionID == "" {
		return true // project-wide broadcast reaches every project subscriber
	}
	if c.SessionID == "" {
		return true // project-level watcher (dashboard) sees every session
	}
	return c.SessionID == sessionID
}

// Register subscribes c and announces it online to matching peers.
func (h *Hub) Register(c *Client) {
	if c == nil || c.hub != h {
		return
	}
	now := h.now()
	h.mu.Lock()
	c.mu.Lock()
	c.status = PresenceOnline
	c.lastActive = now
	c.lastHeartbeat = now
	c.mu.Unlock()
	h.clients[c] = struct{}{}
	projectID, sessionID := c.ProjectID, c.SessionID
	h.mu.Unlock()

	if strings.TrimSpace(projectID) == "" {
		return // not yet subscribed; the first subscribe announces
	}
	// Best-effort: the newcomer itself may receive its own hello.
	_, _ = h.PublishPresence(c.ID, projectID, sessionID, PresenceOnline)
}

// Unregister removes c, signals its pumps via Done, and announces it
// offline to matching peers. Idempotent.
func (h *Hub) Unregister(c *Client) {
	if c == nil || c.hub != h {
		return
	}
	h.mu.Lock()
	delete(h.clients, c)
	projectID, sessionID, userID := c.ProjectID, c.SessionID, c.ID
	h.mu.Unlock()
	c.once.Do(func() { close(c.done) })

	if strings.TrimSpace(projectID) == "" {
		return
	}
	_, _ = h.PublishPresence(userID, projectID, sessionID, PresenceOffline)
}

// ClientCount reports the number of registered clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// trySend enqueues msg without blocking. On a full buffer the message is
// dropped and the client's drop counter grows. Called with h.mu held
// (read); eviction happens after unlock in Broadcast.
func (h *Hub) trySend(c *Client, msg []byte) bool {
	select {
	case c.Send <- msg:
		return true
	default:
		c.mu.Lock()
		c.dropped++
		c.mu.Unlock()
		return false
	}
}

// Broadcast pushes one pre-marshaled frame to every client matching
// (projectID, sessionID) and returns the delivered count. It never blocks:
// slow clients shed load (see trySend) and are evicted past the drop
// budget so one stalled TCP connection cannot stall fan-out.
func (h *Hub) Broadcast(projectID, sessionID string, msg []byte) int {
	if len(msg) == 0 {
		return 0
	}
	var evict []*Client
	delivered := 0
	h.mu.RLock()
	for c := range h.clients {
		if !c.matches(projectID, sessionID) {
			continue
		}
		if h.trySend(c, msg) {
			delivered++
			continue
		}
		c.mu.Lock()
		d := c.dropped
		c.mu.Unlock()
		if d >= h.maxDrops {
			evict = append(evict, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range evict {
		h.Unregister(c)
	}
	return delivered
}

// PublishEvent fans one event row/envelope out as an "event" frame.
// Returns receivers (0 = nobody subscribed — not an error).
func (h *Hub) PublishEvent(projectID, sessionID string, event any) (int, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, errors.New("server: ws publish event requires a project id")
	}
	msg, err := NewEventMessage(event)
	if err != nil {
		return 0, err
	}
	return h.Broadcast(strings.TrimSpace(projectID), strings.TrimSpace(sessionID), msg), nil
}

// PublishPresence fans one presence state out as a "presence" frame.
func (h *Hub) PublishPresence(userID, projectID, sessionID, status string) (int, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, errors.New("server: ws publish presence requires a project id")
	}
	msg, err := NewPresenceMessage(userID, status)
	if err != nil {
		return 0, err
	}
	return h.Broadcast(strings.TrimSpace(projectID), strings.TrimSpace(sessionID), msg), nil
}

// PublishMemoryUpdate fans one memory item transition as "memory_update".
func (h *Hub) PublishMemoryUpdate(projectID, sessionID string, item any, action string) (int, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, errors.New("server: ws publish memory_update requires a project id")
	}
	msg, err := NewMemoryUpdateMessage(item, action)
	if err != nil {
		return 0, err
	}
	return h.Broadcast(strings.TrimSpace(projectID), strings.TrimSpace(sessionID), msg), nil
}

// PublishEpisodeUpdate fans one episode transition as "episode_update".
func (h *Hub) PublishEpisodeUpdate(projectID, sessionID string, episode any, action string) (int, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, errors.New("server: ws publish episode_update requires a project id")
	}
	msg, err := NewEpisodeUpdateMessage(episode, action)
	if err != nil {
		return 0, err
	}
	return h.Broadcast(strings.TrimSpace(projectID), strings.TrimSpace(sessionID), msg), nil
}

// PresenceSnapshot maps user → status for clients matching
// (projectID, sessionID). Offline peers are absent (they are unregistered);
// callers treat absence as offline.
func (h *Hub) PresenceSnapshot(projectID, sessionID string) map[string]string {
	out := map[string]string{}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if !c.matches(projectID, sessionID) {
			continue
		}
		c.mu.Lock()
		st := c.status
		c.mu.Unlock()
		out[c.ID] = st
	}
	return out
}

// NoteHeartbeat marks a user's matching clients as seen-now (heartbeat
// path: wire pong handler today, REST-heartbeat wiring as follow-up). Idle
// clients revive to online and the revival is announced.
func (h *Hub) NoteHeartbeat(userID, projectID, sessionID string) {
	now := h.now()
	var revived []struct{ user, project, session string }
	h.mu.Lock()
	for c := range h.clients {
		if c.ID != userID || !c.matches(projectID, sessionID) {
			continue
		}
		c.mu.Lock()
		c.lastHeartbeat = now
		if c.status == PresenceIdle {
			c.status = PresenceOnline
			c.lastActive = now
			revived = append(revived, struct{ user, project, session string }{c.ID, c.ProjectID, c.SessionID})
		}
		c.mu.Unlock()
	}
	h.mu.Unlock()
	for _, r := range revived {
		_, _ = h.PublishPresence(r.user, r.project, r.session, PresenceOnline)
	}
}

// PresenceChange describes one ReapStale transition.
type PresenceChange struct {
	UserID    string
	ProjectID string
	SessionID string
	From      string
	To        string
}

// latestLocked returns the freshest liveness signal. Called with h.mu held.
func (c *Client) latestLocked() time.Time {
	if c.lastHeartbeat.After(c.lastActive) {
		return c.lastHeartbeat
	}
	return c.lastActive
}

// ReapStale sweeps presence in one pass at now: typing decays to online
// past TypingTimeout, silence past IdleAfter becomes idle, silence past
// OfflineAfter evicts (announced offline by Unregister). Transitions are
// broadcast after unlock; the returned changes let callers (and tests)
// observe exactly what moved.
func (h *Hub) ReapStale(now time.Time) []PresenceChange {
	var changes []PresenceChange
	var evict []*Client
	var announce []PresenceChange

	h.mu.Lock()
	for c := range h.clients {
		c.mu.Lock()
		silence := now.Sub(c.latestLocked())
		switch {
		case silence > h.offlineAfter:
			from := c.status
			c.status = PresenceOffline
			changes = append(changes, PresenceChange{
				UserID: c.ID, ProjectID: c.ProjectID, SessionID: c.SessionID,
				From: from, To: PresenceOffline,
			})
			evict = append(evict, c)
		case c.status == PresenceTyping && now.Sub(c.lastActive) > h.typingTimeout:
			c.status = PresenceOnline
			changes = append(changes, PresenceChange{
				UserID: c.ID, ProjectID: c.ProjectID, SessionID: c.SessionID,
				From: PresenceTyping, To: PresenceOnline,
			})
			announce = append(announce, PresenceChange{
				UserID: c.ID, ProjectID: c.ProjectID, SessionID: c.SessionID,
				To: PresenceOnline,
			})
		case c.status == PresenceOnline && now.Sub(c.lastActive) > h.idleAfter:
			c.status = PresenceIdle
			changes = append(changes, PresenceChange{
				UserID: c.ID, ProjectID: c.ProjectID, SessionID: c.SessionID,
				From: PresenceOnline, To: PresenceIdle,
			})
			announce = append(announce, PresenceChange{
				UserID: c.ID, ProjectID: c.ProjectID, SessionID: c.SessionID,
				To: PresenceIdle,
			})
		}
		c.mu.Unlock()
	}
	h.mu.Unlock()

	for _, a := range announce {
		_, _ = h.PublishPresence(a.UserID, a.ProjectID, a.SessionID, a.To)
	}
	for _, c := range evict {
		h.Unregister(c) // announces offline
	}
	return changes
}

// touchLocked records activity at now; idle clients revive to online.
// Caller must broadcast the revival (returned as revive=true with scope).
// Called with h.mu held (hub-before-client ordering).
func (c *Client) touchLocked(now time.Time) (revive bool, project, session, user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastActive = now
	c.lastHeartbeat = now
	if c.status == PresenceIdle {
		c.status = PresenceOnline
		return true, c.ProjectID, c.SessionID, c.ID
	}
	return false, "", "", ""
}

// HandleClientMessage routes one client → server frame and returns a
// human-readable error for the "error" reply frame (nil = applied).
func (h *Hub) HandleClientMessage(c *Client, raw []byte) error {
	if c == nil || c.hub != h {
		return errors.New("unknown client")
	}
	if len(raw) == 0 || len(raw) > WSMaxMessageBytes {
		return errors.New("empty or oversize message")
	}
	var m ClientMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	now := h.now()
	switch strings.TrimSpace(m.Type) {
	case MsgSubscribe:
		projectID := strings.TrimSpace(m.ProjectID)
		if projectID == "" {
			return errors.New("subscribe requires project_id")
		}
		sessionID := strings.TrimSpace(m.SessionID)
		h.mu.Lock()
		c.ProjectID = projectID
		c.SessionID = sessionID
		revive, rp, rs, ru := c.touchLocked(now)
		h.mu.Unlock()
		if revive {
			_, _ = h.PublishPresence(ru, rp, rs, PresenceOnline)
		}
		_, _ = h.PublishPresence(c.ID, projectID, sessionID, PresenceOnline)
		return nil

	case MsgPresence:
		status := strings.TrimSpace(m.Status)
		if !allowedClientPresence[status] {
			return errors.New("presence status must be typing, online or idle")
		}
		h.mu.RLock()
		projectID, sessionID := c.ProjectID, c.SessionID
		h.mu.RUnlock()
		if strings.TrimSpace(projectID) == "" {
			return errors.New("subscribe before sending presence")
		}
		c.mu.Lock()
		c.status = status
		c.lastActive = now
		c.lastHeartbeat = now
		c.mu.Unlock()
		_, _ = h.PublishPresence(c.ID, projectID, sessionID, status)
		return nil

	case MsgAction:
		eventType := strings.TrimSpace(m.EventType)
		if eventType == "" {
			return errors.New("action requires event_type")
		}
		if !store.IsValidEventType(eventType) {
			return errors.New("unknown event type " + eventType)
		}
		h.mu.Lock()
		projectID, sessionID := c.ProjectID, c.SessionID
		revive, rp, rs, ru := c.touchLocked(now)
		h.mu.Unlock()
		if strings.TrimSpace(projectID) == "" {
			return errors.New("subscribe before sending actions")
		}
		if revive {
			_, _ = h.PublishPresence(ru, rp, rs, PresenceOnline)
		}
		payload := m.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		if !json.Valid(payload) {
			return errors.New("action payload is not valid JSON")
		}
		envelope := map[string]any{
			"event_type": eventType,
			"payload":    json.RawMessage(payload),
			"project_id": projectID,
			"user_id":    c.ID,
			"created_at": now.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
		if strings.TrimSpace(sessionID) != "" {
			envelope["session_id"] = sessionID
		}
		_, err := h.PublishEvent(projectID, sessionID, envelope)
		return err

	default:
		return errors.New("unknown message type " + strings.TrimSpace(m.Type))
	}
}

// ---------------------------------------------------------------------------
// Upgrade adapter seam (stdlib-only default)
// ---------------------------------------------------------------------------

// WSConn is the 7-method transport surface the pumps need. Any library
// (nhooyr.io/websocket, gorilla, stdlib hijack below) adapts to it.
type WSConn interface {
	ReadText() ([]byte, error)
	WriteText([]byte) error
	Ping() error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	SetPongHandler(func())
	Close() error
}

// Upgrader performs the HTTP → WebSocket upgrade. The default
// (hijackUpgrader) is stdlib-only; tests substitute a stub.
type Upgrader interface {
	Upgrade(w http.ResponseWriter, r *http.Request) (WSConn, error)
}

// wsUpgrader is the process-wide adapter (swappable in tests).
var wsUpgrader Upgrader = &hijackUpgrader{}

// wsGUID is the RFC 6455 magic concatenated with Sec-WebSocket-Key.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// hijackUpgrader upgrades via net/http.Hijacker and speaks RFC 6455 framing
// (text + ping/pong/close, masked client frames) with no dependencies.
type hijackUpgrader struct {
	// ReadLimit caps one reassembled message; <=0 means WSMaxMessageBytes.
	ReadLimit int64
}

// headerHasToken reports whether a comma-separated header value holds tok
// (case-insensitive), e.g. Connection: keep-alive, Upgrade.
func headerHasToken(h http.Header, key, tok string) bool {
	for _, part := range strings.Split(h.Get(key), ",") {
		if strings.EqualFold(strings.TrimSpace(part), tok) {
			return true
		}
	}
	return false
}

// wsAcceptKey derives Sec-WebSocket-Accept from the client's key.
func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// closeCode builds a close-frame payload (2-byte code + UTF-8 reason).
func closeCode(code int, reason string) []byte {
	b := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(b, uint16(code))
	copy(b[2:], reason)
	return b
}

// Upgrade validates the handshake, hijacks the connection, and writes the
// 101 response itself (after Hijack the net/http server no longer owns the
// socket, so errors below are returned without touching w).
func (u *hijackUpgrader) Upgrade(w http.ResponseWriter, r *http.Request) (WSConn, error) {
	if r.Method != http.MethodGet {
		http.Error(w, "websocket upgrade requires GET", http.StatusMethodNotAllowed)
		return nil, errors.New("server: websocket: non-GET upgrade")
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		http.Error(w, "not a websocket handshake", http.StatusBadRequest)
		return nil, errors.New("server: websocket: missing upgrade headers")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "websocket version 13 required", http.StatusUpgradeRequired)
		return nil, errors.New("server: websocket: unsupported version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("server: websocket: missing key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return nil, errors.New("server: websocket: hijack unsupported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	limit := u.ReadLimit
	if limit <= 0 {
		limit = WSMaxMessageBytes
	}
	return newHijackConn(conn, rw, limit), nil
}

// hijackConn is a minimal RFC 6455 endpoint over a hijacked socket.
type hijackConn struct {
	conn      net.Conn
	rw        *bufio.ReadWriter
	readLimit int64

	writeMu sync.Mutex
	pongMu  sync.Mutex
	onPong  func()
}

func newHijackConn(conn net.Conn, rw *bufio.ReadWriter, limit int64) *hijackConn {
	return &hijackConn{conn: conn, rw: rw, readLimit: limit}
}

func (c *hijackConn) SetPongHandler(h func()) {
	c.pongMu.Lock()
	defer c.pongMu.Unlock()
	c.onPong = h
}

func (c *hijackConn) getPongHandler() func() {
	c.pongMu.Lock()
	defer c.pongMu.Unlock()
	return c.onPong
}

func (c *hijackConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *hijackConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
func (c *hijackConn) Close() error                       { return c.conn.Close() }

// WriteText sends one unfragmented text frame (servers never mask).
func (c *hijackConn) WriteText(p []byte) error { return c.writeFrame(true, 0x1, p) }

// Ping sends an empty ping; the peer's pong keeps the read deadline alive.
func (c *hijackConn) Ping() error { return c.writeFrame(true, 0x9, nil) }

func (c *hijackConn) writeFrame(fin bool, opcode byte, payload []byte) error {
	b0 := opcode & 0x0F
	if fin {
		b0 |= 0x80
	}
	var hdr [10]byte
	hdr[0] = b0
	var header []byte
	switch n := len(payload); {
	case n <= 125:
		hdr[1] = byte(n)
		header = hdr[:2]
	case n <= 65535:
		hdr[1] = 126
		hdr[2] = byte(n >> 8)
		hdr[3] = byte(n)
		header = hdr[:4]
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		header = hdr[:10]
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.rw.Write(header); err != nil {
		return err
	}
	if _, err := c.rw.Write(payload); err != nil {
		return err
	}
	return c.rw.Flush()
}

// readFrame parses one frame header + payload. Client frames MUST be masked
// (RFC 6455 §5.1); unmasked frames fail fast with a 1002 close.
func (c *hijackConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(c.rw, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = hdr[0]&0x80 != 0
	opcode = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.rw, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.rw, ext[:]); err != nil {
			return false, 0, nil, err
		}
		u := binary.BigEndian.Uint64(ext[:])
		if u > 1<<63-1 {
			_ = c.writeFrame(true, 0x8, closeCode(1009, "message too large"))
			return false, 0, nil, errors.New("server: websocket: frame too large")
		}
		length = int64(u)
	}
	if length > c.readLimit {
		_ = c.writeFrame(true, 0x8, closeCode(1009, "message too large"))
		return false, 0, nil, errors.New("server: websocket: message too large")
	}
	if !masked {
		_ = c.writeFrame(true, 0x8, closeCode(1002, "client frames must be masked"))
		return false, 0, nil, errors.New("server: websocket: unmasked client frame")
	}
	var mask [4]byte
	if _, err = io.ReadFull(c.rw, mask[:]); err != nil {
		return false, 0, nil, err
	}
	if length == 0 {
		return fin, opcode, nil, nil
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.rw, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

// ReadText reassembles fragmented text/binary frames, answers pings,
// consumes pongs (firing the pong handler), and echoes closes.
func (c *hijackConn) ReadText() ([]byte, error) {
	var msg []byte
	var opcode byte = 0xFF // unset until the first data frame
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x8: // close: echo and stop
			_ = c.writeFrame(true, 0x8, payload)
			return nil, ErrWSClosed
		case 0x9: // ping: pong with identical payload
			_ = c.writeFrame(true, 0xA, payload)
			continue
		case 0xA: // pong: keep-alive tick
			if h := c.getPongHandler(); h != nil {
				h()
			}
			continue
		case 0x0, 0x1, 0x2:
			if opcode == 0xFF {
				if op == 0x0 {
					_ = c.writeFrame(true, 0x8, closeCode(1002, "unexpected continuation"))
					return nil, errors.New("server: websocket: unexpected continuation frame")
				}
				opcode = op
			} else if op != 0x0 {
				_ = c.writeFrame(true, 0x8, closeCode(1002, "interleaved data frames"))
				return nil, errors.New("server: websocket: interleaved data frames")
			}
			if int64(len(msg)+len(payload)) > c.readLimit {
				_ = c.writeFrame(true, 0x8, closeCode(1009, "message too large"))
				return nil, errors.New("server: websocket: message too large")
			}
			msg = append(msg, payload...)
			if fin {
				return msg, nil
			}
		default: // reserved opcodes: fail fast per RFC 6455 §5.2
			_ = c.writeFrame(true, 0x8, closeCode(1003, "unsupported opcode"))
			return nil, errors.New("server: websocket: unsupported opcode")
		}
	}
}

// ---------------------------------------------------------------------------
// Server integration (route + JWT-authed upgrade + pumps)
// ---------------------------------------------------------------------------

// serverHubs maps *Server → *Hub so this file adds per-server hub state
// without editing the Server struct in server.go.
var serverHubs sync.Map

// wsRoutesOnce guards EnableWS against double registration (ServeMux panics
// on duplicate patterns).
var wsRoutesOnce sync.Map

// Hub returns the server's WebSocket hub, creating it on first use.
// Tests inject tight timeouts via SetHub.
func (s *Server) Hub() *Hub {
	if h, ok := serverHubs.Load(s); ok {
		return h.(*Hub)
	}
	h, _ := serverHubs.LoadOrStore(s, NewHub(HubOptions{}))
	return h.(*Hub)
}

// SetHub replaces the server's hub (tests, custom timeouts, or a shared
// hub across server instances). It does not migrate existing clients.
func (s *Server) SetHub(h *Hub) {
	if h == nil {
		return
	}
	serverHubs.Store(s, h)
}

// EnableWS registers GET /ws on the server mux. It is separate from
// registerRoutes (routes.go, owned by issue #8) so this issue extends the
// route table without editing that file. Safe to call twice; production
// wiring calls it once after New.
func (s *Server) EnableWS() {
	if _, loaded := wsRoutesOnce.LoadOrStore(s, true); loaded {
		return
	}
	s.mux.HandleFunc("GET /ws", s.handleWS)
}

// tokenFromWSRequest extracts the JWT: Authorization header first, then the
// ?token= query fallback for browser WebSocket clients.
func tokenFromWSRequest(r *http.Request) string {
	if tok := tokenFromHeader(r.Header.Get("Authorization")); tok != "" {
		return tok
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// handleWS authenticates (JWT, server clock), upgrades, and serves the
// connection for its lifetime: one read loop here, one write pump goroutine.
// Query params seed the subscription (?project_id=, ?session_id=); the
// client refines it with a subscribe message afterwards.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.verifyToken(tokenFromWSRequest(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r)
	if err != nil {
		return // adapter already wrote the HTTP error (or the socket died)
	}
	s.serveWSConn(conn,
		userID,
		strings.TrimSpace(r.URL.Query().Get("project_id")),
		strings.TrimSpace(r.URL.Query().Get("session_id")),
	)
}

// serveWSConn runs the connection lifecycle: register (announces online),
// write pump with ping heartbeat, read loop dispatching into the hub, and
// unregister on exit (announces offline).
func (s *Server) serveWSConn(conn WSConn, userID, projectID, sessionID string) {
	hub := s.Hub()
	c := hub.NewClient(userID, projectID, sessionID, conn)
	hub.Register(c)
	defer hub.Unregister(c)
	defer func() { _ = conn.Close() }()

	go wsWritePump(conn, c)

	_ = conn.SetReadDeadline(time.Now().Add(WSPongWait))
	conn.SetPongHandler(func() {
		_ = conn.SetReadDeadline(time.Now().Add(WSPongWait))
		hub.NoteHeartbeat(c.ID, scopedProject(c), scopedSession(c))
	})
	for {
		msg, err := conn.ReadText()
		if err != nil {
			return // closed, timed out, or protocol error: pumps unwind
		}
		_ = conn.SetReadDeadline(time.Now().Add(WSPongWait))
		if herr := hub.HandleClientMessage(c, msg); herr != nil {
			_ = conn.SetWriteDeadline(time.Now().Add(WSWriteWait))
			_ = conn.WriteText(NewErrorMessage(herr.Error()))
		}
	}
}

// scopedProject/scopedSession read the subscription under hub lock so the
// pong handler never races a concurrent subscribe update.
func scopedProject(c *Client) string {
	c.hub.mu.RLock()
	defer c.hub.mu.RUnlock()
	return c.ProjectID
}

// scopedSession reads the session subscription under hub lock.
func scopedSession(c *Client) string {
	c.hub.mu.RLock()
	defer c.hub.mu.RUnlock()
	return c.SessionID
}

// wsWritePump drains the client's Send outbox and pings on schedule. It
// exits when the outbox is drained-closed, a write fails, or Done fires
// (unregister/eviction). One pump per connection; the hub never blocks.
func wsWritePump(conn WSConn, c *Client) {
	ticker := time.NewTicker(WSPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.Send:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(WSWriteWait))
			if err := conn.WriteText(msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(WSWriteWait))
			if err := conn.Ping(); err != nil {
				return
			}
		case <-c.Done():
			return
		}
	}
}
