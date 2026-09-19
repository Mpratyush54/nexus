package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	memctx "central-memory/internal/context"
	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

// registerAgentV1Routes exposes cloud agent APIs so clients never need a
// local mem.exe path. Auth is the same Bearer token as the rest of the API.
func (s *Server) registerAgentV1Routes() {
	s.Mux.HandleFunc("GET /v1/agent", s.requireAuth(s.handleAgentDiscovery))
	s.Mux.HandleFunc("GET /v1/agent/projects", s.requireAuth(s.handleProjectList))
	s.Mux.HandleFunc("POST /v1/agent/memory/search", s.requireAuth(s.handleAgentMemorySearch))
	s.Mux.HandleFunc("POST /v1/agent/memory/write", s.requireAuth(s.handleAgentMemoryWrite))
	s.Mux.HandleFunc("POST /v1/agent/mcp", s.requireAuth(s.handleAgentMCP))
}

func (s *Server) handleAgentDiscovery(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(publicAPIBase(r), "/")
	writeJSON(w, http.StatusOK, map[string]any{
		"name":        "nexus-agent-api",
		"version":     "1",
		"description": "Cloud agent API for Nexus memory. Prefer local Nexus MCP when the app is installed; use this HTTPS API + Bearer token when MCP is unavailable. Never hunt for mem.exe.",
		"auth":        "Authorization: Bearer <token from nexus login / portal API tokens>",
		"project":     "Pass project_id in the JSON body, or header X-Nexus-Project. Omit to use your only project, else list /v1/agent/projects.",
		"endpoints": []map[string]any{
			{"method": "GET", "path": "/v1/agent", "purpose": "this discovery document"},
			{"method": "GET", "path": "/v1/agent/projects", "purpose": "list projects for the token"},
			{"method": "POST", "path": "/v1/agent/memory/search", "body": map[string]any{"query": "string", "project_id": "optional uuid", "limit": 20}},
			{"method": "POST", "path": "/v1/agent/memory/write", "body": map[string]any{"key": "topic/key", "content": "20-2000 chars", "project_id": "optional", "level": "project", "scope": "fact"}},
			{"method": "POST", "path": "/v1/agent/mcp", "purpose": "MCP JSON-RPC 2.0 (initialize, tools/list, tools/call) over HTTPS"},
		},
		"mcp_url": base + "/v1/agent/mcp",
		"tools":   mcp.ListTools(),
		"example": map[string]any{
			"search": map[string]any{
				"curl": "curl -s -X POST " + base + "/v1/agent/memory/search -H \"Authorization: Bearer $NEXUS_TOKEN\" -H \"Content-Type: application/json\" -d \"{\\\"query\\\":\\\"redis\\\",\\\"project_id\\\":\\\"YOUR_PROJECT_UUID\\\"}\"",
			},
		},
	})
}

func publicAPIBase(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); v != "" {
		proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
		if proto == "" {
			proto = "https"
		}
		return proto + "://" + v
	}
	if r.Host != "" {
		proto := "https"
		if r.TLS == nil && !strings.Contains(r.Host, "pratyushes.dev") {
			proto = "http"
		}
		return proto + "://" + r.Host
	}
	return "https://api-nexus.pratyushes.dev"
}

type agentMemorySearchReq struct {
	Query     string   `json:"query"`
	ProjectID string   `json:"project_id"`
	Tags      []string `json:"tags"`
	Level     string   `json:"level"`
	Limit     int      `json:"limit"`
}

func (s *Server) handleAgentMemorySearch(w http.ResponseWriter, r *http.Request) {
	var req agentMemorySearchReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	projectID, ok := s.resolveAgentProject(w, r, req.ProjectID)
	if !ok {
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	viewerCtx := store.WithViewer(r.Context(), authSubject(r))
	items, err := s.Store.SearchMemory(viewerCtx, projectID, req.Query, req.Tags, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	if lvl := strings.TrimSpace(req.Level); lvl != "" {
		filtered := items[:0]
		for _, it := range items {
			if it != nil && strings.EqualFold(it.Level, lvl) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		out = append(out, map[string]any{
			"id":         it.ID,
			"key":        it.Key,
			"content":    it.Content,
			"level":      it.Level,
			"scope":      it.Scope,
			"status":     it.Status,
			"confidence": it.Confidence,
			"tags":       it.Tags,
			"source":     it.Source,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": projectID,
		"count":      len(out),
		"items":      out,
	})
}

type agentMemoryWriteReq struct {
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	ProjectID string   `json:"project_id"`
	Level     string   `json:"level"`
	Scope     string   `json:"scope"`
	Tags      []string `json:"tags"`
}

func (s *Server) handleAgentMemoryWrite(w http.ResponseWriter, r *http.Request) {
	var req agentMemoryWriteReq
	if !decodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.Key)
	content := strings.TrimSpace(req.Content)
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if err := store.ValidateMemoryContent(content); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID, ok := s.resolveAgentProject(w, r, req.ProjectID)
	if !ok {
		return
	}
	if !s.authorizePermission(w, r, projectID, store.PermMemoryWrite) {
		return
	}
	level := strings.ToLower(strings.TrimSpace(req.Level))
	switch level {
	case "organization", "project", "personal":
	default:
		level = "project"
	}
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "fact"
	}
	item := &store.MemoryItem{
		ProjectID:  projectID,
		Key:        key,
		Content:    content,
		Level:      level,
		Scope:      scope,
		Tags:       req.Tags,
		Status:     store.StatusProposed,
		Source:     "agent:api",
		ProposedBy: authSubject(r),
		Confidence: 0.85,
	}
	if level == "personal" {
		item.UserID = authSubject(r)
	}
	item.Embedding = s.embedText(r.Context(), memctx.EmbedTextForItem(item.Key, item.Content))
	if err := s.Store.CreateMemoryItem(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create memory: "+err.Error())
		return
	}
	s.notifyProjectActivity(projectID, "MEMORY_PROPOSED", "/app/memory")
	s.publishMemoryLifecycle(item, "MEMORY_PROPOSED", "proposed")
	writeJSON(w, http.StatusCreated, item)
}

// resolveAgentProject picks a project from body, X-Nexus-Project header, or
// the caller's sole membership. Returns false after writing an error.
func (s *Server) resolveAgentProject(w http.ResponseWriter, r *http.Request, bodyProject string) (string, bool) {
	pid := strings.TrimSpace(bodyProject)
	if pid == "" {
		pid = strings.TrimSpace(r.Header.Get("X-Nexus-Project"))
	}
	if pid == "" {
		pid = strings.TrimSpace(r.URL.Query().Get("project_id"))
	}
	if pid != "" {
		if !s.authorizeProject(w, r, pid) {
			return "", false
		}
		return pid, true
	}
	items, err := s.Store.ListProjectsForUser(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list projects: "+err.Error())
		return "", false
	}
	if len(items) == 1 && items[0] != nil && items[0].ID != "" {
		return items[0].ID, true
	}
	ids := make([]string, 0, len(items))
	for _, p := range items {
		if p != nil && p.ID != "" {
			name := p.DisplayName
			if name == "" {
				name = p.FolderName
			}
			ids = append(ids, p.ID+" ("+name+")")
		}
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    400,
			"message": "project_id required (pass JSON project_id, header X-Nexus-Project, or ensure the token has exactly one project)",
			"projects": ids,
		},
	})
	return "", false
}

// handleAgentMCP serves MCP JSON-RPC 2.0 over HTTPS so hosts can point at
// the cloud API instead of spawning a local mem.exe.
func (s *Server) handleAgentMCP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	projectID := strings.TrimSpace(r.Header.Get("X-Nexus-Project"))
	if projectID == "" {
		// Best-effort: try to peek project_id from tools/call arguments later;
		// for initialize/tools/list allow empty and resolve on call.
		var peek struct {
			Method string `json:"method"`
			Params struct {
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &peek)
		if len(peek.Params.Arguments) > 0 {
			var args struct {
				ProjectID string `json:"project_id"`
			}
			_ = json.Unmarshal(peek.Params.Arguments, &args)
			projectID = strings.TrimSpace(args.ProjectID)
		}
	}
	if projectID == "" {
		if items, err := s.Store.ListProjectsForUser(r.Context(), authSubject(r)); err == nil && len(items) == 1 && items[0] != nil {
			projectID = items[0].ID
		}
	}
	if projectID != "" && !s.authorizeProject(w, r, projectID) {
		return
	}

	name := "project"
	if projectID != "" {
		if p, err := s.Store.GetProject(r.Context(), projectID); err == nil && p != nil {
			if p.DisplayName != "" {
				name = p.DisplayName
			} else if p.FolderName != "" {
				name = p.FolderName
			}
		}
	}

	cfg := mcp.Config{
		ProjectID:   projectID,
		ProjectName: name,
		AgentName:   firstNonEmpty(r.Header.Get("X-Nexus-Agent"), "agent-api"),
		Embedder:    s.resolveEmbedder(),
	}
	srv := mcp.NewServer(agentMCPStore{Store: s.Store}, cfg)
	resp := srv.Handle(r.Context(), raw)
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// agentMCPStore adapts store.Store to mcp.Store for cloud MCP JSON-RPC.
type agentMCPStore struct {
	Store store.Store
}

func (a agentMCPStore) SearchMemory(ctx context.Context, projectID, query string, tags []string, limit int) ([]*mcp.MemoryItem, error) {
	items, err := a.Store.SearchMemory(ctx, projectID, query, tags, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*mcp.MemoryItem, 0, len(items))
	for _, item := range items {
		out = append(out, storeToMCPMemory(item))
	}
	return out, nil
}

func (a agentMCPStore) CreateMemoryItem(ctx context.Context, item *mcp.MemoryItem) error {
	row := mcpToStoreMemory(item)
	if err := a.Store.CreateMemoryItem(ctx, row); err != nil {
		return err
	}
	if row.ID != "" {
		item.ID = row.ID
	}
	if row.Status != "" {
		item.Status = row.Status
	}
	return nil
}

func (a agentMCPStore) SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*mcp.Episode, error) {
	eps, err := a.Store.SearchEpisodes(ctx, projectID, errorPattern, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*mcp.Episode, 0, len(eps))
	for _, ep := range eps {
		if ep == nil {
			continue
		}
		out = append(out, &mcp.Episode{
			ID: ep.ID, ProjectID: ep.ProjectID, Title: ep.Title,
			EpisodeType: ep.EpisodeType, Trigger: ep.Trigger,
			RootCause: ep.RootCause, Resolution: ep.Resolution,
			Status: ep.Status, Tags: ep.Tags,
		})
	}
	return out, nil
}

func (a agentMCPStore) CreateEpisode(ctx context.Context, ep *mcp.Episode) error {
	row := &store.Episode{
		ID: ep.ID, ProjectID: ep.ProjectID, Title: ep.Title,
		EpisodeType: ep.EpisodeType, Trigger: ep.Trigger,
		RootCause: ep.RootCause, Resolution: ep.Resolution,
		Status: ep.Status, Tags: ep.Tags,
	}
	if err := a.Store.CreateEpisode(ctx, row); err != nil {
		return err
	}
	ep.ID = row.ID
	return nil
}

func (a agentMCPStore) GetActiveWorkspace(ctx context.Context, projectID string) (*mcp.Workspace, error) {
	ws, err := a.Store.GetActiveWorkspace(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return &mcp.Workspace{}, nil
	}
	return &mcp.Workspace{Branch: ws.Branch, CommitSHA: ws.CommitSHA, IsDirty: ws.IsDirty, Path: ws.Path}, nil
}

func (a agentMCPStore) GetProject(ctx context.Context, id string) (*mcp.Project, error) {
	p, err := a.Store.GetProject(ctx, id)
	if err != nil {
		return nil, err
	}
	return &mcp.Project{DisplayName: p.DisplayName, FolderName: p.FolderName}, nil
}

func storeToMCPMemory(item *store.MemoryItem) *mcp.MemoryItem {
	if item == nil {
		return nil
	}
	return &mcp.MemoryItem{
		ID: item.ID, ProjectID: item.ProjectID, UserID: item.UserID, SessionID: item.SessionID,
		Key: item.Key, Content: item.Content, ContextSnippet: item.ContextSnippet,
		Level: item.Level, Scope: item.Scope, Tags: item.Tags,
		Confidence: item.Confidence, Status: item.Status, Source: item.Source,
		Embedding: item.Embedding,
	}
}

func mcpToStoreMemory(item *mcp.MemoryItem) *store.MemoryItem {
	return &store.MemoryItem{
		ID: item.ID, ProjectID: item.ProjectID, UserID: item.UserID, SessionID: item.SessionID,
		Key: item.Key, Content: item.Content, ContextSnippet: item.ContextSnippet,
		Level: item.Level, Scope: item.Scope, Tags: item.Tags,
		Confidence: item.Confidence, Status: item.Status, Source: item.Source,
		Embedding: item.Embedding,
	}
}
