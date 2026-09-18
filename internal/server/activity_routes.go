package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

func (s *Server) registerActivityRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/events", s.requireAuth(s.handleProjectEvents))
}

func (s *Server) handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project id path parameter is required")
		return
	}
	if !s.authorizeProject(w, r, projectID) {
		return
	}

	q := r.URL.Query()
	typeFilter := strings.TrimSpace(q.Get("type"))
	userFilter := strings.TrimSpace(q.Get("user_id"))
	sinceRaw := strings.TrimSpace(q.Get("since"))

	limit := 200
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}

	var sinceID int64
	var sinceTime time.Time
	if sinceRaw != "" {
		if n, err := strconv.ParseInt(sinceRaw, 10, 64); err == nil && n > 0 && !strings.Contains(sinceRaw, "-") {
			sinceID = n
		} else if t, err := time.Parse(time.RFC3339, sinceRaw); err == nil {
			sinceTime = t
		} else if t, err := time.Parse("2006-01-02", sinceRaw); err == nil {
			sinceTime = t
		}
	}

	events, err := s.Store.ListEvents(r.Context(), projectID, sinceID, store.MaxEventsLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list events: "+err.Error())
		return
	}

	filtered := make([]*store.Event, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if typeFilter != "" && ev.EventType != typeFilter {
			continue
		}
		if userFilter != "" && !strings.Contains(strings.ToLower(ev.UserID), strings.ToLower(userFilter)) {
			continue
		}
		if !sinceTime.IsZero() && ev.CreatedAt.Before(sinceTime) {
			continue
		}
		filtered = append(filtered, ev)
	}

	// Newest first for the activity feed.
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	if filtered == nil {
		filtered = []*store.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": filtered, "count": len(filtered)})
}
