package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

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
			{"method": "GET", "path": "/v1/timeline", "purpose": "cross-project session cards the caller can see"},
			{"method": "POST", "path": "/v1/agent-sessions", "purpose": "create or touch a harness session by native id"},
			{"method": "GET", "path": "/v1/agent-sessions/{id}/fetch", "purpose": "paged raw turns for a session the caller can read"},
			{"method": "GET", "path": "/v1/agent-sessions/{id}/summary", "purpose": "session summary for a session the caller can read"},
			{"method": "DELETE", "path": "/v1/agent-sessions/{id}", "purpose": "owner only: delete a chat with its turns, usage and shares (memories stay)"},
			{"method": "POST", "path": "/v1/agent-sessions/{id}/turns/dedupe", "purpose": "owner only: remove undated copies of dated turns and harness-only user envelopes; ?mode=all|exact|copies|envelopes, ?dry_run=1 to count"},
			{"method": "GET", "path": "/v1/projects/{id}/knowledge", "purpose": "project knowledge; status is active, superseded, or forgotten"},
			{"method": "POST", "path": "/memory/{id}/pin", "purpose": "pin a memory item"},
			{"method": "POST", "path": "/memory/{id}/scope", "purpose": "change memory level: session, project, personal, or organization"},
			{"method": "POST", "path": "/memory/{id}/forget", "purpose": "soft-delete a memory item"},
			{"method": "POST", "path": "/memory/{id}/auto-promote", "purpose": "promote a session fact after secret and PII redaction"},
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
	// Agent retrieval counts as use (decay clock, plan §1.7).
	recordMemoryUse(r.Context(), s.Store, items)
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
	agentID := authAgentID(r)
	if agentID != "" {
		if ps, ok := s.agentPermissionStore(); ok {
			if perm, err := ps.GetAgentPermission(r.Context(), projectID, agentID); err == nil {
				if err := store.CheckAgentToolAllowed(perm, "memory_write"); err != nil {
					writeError(w, http.StatusForbidden, err.Error())
					return
				}
				if perm.RateLimit > 0 && !s.mcpRateAllowed("mcp:"+projectID+":"+agentID, perm.RateLimit) {
					writeError(w, http.StatusTooManyRequests, "agent rate limit exceeded")
					return
				}
			}
		}
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
	src := "agent:api"
	if agentID != "" {
		src = "agent:" + agentID
	}
	status := store.StatusProposed
	if agentID != "" {
		if ps, ok := s.agentPermissionStore(); ok {
			if perm, err := ps.GetAgentPermission(r.Context(), projectID, agentID); err == nil &&
				perm != nil && perm.Mode == store.AgentModeFull {
				status = store.StatusConfirmed
			}
		}
	}
	item := &store.MemoryItem{
		ProjectID:  projectID,
		Key:        key,
		Content:    content,
		Level:      level,
		Scope:      scope,
		Tags:       req.Tags,
		Status:     status,
		Source:     src,
		ProposedBy: authSubject(r),
		Confidence: 0.85,
	}
	if level == "personal" {
		item.UserID = authSubject(r)
	}
	s.embedItem(r.Context(), item)
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
			"code":     400,
			"message":  "project_id required (pass JSON project_id, header X-Nexus-Project, or ensure the token has exactly one project)",
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

	name := ""
	if projectID != "" {
		if p, err := s.Store.GetProject(r.Context(), projectID); err == nil && p != nil {
			if p.DisplayName != "" {
				name = p.DisplayName
			} else if p.FolderName != "" {
				name = p.FolderName
			}
		}
	}

	bound := authAgentID(r)
	headerAgent := strings.TrimSpace(r.Header.Get("X-Nexus-Agent"))
	agentName := bound
	if agentName == "" {
		agentName = firstNonEmpty(headerAgent, "agent-api")
	} else if headerAgent != "" && !strings.EqualFold(headerAgent, bound) {
		writeError(w, http.StatusForbidden, "X-Nexus-Agent does not match token-bound agent_id")
		return
	}

	var access *mcp.AgentAccess
	if projectID != "" && agentName != "" && agentName != "agent-api" {
		if ps, ok := s.agentPermissionStore(); ok {
			if perm, err := ps.GetAgentPermission(r.Context(), projectID, agentName); err == nil && perm != nil {
				access = &mcp.AgentAccess{
					AgentID:   perm.AgentID,
					Mode:      perm.Mode,
					RateLimit: perm.RateLimit,
					Tools:     perm.Tools,
				}
			}
		}
		// Bound agents always get a gate even before Agents page saves a row.
		if access == nil {
			access = &mcp.AgentAccess{
				AgentID:   agentName,
				Mode:      mcp.ModeFull,
				RateLimit: 60,
			}
		}
	}

	cfg := mcp.Config{
		ProjectID:   projectID,
		ProjectName: name,
		AgentName:   agentName,
		Embedder:    s.resolveEmbedder(),
		// Queries get the model's query prefix (nil = Embedder).
		QueryEmbedder: s.queryEmbedder(),
		Access:        access,
		RateLimiter:   s.agentMCPLimiter(),
	}
	srv := mcp.NewServer(agentMCPStore{Store: s.Store, UserID: authSubject(r), Embeds: s}, cfg)
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
// Reads run as UserID (store.WithViewer), so an agent sees exactly what its
// user may see: project rows plus that user's own private (personal) rows.
type agentMCPStore struct {
	Store  store.Store
	UserID string
	// Embeds (optional) tags written vectors with their model and vetoes
	// vector search while the embedding model is down (the MCP query
	// embedder then returns HashEmbed, which cannot rank model vectors).
	Embeds *Server
}

func (a agentMCPStore) viewerCtx(ctx context.Context) context.Context {
	return store.WithViewer(ctx, a.UserID)
}

func (a agentMCPStore) SearchMemory(ctx context.Context, projectID, query string, tags []string, limit int) ([]*mcp.MemoryItem, error) {
	items, err := a.Store.SearchMemory(a.viewerCtx(ctx), projectID, query, tags, limit)
	if err != nil {
		return nil, err
	}
	return storeToMCPMemories(items), nil
}

// SearchMemoryVector gives cloud agents the semantic path mcp.handleMemorySearch
// prefers. Stores without vector search report no rows, so the handler falls
// back to keyword search; so does a search where no row reaches
// the model's similarity floor. With Embeds set, only rows embedded by the
// server's model are compared (the MCP query vector came from that model).
func (a agentMCPStore) SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*mcp.MemoryItem, error) {
	if a.Embeds != nil && !a.Embeds.vectorSearchUsable() {
		return nil, nil // keyword fallback
	}
	var p *memctx.Provider
	if a.Embeds != nil {
		p = a.Embeds.EmbedProvider
	}
	search := modelVectorSearchFor(a.Store, p)
	if search == nil {
		return nil, nil
	}
	items, err := search(a.viewerCtx(ctx), projectID, queryVec, limit)
	if err != nil {
		return nil, err
	}
	return storeToMCPMemories(items), nil
}

// RecordMemoryUse lets mcp.handleMemorySearch reset the decay clock on rows
// it serves to a cloud agent. Stores without use tracking skip it.
func (a agentMCPStore) RecordMemoryUse(ctx context.Context, id string) error {
	ru, ok := a.Store.(interface {
		RecordMemoryUse(context.Context, string) error
	})
	if !ok {
		return nil
	}
	return ru.RecordMemoryUse(ctx, id)
}

func storeToMCPMemories(items []*store.MemoryItem) []*mcp.MemoryItem {
	out := make([]*mcp.MemoryItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, storeToMCPMemory(item))
	}
	return out
}

func (a agentMCPStore) CreateMemoryItem(ctx context.Context, item *mcp.MemoryItem) error {
	row := mcpToStoreMemory(item)
	// Attribution is server-derived, as on POST /memory: the authenticated
	// user wrote it, and a personal row belongs to that user (so it is
	// private to them, not to whatever user_id the agent passed).
	if a.UserID != "" {
		row.ProposedBy = a.UserID
		if strings.EqualFold(strings.TrimSpace(row.Level), store.LevelPersonal) {
			row.UserID = a.UserID
		}
	}
	if a.Embeds != nil {
		a.Embeds.tagEmbeddingModel(row)
	}
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

// ResolveWorkspaceProject picks, for a workspace_info call that named no
// project, the caller's project whose active workspace (the caller's own
// machine, live heartbeat) was seen most recently. options lists every
// project the caller can use so the agent can pass one.
func (a agentMCPStore) ResolveWorkspaceProject(ctx context.Context) (string, []mcp.ProjectRef, error) {
	projects, err := a.Store.ListProjectsForUser(ctx, a.UserID)
	if err != nil {
		return "", nil, err
	}
	var best string
	var bestSeen time.Time
	options := make([]mcp.ProjectRef, 0, len(projects))
	for _, p := range projects {
		if p == nil || p.ID == "" {
			continue
		}
		options = append(options, mcp.ProjectRef{ID: p.ID, Name: firstNonEmpty(p.DisplayName, p.FolderName, p.ID)})
		ws, err := a.Store.GetActiveWorkspace(ctx, p.ID)
		if err != nil || ws == nil || ws.UserID != a.UserID {
			continue
		}
		if best == "" || ws.LastSeen.After(bestSeen) {
			best, bestSeen = p.ID, ws.LastSeen
		}
	}
	if len(options) == 1 {
		best = options[0].ID
	}
	return best, options, nil
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
		Embedding: item.Embedding, Category: item.Category,
		FilesAffected: append([]string(nil), item.FilesAffected...),
		CreatedAt:     item.CreatedAt, UpdatedAt: item.UpdatedAt,
		StaleSince: item.StaleSince,
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
