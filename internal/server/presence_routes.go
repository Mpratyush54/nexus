package server

import (
	"net/http"
	"sort"
	"strings"
)

func (s *Server) registerPresenceRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/presence", s.requireAuth(s.handleProjectPresence))
}

func (s *Server) handleProjectPresence(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id path parameter is required")
		return
	}
	if !s.authorizeProject(w, r, id) {
		return
	}

	seen := map[string]string{}
	add := func(uid, status string) {
		uid = strings.TrimSpace(uid)
		if uid == "" {
			return
		}
		if status == "" || status == PresenceOffline {
			status = PresenceOnline
		}
		if prev, ok := seen[uid]; ok && prev == PresenceTyping {
			return
		}
		seen[uid] = status
	}

	add(authSubject(r), PresenceOnline)

	s.steerMu.RLock()
	h := s.hub
	s.steerMu.RUnlock()
	for _, p := range h.PresenceForProject(id) {
		add(p.UserID, p.Status)
	}

	items := make([]map[string]string, 0, len(seen))
	for uid, st := range seen {
		items = append(items, map[string]string{"user_id": uid, "status": st})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["user_id"] < items[j]["user_id"] })
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": id,
		"count":      len(items),
		"items":      items,
	})
}
