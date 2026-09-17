// Route table and handlers for the central REST API (issue #8, plan §1.8).
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

// Allowed vocabularies mirror the Postgres CHECK constraints (plan §1.1) so
// bad payloads fail fast at the API boundary with 400, not 500 from the DB.
var (
	allowedLevels = map[string]bool{
		"organization": true, "project": true, "personal": true, "session": true,
	}
	allowedScopes = map[string]bool{
		"fact": true, "preference": true, "decision": true,
		"constraint": true, "pattern": true, "episode_summary": true,
	}
	allowedMemoryStatus = map[string]bool{
		"PROPOSED": true, "CONFIRMED": true, "REJECTED": true, "SUPERSEDED": true,
	}
	allowedEpisodeTypes = map[string]bool{
		"bug_fix": true, "feature": true, "refactor": true,
		"incident": true, "investigation": true, "onboarding": true,
	}
	allowedEpisodeStatus = map[string]bool{
		"OPEN": true, "INVESTIGATING": true, "RESOLVED": true, "WONT_FIX": true,
	}
)

// defaultSearchLimit caps list endpoints when ?limit is absent/invalid.
const defaultSearchLimit = 20

// maxSearchLimit bounds ?limit (mirrors store.MaxSearchLimit).
const maxSearchLimit = 100

// registerRoutes binds every endpoint on the stdlib mux (Go 1.22+
// method+template patterns; no third-party router).
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /projects/resolve", s.handleProjectResolve)
	mux.HandleFunc("POST /workspaces/register", s.handleWorkspaceRegister)
	mux.HandleFunc("POST /workspaces/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /workspaces/{projectID}/active", s.handleActiveWorkspaces)
	mux.HandleFunc("POST /memory", s.handleCreateMemory)
	mux.HandleFunc("GET /memory/search", s.handleSearchMemory)
	mux.HandleFunc("POST /episodes", s.handleCreateEpisode)
	mux.HandleFunc("GET /episodes/search", s.handleSearchEpisodes)
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// envelope is the error body: {"error": "..."}.
type envelope struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, envelope{Error: msg})
}

// readJSON decodes a bounded body (1 MiB) with unknown-field rejection so
// client typos (e.g. "commmit_sha") surface as 400 instead of silent drops.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// storeError maps store failures: ErrUnauthorized/NotFound → 4xx, rest → 500.
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid username or password")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// queryLimit parses ?limit with default + clamp.
func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return defaultSearchLimit
	}
	if n > maxSearchLimit {
		return maxSearchLimit
	}
	return n
}

// ---------------------------------------------------------------------------
// POST /auth/login — {username, password} → {token, expires_at, user_id}
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	UserID    string `json:"user_id"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	userID, err := s.store.Authenticate(r.Context(), strings.TrimSpace(req.Username), req.Password)
	if err != nil {
		storeError(w, err)
		return
	}
	token, exp, err := s.mintToken(userID, strings.TrimSpace(req.Username))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Token:     token,
		ExpiresAt: exp.UTC().Format(time.RFC3339),
		UserID:    userID,
	})
}

// GET /healthz — liveness probe, no auth.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// POST /projects/resolve
// ---------------------------------------------------------------------------

type resolveRequest struct {
	Origin      string `json:"origin"`
	RootCommit  string `json:"root_commit"`
	FolderName  string `json:"folder_name"`
	DisplayName string `json:"display_name"`
}

func (s *Server) handleProjectResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FolderName) == "" {
		writeError(w, http.StatusBadRequest, "folder_name is required")
		return
	}
	p, err := s.store.ResolveProject(r.Context(), store.ProjectParams{
		Origin:      req.Origin,
		RootCommit:  req.RootCommit,
		FolderName:  req.FolderName,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ---------------------------------------------------------------------------
// POST /workspaces/register
// ---------------------------------------------------------------------------

type registerRequest struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha"`
	IsDirty   bool   `json:"is_dirty"`
	DaemonURL string `json:"daemon_url"`
}

func (s *Server) handleWorkspaceRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.UserID) == "" ||
		strings.TrimSpace(req.MachineID) == "" || strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "project_id, user_id, machine_id and path are required")
		return
	}
	ws, err := s.store.RegisterWorkspace(r.Context(), store.WorkspaceParams{
		ProjectID: strings.TrimSpace(req.ProjectID),
		UserID:    strings.TrimSpace(req.UserID),
		MachineID: strings.TrimSpace(req.MachineID),
		Path:      req.Path,
		Branch:    req.Branch,
		CommitSHA: req.CommitSHA,
		IsDirty:   req.IsDirty,
		DaemonURL: req.DaemonURL,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

// ---------------------------------------------------------------------------
// POST /workspaces/heartbeat — {workspace_id, branch, commit_sha, is_dirty}
// ---------------------------------------------------------------------------

type heartbeatRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Branch      string `json:"branch"`
	CommitSHA   string `json:"commit_sha"`
	IsDirty     bool   `json:"is_dirty"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	ws, err := s.store.HeartbeatWorkspace(r.Context(), strings.TrimSpace(req.WorkspaceID), store.HeartbeatParams{
		Branch:    req.Branch,
		CommitSHA: req.CommitSHA,
		IsDirty:   req.IsDirty,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

// ---------------------------------------------------------------------------
// GET /workspaces/{projectID}/active
// ---------------------------------------------------------------------------

type activeResponse struct {
	Workspaces []store.Workspace `json:"workspaces"`
	Count      int               `json:"count"`
}

func (s *Server) handleActiveWorkspaces(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if strings.TrimSpace(projectID) == "" {
		writeError(w, http.StatusBadRequest, "project id path parameter is required")
		return
	}
	all, err := s.store.ListWorkspaces(r.Context(), projectID)
	if err != nil {
		storeError(w, err)
		return
	}
	// Server-side staleness gate: online only while silence <= OfflineAfter.
	// store.IsOnlineAtPtr is the shared predicate (nil last_seen = offline),
	// so this filter can never drift from the store package's definition.
	now := s.now()
	active := make([]store.Workspace, 0, len(all))
	for _, ws := range all {
		if IsOnlineAt(ws.LastSeen, now) {
			active = append(active, ws)
		}
	}
	writeJSON(w, http.StatusOK, activeResponse{Workspaces: active, Count: len(active)})
}

// ---------------------------------------------------------------------------
// POST /memory
// ---------------------------------------------------------------------------

type createMemoryRequest struct {
	ProjectID      string   `json:"project_id"`
	Key            string   `json:"key"`
	Content        string   `json:"content"`
	ContextSnippet string   `json:"context_snippet"`
	Level          string   `json:"level"`
	Scope          string   `json:"scope"`
	Tags           []string `json:"tags"`
	Confidence     float64  `json:"confidence"`
	Status         string   `json:"status"`
	Source         string   `json:"source"`
}

func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	var req createMemoryRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if n := len(req.Content); n < 20 || n > 2000 {
		writeError(w, http.StatusBadRequest, "content must be 20-2000 characters")
		return
	}
	level := req.Level
	if level == "" {
		level = store.LevelProject
	}
	if !allowedLevels[level] {
		writeError(w, http.StatusBadRequest, "invalid level")
		return
	}
	scope := req.Scope
	if scope == "" {
		scope = "fact"
	}
	if !allowedScopes[scope] {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return
	}
	status := req.Status
	if status == "" {
		status = store.StatusProposed
	}
	if !allowedMemoryStatus[status] {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	confidence := req.Confidence
	if confidence == 0 {
		confidence = 1.0
	}
	if confidence < 0 || confidence > 1 {
		writeError(w, http.StatusBadRequest, "confidence must be 0.0-1.0")
		return
	}
	m, err := s.store.CreateMemory(r.Context(), Memory{
		ProjectID:      strings.TrimSpace(req.ProjectID),
		Key:            strings.TrimSpace(req.Key),
		Content:        req.Content,
		ContextSnippet: req.ContextSnippet,
		Level:          level,
		Scope:          scope,
		Tags:           req.Tags,
		Confidence:     confidence,
		Status:         status,
		Source:         req.Source,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// ---------------------------------------------------------------------------
// GET /memory/search?project_id=&q=&tags=a,b&key=&level=&limit=
//   [&embedding=[0.1,0.2,...]]
//
// Text path (default): substring ?q= over key/content/context_snippet plus
// tag/key/level filters. Vector path (issue #37): when ?embedding= carries
// a caller-supplied vector (pgvector literal "[0.1,0.2]" or bare
// "0.1,0.2"), the request routes through store.Search (plan §1.5 cosine +
// rerank) via the optional VectorMemorySearcher extension; a store that
// predates the extension answers 400, never silent text results. ?q= is
// NEVER stub-embedded server-side (see ADR-037): fake deterministic vectors
// would corrupt cosine ranking while looking authoritative. When both are
// present, embedding wins and ?q= is ignored.
// ---------------------------------------------------------------------------

// parseEmbeddingParam parses the optional ?embedding= query parameter: a
// pgvector literal ("[0.1,0.2]") or bare comma/space-separated floats
// ("0.1,0.2"). "" (absent) returns nil, nil — the text path. Anything
// unparseable (including NaN/Inf, which ParseFloat accepts but pgvector
// rejects) is an error the handler maps to 400.
func parseEmbeddingParam(raw string) ([]float32, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "("), ")"))
	if s == "" {
		return nil, errors.New("empty embedding vector")
	}
	var parts []string
	if strings.Contains(s, ",") {
		parts = strings.Split(s, ",")
	} else {
		parts = strings.Fields(s)
	}
	vec := make([]float32, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("empty embedding component")
		}
		f, err := strconv.ParseFloat(part, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid embedding component %q", part)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite embedding component %q", part)
		}
		if len(vec) >= 4096 {
			return nil, errors.New("embedding exceeds 4096 dimensions")
		}
		vec = append(vec, float32(f))
	}
	if len(vec) == 0 {
		return nil, errors.New("empty embedding vector")
	}
	return vec, nil
}

func (s *Server) handleSearchMemory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID := strings.TrimSpace(q.Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	var tags []string
	if raw := strings.TrimSpace(q.Get("tags")); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}
	level := strings.TrimSpace(q.Get("level"))
	if level != "" && !allowedLevels[level] {
		writeError(w, http.StatusBadRequest, "invalid level")
		return
	}
	filter := MemoryFilter{
		ProjectID: projectID,
		Query:     strings.TrimSpace(q.Get("q")),
		Tags:      tags,
		Key:       strings.TrimSpace(q.Get("key")),
		Level:     level,
		Limit:     queryLimit(r),
	}
	if rawEmb := strings.TrimSpace(q.Get("embedding")); rawEmb != "" {
		emb, err := parseEmbeddingParam(rawEmb)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid embedding: "+err.Error())
			return
		}
		vs, ok := s.store.(VectorMemorySearcher)
		if !ok {
			writeError(w, http.StatusBadRequest, "vector search not supported by configured store")
			return
		}
		items, err := vs.SearchMemoryVector(r.Context(), filter, emb)
		if err != nil {
			storeError(w, err)
			return
		}
		if items == nil {
			items = []Memory{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	items, err := s.store.SearchMemory(r.Context(), filter)
	if err != nil {
		storeError(w, err)
		return
	}
	if items == nil {
		items = []Memory{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// ---------------------------------------------------------------------------
// POST /episodes
// ---------------------------------------------------------------------------

type createEpisodeRequest struct {
	ProjectID     string   `json:"project_id"`
	Title         string   `json:"title"`
	EpisodeType   string   `json:"episode_type"`
	Trigger       string   `json:"trigger"`
	Investigation string   `json:"investigation"`
	RootCause     string   `json:"root_cause"`
	Resolution    string   `json:"resolution"`
	Verification  string   `json:"verification"`
	Tags          []string `json:"tags"`
	FilesInvolved []string `json:"files_involved"`
	ErrorPatterns []string `json:"error_patterns"`
	Status        string   `json:"status"`
}

func (s *Server) handleCreateEpisode(w http.ResponseWriter, r *http.Request) {
	var req createEpisodeRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if !allowedEpisodeTypes[req.EpisodeType] {
		writeError(w, http.StatusBadRequest, "invalid episode_type")
		return
	}
	status := req.Status
	if status == "" {
		status = "OPEN"
	}
	if !allowedEpisodeStatus[status] {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	ep, err := s.store.CreateEpisode(r.Context(), Episode{
		ProjectID:     strings.TrimSpace(req.ProjectID),
		Title:         strings.TrimSpace(req.Title),
		EpisodeType:   req.EpisodeType,
		Trigger:       req.Trigger,
		Investigation: req.Investigation,
		RootCause:     req.RootCause,
		Resolution:    req.Resolution,
		Verification:  req.Verification,
		Tags:          req.Tags,
		FilesInvolved: req.FilesInvolved,
		ErrorPatterns: req.ErrorPatterns,
		Status:        status,
	})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ep)
}

// ---------------------------------------------------------------------------
// GET /episodes/search?project_id=&q=&error_pattern=&file=&status=&episode_type=&limit=
// ---------------------------------------------------------------------------

func (s *Server) handleSearchEpisodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID := strings.TrimSpace(q.Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id query parameter is required")
		return
	}
	status := strings.TrimSpace(q.Get("status"))
	if status != "" && !allowedEpisodeStatus[status] {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	episodeType := strings.TrimSpace(q.Get("episode_type"))
	if episodeType != "" && !allowedEpisodeTypes[episodeType] {
		writeError(w, http.StatusBadRequest, "invalid episode_type")
		return
	}
	eps, err := s.store.SearchEpisodes(r.Context(), EpisodeFilter{
		ProjectID:    projectID,
		Query:        strings.TrimSpace(q.Get("q")),
		ErrorPattern: strings.TrimSpace(q.Get("error_pattern")),
		File:         strings.TrimSpace(q.Get("file")),
		Status:       status,
		EpisodeType:  episodeType,
		Limit:        queryLimit(r),
	})
	if err != nil {
		storeError(w, err)
		return
	}
	if eps == nil {
		eps = []Episode{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"episodes": eps, "count": len(eps)})
}
