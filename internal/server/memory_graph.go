package server

// memory_graph.go — GET /v1/projects/{id}/graph?limit=: the viewer's memory
// graph of a project (memories, the chats they came from, the files they
// touch) for the desktop Graph view. See store.LoadProjectGraph.

import (
	"net/http"
	"strconv"
	"strings"

	"central-memory/internal/store"
)

func (s *Server) registerMemoryGraphRoutes() {
	s.Mux.HandleFunc("GET /v1/projects/{id}/graph", s.requireAuth(s.handleProjectGraph))
}

func (s *Server) handleProjectGraph(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	src := store.GraphSources{Memories: s.Store}
	if cs, ok := s.agentCloud(); ok {
		src.Sessions = cs
	}
	if ll, ok := s.Store.(store.ProjectLinkLister); ok {
		src.Links = ll
	}
	g, err := store.LoadProjectGraph(r.Context(), src, authSubject(r), projectID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "graph failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}
