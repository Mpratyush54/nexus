package server

// promote.go — POST /memory/{id}/promote (issue #38).
//
// Master owns routes_extra.go (confirm/reject/resolve wiring) and the store
// package owns promotion (MemStore/PostgresStore.PromoteSessionMemory: level
// session → project, session_id cleared, status → CONFIRMED). This file only
// wires the missing HTTP endpoint onto s.Mux (route line in
// registerExtraRoutes) following the confirm handler's conventions:
// unknown id → 404, non-session level → 409, same {"items"}-free envelope
// (the promoted item as JSON).

import (
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

// handleMemoryPromote promotes one session-scoped memory to project scope.
func (s *Server) handleMemoryPromote(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	ss, ok := s.sessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "session promotion not supported by configured store")
		return
	}
	confirmedBy := authSubject(r)
	if err := ss.PromoteSessionMemory(r.Context(), id, confirmedBy); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return
		}
		// Non-session level (and other state violations) surface as 409:
		// the request shape is fine, the resource state forbids it.
		if strings.Contains(err.Error(), "not session-scoped") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not promote memory: "+err.Error())
		return
	}
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "level": "project"})
		return
	}
	writeJSON(w, http.StatusOK, item)
}
