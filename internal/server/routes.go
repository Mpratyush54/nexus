package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

// registerRoutes wires every Phase 1.8 endpoint. Authenticated routes go
// through requireAuth; only /auth/login and /healthz are public.
func (s *Server) registerRoutes() {
	s.Mux.HandleFunc("POST /auth/login", s.handleLogin)
	s.Mux.HandleFunc("GET /healthz", s.handleHealth)

	s.Mux.HandleFunc("POST /projects/resolve", s.requireAuth(s.handleProjectResolve))
	s.Mux.HandleFunc("POST /workspaces/register", s.requireAuth(s.handleWorkspaceRegister))
	s.Mux.HandleFunc("POST /workspaces/heartbeat", s.requireAuth(s.handleWorkspaceHeartbeat))
	s.Mux.HandleFunc("GET /workspaces/{projectId}/active", s.requireAuth(s.handleWorkspaceActive))

	s.Mux.HandleFunc("POST /memory", s.requireAuth(s.handleMemoryCreate))
	s.Mux.HandleFunc("GET /memory/search", s.requireAuth(s.handleMemorySearch))

	s.Mux.HandleFunc("POST /episodes", s.requireAuth(s.handleEpisodeCreate))
	s.Mux.HandleFunc("GET /episodes/search", s.requireAuth(s.handleEpisodeSearch))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- auth ---

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin implements the v1 simple login: any non-empty username+password
// yields a stub JWT. TODO(auth): validate against the users table once the
// Postgres/RDS store lands; return 401 for unknown users / bad passwords.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	token, err := s.Auth.Generate(strings.TrimSpace(req.Username), DefaultTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    token,
		"username": strings.TrimSpace(req.Username),
	})
}

// --- projects ---

type projectResolveRequest struct {
	CanonicalURL string `json:"canonical_url"`
	RootCommit   string `json:"root_commit"`
	FolderName   string `json:"folder_name"`
}

func (s *Server) handleProjectResolve(w http.ResponseWriter, r *http.Request) {
	var req projectResolveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FolderName) == "" && strings.TrimSpace(req.CanonicalURL) == "" && strings.TrimSpace(req.RootCommit) == "" {
		writeError(w, http.StatusBadRequest, "at least one of canonical_url, root_commit, folder_name is required")
		return
	}
	p, err := s.Store.ResolveProject(r.Context(), req.CanonicalURL, req.RootCommit, req.FolderName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not resolve project: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- workspaces ---

func (s *Server) handleWorkspaceRegister(w http.ResponseWriter, r *http.Request) {
	var ws store.Workspace
	if !decodeJSON(w, r, &ws) {
		return
	}
	if strings.TrimSpace(ws.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if strings.TrimSpace(ws.MachineID) == "" || strings.TrimSpace(ws.Path) == "" {
		writeError(w, http.StatusBadRequest, "machine_id and path are required")
		return
	}
	ws.ID = "" // server assigns the ID; client must not set it
	if err := s.Store.RegisterWorkspace(r.Context(), &ws); err != nil {
		writeError(w, http.StatusInternalServerError, "could not register workspace: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &ws)
}

type heartbeatRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Branch      string `json:"branch"`
	CommitSHA   string `json:"commit_sha"`
	IsDirty     bool   `json:"is_dirty"`
}

func (s *Server) handleWorkspaceHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if err := s.Store.Heartbeat(r.Context(), req.WorkspaceID, req.Branch, req.CommitSHA, req.IsDirty); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not record heartbeat: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_id": req.WorkspaceID})
}

func (s *Server) handleWorkspaceActive(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	if strings.TrimSpace(projectID) == "" {
		writeError(w, http.StatusBadRequest, "projectId path parameter is required")
		return
	}
	ws, err := s.Store.GetActiveWorkspace(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no active workspace for project")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not fetch active workspace: "+err.Error())
		return
	}
	// Defense in depth: the store filters by the same 90s window, but re-check
	// here so a future store implementation cannot leak stale presences.
	if !WorkspaceIsOnline(ws, time.Now()) {
		writeError(w, http.StatusNotFound, "no active workspace for project")
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

// --- memory ---

func (s *Server) handleMemoryCreate(w http.ResponseWriter, r *http.Request) {
	var item store.MemoryItem
	if !decodeJSON(w, r, &item) {
		return
	}
	if strings.TrimSpace(item.Key) == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	// Mirrors the CHECK constraint in migrations/001_initial.up.sql (20–2000 chars).
	if n := len(strings.TrimSpace(item.Content)); n < 20 || n > 2000 {
		writeError(w, http.StatusBadRequest, "content must be 20-2000 characters")
		return
	}
	item.ID = "" // server assigns the ID
	if err := s.Store.CreateMemoryItem(r.Context(), &item); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create memory item: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &item)
}

func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID := strings.TrimSpace(q.Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	// NOTE(search): MemStore does keyword fallback only. The Postgres store
	// will run pgvector cosine search first and fall back to full-text; the
	// response shape {"items","count"} is kept stable for that swap.
	var tags []string
	if raw := q.Get("tags"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	items, err := s.Store.SearchMemory(r.Context(), projectID, q.Get("q"), tags, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.MemoryItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// --- episodes ---

func (s *Server) handleEpisodeCreate(w http.ResponseWriter, r *http.Request) {
	var ep store.Episode
	if !decodeJSON(w, r, &ep) {
		return
	}
	if strings.TrimSpace(ep.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if strings.TrimSpace(ep.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if strings.TrimSpace(ep.EpisodeType) == "" {
		writeError(w, http.StatusBadRequest, "episode_type is required")
		return
	}
	ep.ID = "" // server assigns the ID
	if err := s.Store.CreateEpisode(r.Context(), &ep); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create episode: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &ep)
}

func (s *Server) handleEpisodeSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID := strings.TrimSpace(q.Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	episodes, err := s.Store.SearchEpisodes(r.Context(), projectID, q.Get("error_pattern"), q.Get("q"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	if episodes == nil {
		episodes = []*store.Episode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": episodes, "count": len(episodes)})
}
