// notification_routes.go — in-app notification feed + Web Push subscribe (issue #176).
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
	"central-memory/internal/webpush"
)

type pushSubscription struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	Endpoint  string    `json:"endpoint"`
	Keys      pushKeys  `json:"keys"`
	CreatedAt time.Time `json:"created_at"`
	UserAgent string    `json:"user_agent,omitempty"`
}

type pushDevice struct {
	ID           string    `json:"id"`
	Host         string    `json:"host"`
	EndpointHint string    `json:"endpoint_hint,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type pushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type notificationItem struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	ProjectID string    `json:"project_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	Href      string    `json:"href,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

var knownNotificationTypes = []string{
	"MEMORY_PROPOSED",
	"MEMORY_CONFIRMED",
	"MEMORY_UPDATED",
	"MEMORY_REJECTED",
	"MEMORY_SUPERSEDED",
	"SESSION_HANDOFF_INITIATED",
	"SESSION_HANDOFF_ACCEPTED",
	"MCP_TOOL_CALL",
}

var (
	pushMu   sync.Mutex
	pushSubs = map[string][]pushSubscription{} // userID -> subs

	prefMu     sync.Mutex
	notifPrefs = map[string]map[string]bool{} // userID -> eventType -> enabled
)

func (s *Server) registerNotificationRoutes() {
	s.Mux.HandleFunc("GET /notifications", s.requireAuth(s.handleNotificationList))
	s.Mux.HandleFunc("GET /notifications/vapid-public-key", s.requireAuth(s.handleVapidPublicKey))
	s.Mux.HandleFunc("POST /notifications/subscribe", s.requireAuth(s.handlePushSubscribe))
	s.Mux.HandleFunc("DELETE /notifications/subscribe", s.requireAuth(s.handlePushUnsubscribe))
	s.Mux.HandleFunc("GET /notifications/devices", s.requireAuth(s.handlePushDeviceList))
	s.Mux.HandleFunc("DELETE /notifications/devices/{id}", s.requireAuth(s.handlePushDeviceDelete))
	s.Mux.HandleFunc("GET /notifications/preferences", s.requireAuth(s.handleNotificationPrefsGet))
	s.Mux.HandleFunc("PUT /notifications/preferences", s.requireAuth(s.handleNotificationPrefsPut))
	s.Mux.HandleFunc("POST /notifications/test", s.requireAuth(s.handlePushTest))
}

func newPushDeviceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("dev_%d", time.Now().UTC().UnixNano())
	}
	return "dev_" + hex.EncodeToString(b)
}

func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "unknown"
	}
	return u.Host
}

func endpointHint(endpoint string) string {
	if len(endpoint) <= 10 {
		return endpoint
	}
	return endpoint[len(endpoint)-10:]
}

func toPushDevice(sub pushSubscription) pushDevice {
	return pushDevice{
		ID:           sub.ID,
		Host:         endpointHost(sub.Endpoint),
		EndpointHint: endpointHint(sub.Endpoint),
		UserAgent:    sub.UserAgent,
		CreatedAt:    sub.CreatedAt,
	}
}

func (s *Server) handleVapidPublicKey(w http.ResponseWriter, r *http.Request) {
	keys, _, ok := webpush.KeysFromEnv()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"public_key": "",
			"configured": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_key": keys.Public,
		"configured": true,
	})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string   `json:"endpoint"`
		Keys     pushKeys `json:"keys"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Endpoint) == "" || strings.TrimSpace(req.Keys.P256dh) == "" {
		writeError(w, http.StatusBadRequest, "endpoint and keys are required")
		return
	}
	uid := authSubject(r)
	sub := pushSubscription{
		ID:        newPushDeviceID(),
		UserID:    uid,
		Endpoint:  strings.TrimSpace(req.Endpoint),
		Keys:      req.Keys,
		CreatedAt: time.Now().UTC(),
		UserAgent: r.Header.Get("User-Agent"),
	}
	pushMu.Lock()
	list := pushSubs[uid]
	replaced := false
	for i := range list {
		if list[i].Endpoint == sub.Endpoint {
			sub.ID = list[i].ID
			if sub.ID == "" {
				sub.ID = newPushDeviceID()
			}
			list[i] = sub
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, sub)
	}
	pushSubs[uid] = list
	pushMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"subscribed": true,
		"id":         sub.ID,
		"device":     toPushDevice(sub),
	})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	uid := authSubject(r)
	pushMu.Lock()
	list := pushSubs[uid]
	out := list[:0]
	for _, sub := range list {
		if sub.Endpoint != strings.TrimSpace(req.Endpoint) {
			out = append(out, sub)
		}
	}
	pushSubs[uid] = out
	pushMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"unsubscribed": true})
}

func (s *Server) handlePushDeviceList(w http.ResponseWriter, r *http.Request) {
	uid := authSubject(r)
	pushMu.Lock()
	list := pushSubs[uid]
	devices := make([]pushDevice, 0, len(list))
	for i := range list {
		if list[i].ID == "" {
			list[i].ID = newPushDeviceID()
		}
		devices = append(devices, toPushDevice(list[i]))
	}
	pushSubs[uid] = list
	pushMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": devices, "count": len(devices)})
}

func (s *Server) handlePushDeviceDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "device id is required")
		return
	}
	uid := authSubject(r)
	pushMu.Lock()
	list := pushSubs[uid]
	out := list[:0]
	found := false
	for _, sub := range list {
		if sub.ID == id {
			found = true
			continue
		}
		out = append(out, sub)
	}
	pushSubs[uid] = out
	pushMu.Unlock()
	if !found {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "id": id})
}

func defaultNotificationPrefs() map[string]bool {
	out := make(map[string]bool, len(knownNotificationTypes))
	for _, t := range knownNotificationTypes {
		out[t] = true
	}
	return out
}

func prefsFor(userID string) map[string]bool {
	out := defaultNotificationPrefs()
	prefMu.Lock()
	defer prefMu.Unlock()
	if stored, ok := notifPrefs[userID]; ok {
		for k, v := range stored {
			out[k] = v
		}
	}
	return out
}

func wantsNotification(userID, eventType string) bool {
	if eventType == "" {
		return true
	}
	p := prefsFor(userID)
	v, ok := p[eventType]
	if !ok {
		return true
	}
	return v
}

func (s *Server) handleNotificationPrefsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"types": prefsFor(authSubject(r)),
	})
}

func (s *Server) handleNotificationPrefsPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Types map[string]bool `json:"types"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	uid := authSubject(r)
	merged := defaultNotificationPrefs()
	for k, v := range req.Types {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		merged[k] = v
	}
	prefMu.Lock()
	notifPrefs[uid] = merged
	prefMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"types": merged})
}

func (s *Server) handleNotificationList(w http.ResponseWriter, r *http.Request) {
	uid := authSubject(r)
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizeProject(w, r, projectID) {
		return
	}

	var sinceID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			sinceID = n
		}
	}

	events, err := s.Store.ListEvents(r.Context(), projectID, sinceID, 80)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list notifications: "+err.Error())
		return
	}
	items := make([]notificationItem, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if !wantsNotification(uid, ev.EventType) {
			continue
		}
		title, body := notificationCopy(ev)
		items = append(items, notificationItem{
			ID:        fmt.Sprintf("%d", ev.ID),
			Type:      ev.EventType,
			Title:     title,
			Body:      body,
			ProjectID: ev.ProjectID,
			UserID:    ev.UserID,
			Href:      notificationHref(ev),
			CreatedAt: ev.CreatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > 40 {
		items = items[:40]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func payloadString(ev *store.Event, keys ...string) string {
	if ev == nil || ev.Payload == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := ev.Payload[k].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func notificationHref(ev *store.Event) string {
	if ev == nil {
		return "/app/activity"
	}
	switch ev.EventType {
	case "MEMORY_PROPOSED", "MEMORY_CONFIRMED", "MEMORY_UPDATED", "MEMORY_REJECTED", "MEMORY_SUPERSEDED":
		return "/app/memory"
	case "MCP_TOOL_CALL":
		return "/app/agents"
	case "SESSION_HANDOFF_INITIATED", "SESSION_HANDOFF_ACCEPTED":
		return "/app/sessions"
	default:
		return "/app/activity"
	}
}

func notificationCopy(ev *store.Event) (title, body string) {
	if ev == nil {
		return "Activity", "Project activity"
	}
	key := payloadString(ev, "key")
	tool := payloadString(ev, "tool_name", "tool")
	agent := payloadString(ev, "agent_id")
	switch ev.EventType {
	case "MEMORY_PROPOSED":
		if key != "" {
			return "Memory proposed", key
		}
		return "Memory proposed", "A new memory is waiting for review"
	case "MEMORY_CONFIRMED":
		if key != "" {
			return "Memory confirmed", key
		}
		return "Memory confirmed", "A proposed memory was confirmed"
	case "MEMORY_UPDATED":
		if key != "" {
			return "Memory edited", key
		}
		return "Memory edited", "Someone updated a memory"
	case "MEMORY_REJECTED":
		if key != "" {
			return "Memory rejected", key
		}
		return "Memory rejected", "A proposed memory was rejected"
	case "MEMORY_SUPERSEDED":
		if key != "" {
			return "Memory superseded", key
		}
		return "Memory superseded", "A memory was replaced"
	case "SESSION_HANDOFF_INITIATED":
		return "Handoff started", "A session handoff is waiting"
	case "SESSION_HANDOFF_ACCEPTED":
		return "Handoff accepted", "A handoff was accepted"
	case "MCP_TOOL_CALL":
		if tool != "" && agent != "" {
			return "Agent activity", agent + " · " + tool
		}
		if tool != "" {
			return "Agent activity", tool
		}
		return "Agent activity", "An MCP agent invoked a tool"
	default:
		if ev.EventType == "" {
			return "Activity", "Project activity"
		}
		if key != "" {
			return ev.EventType, key
		}
		return ev.EventType, "Project activity"
	}
}

type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title     string `json:"title"`
		Body      string `json:"body"`
		URL       string `json:"url"`
		ProjectID string `json:"project_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Nexus test"
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		body = "Web Push delivery is configured."
	}
	targetURL := strings.TrimSpace(req.URL)
	if targetURL == "" {
		targetURL = "/app/settings"
	}
	uid := authSubject(r)
	if proj := strings.TrimSpace(req.ProjectID); proj != "" {
		if !s.authorizeProject(w, r, proj) {
			return
		}
		sent, failed, err := s.pushProject(r.Context(), proj, pushPayload{Title: title, Body: body, URL: targetURL}, uid, "")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"sent": sent, "failed": failed, "scope": "project"})
		return
	}
	sent, failed, err := s.pushUsers(r.Context(), []string{uid}, pushPayload{Title: title, Body: body, URL: targetURL})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"sent": sent, "failed": failed, "scope": "self"})
}

// notifyProjectActivity sends a Web Push to every project member that has a
// subscription. Missing VAPID keys or empty member lists are no-ops.
func (s *Server) notifyProjectActivity(projectID, eventType, urlPath string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	if _, _, ok := webpush.KeysFromEnv(); !ok {
		return
	}
	title, body := notificationCopy(&store.Event{EventType: eventType})
	if urlPath == "" {
		urlPath = "/app/memory"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _, _ = s.pushProject(ctx, projectID, pushPayload{Title: title, Body: body, URL: urlPath}, "", eventType)
	}()
}

func (s *Server) pushProject(ctx context.Context, projectID string, msg pushPayload, skipUserID, eventType string) (sent, failed int, err error) {
	seen := map[string]bool{}
	var ids []string
	skip := strings.TrimSpace(skipUserID)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || id == skip || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if p, gerr := s.Store.GetProject(ctx, projectID); gerr == nil && p != nil {
		add(p.CreatedBy)
	}
	members, err := s.Store.ListMembers(ctx, projectID)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range members {
		add(id)
	}
	if eventType != "" {
		kept := ids[:0]
		for _, id := range ids {
			if wantsNotification(id, eventType) {
				kept = append(kept, id)
			}
		}
		ids = kept
	}
	return s.pushUsers(ctx, ids, msg)
}

func (s *Server) pushUsers(ctx context.Context, userIDs []string, msg pushPayload) (sent, failed int, err error) {
	keys, subject, ok := webpush.KeysFromEnv()
	if !ok {
		return 0, 0, fmt.Errorf("web push is not configured (set VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY)")
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return 0, 0, err
	}
	client := s.PushClient
	if client == nil {
		client = http.DefaultClient
	}
	for _, uid := range userIDs {
		subs := copyPushSubs(uid)
		for _, sub := range subs {
			resp, serr := webpush.Send(ctx, keys, webpush.Subscription{
				Endpoint: sub.Endpoint,
				P256dh:   sub.Keys.P256dh,
				Auth:     sub.Keys.Auth,
			}, raw, webpush.Options{
				Subscriber: subject,
				TTL:        86400,
				Urgency:    "high",
				HTTPClient: client,
			})
			if serr != nil {
				failed++
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				dropPushSub(uid, sub.Endpoint)
				failed++
				continue
			}
			if resp.StatusCode >= 300 {
				failed++
				continue
			}
			sent++
		}
	}
	return sent, failed, nil
}

func copyPushSubs(userID string) []pushSubscription {
	pushMu.Lock()
	defer pushMu.Unlock()
	src := pushSubs[userID]
	out := make([]pushSubscription, len(src))
	copy(out, src)
	return out
}

func dropPushSub(userID, endpoint string) {
	pushMu.Lock()
	defer pushMu.Unlock()
	list := pushSubs[userID]
	out := list[:0]
	for _, sub := range list {
		if sub.Endpoint != endpoint {
			out = append(out, sub)
		}
	}
	pushSubs[userID] = out
}
