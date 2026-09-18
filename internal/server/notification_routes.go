// notification_routes.go — in-app notification feed + Web Push subscribe (issue #176).
package server

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
)

type pushSubscription struct {
	UserID    string    `json:"-"`
	Endpoint  string    `json:"endpoint"`
	Keys      pushKeys  `json:"keys"`
	CreatedAt time.Time `json:"created_at"`
	UserAgent string    `json:"user_agent,omitempty"`
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
	CreatedAt time.Time `json:"created_at"`
}

var (
	pushMu   sync.Mutex
	pushSubs = map[string][]pushSubscription{} // userID -> subs
)

func (s *Server) registerNotificationRoutes() {
	s.Mux.HandleFunc("GET /notifications", s.requireAuth(s.handleNotificationList))
	s.Mux.HandleFunc("GET /notifications/vapid-public-key", s.requireAuth(s.handleVapidPublicKey))
	s.Mux.HandleFunc("POST /notifications/subscribe", s.requireAuth(s.handlePushSubscribe))
	s.Mux.HandleFunc("DELETE /notifications/subscribe", s.requireAuth(s.handlePushUnsubscribe))
}

func (s *Server) handleVapidPublicKey(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(os.Getenv("VAPID_PUBLIC_KEY"))
	if key == "" {
		// Dev placeholder — clients can still request Notification permission
		// and register a subscription; delivery requires a real VAPID key pair.
		key = "BNplaceholder_dev_only_replace_with_real_vapid_public_key_base64url______________"
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_key": key})
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
	writeJSON(w, http.StatusCreated, map[string]any{"subscribed": true, "endpoint": sub.Endpoint})
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
		title, body := notificationCopy(ev)
		items = append(items, notificationItem{
			ID:        fmt.Sprintf("%d", ev.ID),
			Type:      ev.EventType,
			Title:     title,
			Body:      body,
			ProjectID: ev.ProjectID,
			CreatedAt: ev.CreatedAt,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > 40 {
		items = items[:40]
	}
	_ = uid
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func notificationCopy(ev *store.Event) (title, body string) {
	switch ev.EventType {
	case "MEMORY_PROPOSED":
		return "Memory proposed", "A new memory is waiting for review"
	case "MEMORY_CONFIRMED":
		return "Memory confirmed", "A proposed memory was confirmed"
	case "MEMORY_UPDATED":
		return "Memory edited", "Someone updated a memory"
	case "SESSION_HANDOFF_INITIATED":
		return "Handoff started", "A session handoff is waiting"
	case "SESSION_HANDOFF_ACCEPTED":
		return "Handoff accepted", "A handoff was accepted"
	case "MCP_TOOL_CALL":
		return "Agent activity", "An MCP agent invoked a tool"
	default:
		if ev.EventType == "" {
			return "Activity", "Project activity"
		}
		return ev.EventType, "Project activity"
	}
}
