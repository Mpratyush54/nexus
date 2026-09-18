package server

import (
	"context"
	"errors"
	"net"
	"net/http"
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

	// Issue #8: sessions, branches, confirmation flow, episode resolve.
	// Handlers live in routes_extra.go to avoid clashing with Phase 1.8 work.
	s.registerExtraRoutes()
	s.registerSteerRoutes()
	s.registerHandoffRoutes()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- auth ---

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// clientIP extracts the login attempt source for rate limiting: the first
// X-Forwarded-For hop when behind the ALB, else the direct remote address.
func clientIP(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		if i := strings.Index(fwd, ","); i >= 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return fwd
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// handleLogin verifies username+password against the users table and mints
// a JWT (issue #133). Unknown users, unset passwords, and mismatches all
// 401 with the same message (no oracle for username enumeration). Without
// a configured Users source the endpoint fails closed with 503. Attempts
// are rate-limited per source IP.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || strings.TrimSpace(req.Password) == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("login:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	if s.Users == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	userID, hash, err := s.Users.GetPasswordHash(r.Context(), username)
	if err != nil {
		// Unknown user and lookup failure alike: 401, no enumeration.
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if strings.TrimSpace(hash) == "" {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if !VerifyPassword(req.Password, hash) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	// Canonical identity (issue #140): sub is the user UUID for Postgres
	// ownership columns; the username rides as the display claim.
	token, err := s.Auth.GenerateUser(userID, username, DefaultTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    token,
		"username": username,
		"user_id":  userID,
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
	ws.ID = ""                 // server assigns the ID; client must not set it
	ws.UserID = authSubject(r) // attribution is the authenticated user (issue #141)
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
	if !s.authorizeProject(w, r, projectID) {
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
	if !s.eventAllowed("memory:" + strings.TrimSpace(item.ProjectID)) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if !s.authorizeProject(w, r, item.ProjectID) {
		return
	}
	if ok, reason := s.quotaAllowed(len(item.Content)); !ok {
		writeError(w, http.StatusTooManyRequests, "quota exceeded: "+reason)
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
	if !s.authorizeProject(w, r, projectID) {
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
	limit := clampSearchLimit(q.Get("limit"), w)
	if limit < 0 {
		return
	}
	// Vector path (issue #37): a caller-supplied ?embedding= vector routes
	// through pgvector cosine search when the configured store supports it
	// (PostgresStore.SearchMemoryVector). Text-only stores answer 400, never
	// silent text results. When both are present, embedding wins and ?q= is
	// ignored.
	if rawEmb := strings.TrimSpace(q.Get("embedding")); rawEmb != "" {
		vec, err := parseEmbeddingParam(rawEmb)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid embedding: "+err.Error())
			return
		}
		vs, ok := s.Store.(interface {
			SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*store.MemoryItem, error)
		})
		if !ok {
			writeError(w, http.StatusBadRequest, "vector search not supported by configured store")
			return
		}
		items, err := vs.SearchMemoryVector(r.Context(), projectID, vec, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "vector search failed: "+err.Error())
			return
		}
		recordMemoryUse(r.Context(), s.Store, items)
		if items == nil {
			items = []*store.MemoryItem{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	items, err := s.Store.SearchMemory(r.Context(), projectID, q.Get("q"), tags, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	recordMemoryUse(r.Context(), s.Store, items)
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
	if !s.eventAllowed("episodes:" + strings.TrimSpace(ep.ProjectID)) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if !s.authorizeProject(w, r, ep.ProjectID) {
		return
	}
	if ok, reason := s.quotaAllowed(len(ep.Title) + len(ep.Trigger)); !ok {
		writeError(w, http.StatusTooManyRequests, "quota exceeded: "+reason)
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
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	limit := clampSearchLimit(q.Get("limit"), w)
	if limit < 0 {
		return
	}
	episodes, err := s.Store.SearchEpisodes(r.Context(), projectID, q.Get("error_pattern"), q.Get("q"), "", "", limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	if episodes == nil {
		episodes = []*store.Episode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": episodes, "count": len(episodes)})
}
