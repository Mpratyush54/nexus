// agent_routes.go — GET/PUT/DELETE /projects/{id}/agents + MCP tool-call
// event ingest (issue #166 / implementation-plan-v2.md §6).
//
// Agent permission CRUD uses AgentPermissionStore. MCP tool calls POST to
// /projects/{id}/mcp/tool-calls, which AppendEvents MCP_TOOL_CALL and fans
// out over the WebSocket hub so the PWA activity feed updates live.

package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
)

// EventMCPToolCall is the durable / WS event type for agent MCP activity.
const EventMCPToolCall = "MCP_TOOL_CALL"

func (s *Server) agentPermissionStore() (store.AgentPermissionStore, bool) {
	ps, ok := s.Store.(store.AgentPermissionStore)
	return ps, ok
}

func (s *Server) registerAgentRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/agents", s.requireAuth(s.handleAgentList))
	s.Mux.HandleFunc("PUT /projects/{id}/agents/{agentId}", s.requireAuth(s.handleAgentPut))
	s.Mux.HandleFunc("DELETE /projects/{id}/agents/{agentId}", s.requireAuth(s.handleAgentDelete))
	s.Mux.HandleFunc("GET /projects/{id}/agents/{agentId}", s.requireAuth(s.handleAgentGet))
	s.Mux.HandleFunc("POST /projects/{id}/mcp/tool-calls", s.requireAuth(s.handleMCPToolCall))
}

type agentPutRequest struct {
	Mode      string          `json:"mode"`
	RateLimit *int            `json:"rate_limit"`
	Tools     map[string]bool `json:"tools"`
}

func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	ps, ok := s.agentPermissionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent permissions not supported by configured store")
		return
	}
	items, err := ps.ListAgentPermissions(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list agents: "+err.Error())
		return
	}
	if items == nil {
		items = []*store.AgentPermission{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleAgentGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	agentID := strings.TrimSpace(r.PathValue("agentId"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agentId path parameter is required")
		return
	}
	ps, ok := s.agentPermissionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent permissions not supported by configured store")
		return
	}
	perm, err := ps.GetAgentPermission(r.Context(), id, agentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "agent permission not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load agent: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, perm)
}

func (s *Server) handleAgentPut(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	agentID := strings.TrimSpace(r.PathValue("agentId"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agentId path parameter is required")
		return
	}
	ps, ok := s.agentPermissionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent permissions not supported by configured store")
		return
	}
	var req agentPutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rate := store.DefaultAgentRateLimit
	if req.RateLimit != nil {
		rate = *req.RateLimit
	}
	perm := &store.AgentPermission{
		ProjectID: id,
		AgentID:   agentID,
		Mode:      req.Mode,
		RateLimit: rate,
		Tools:     req.Tools,
	}
	if err := ps.SetAgentPermission(r.Context(), perm); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		if isInputError(err) || strings.Contains(err.Error(), "invalid agent mode") ||
			strings.Contains(err.Error(), "rate_limit") || strings.Contains(err.Error(), "is required") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not set agent permission: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, perm)
}

func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	agentID := strings.TrimSpace(r.PathValue("agentId"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agentId path parameter is required")
		return
	}
	ps, ok := s.agentPermissionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent permissions not supported by configured store")
		return
	}
	if err := ps.DeleteAgentPermission(r.Context(), id, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete agent permission: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": id, "agent_id": agentID, "deleted": true})
}

type mcpToolCallRequest struct {
	AgentID    string         `json:"agent_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	SessionID  string         `json:"session_id,omitempty"`
}

// handleMCPToolCall records one MCP tool invocation as MCP_TOOL_CALL and
// publishes it on the project WebSocket hub.
func (s *Server) handleMCPToolCall(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	var req mcpToolCallRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tool := strings.TrimSpace(req.ToolName)
	if tool == "" {
		writeError(w, http.StatusBadRequest, "tool_name is required")
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		agentID = "unknown"
	}
	// Enforce configured permissions server-side when present (defense in depth).
	if ps, ok := s.agentPermissionStore(); ok {
		if perm, err := ps.GetAgentPermission(r.Context(), id, agentID); err == nil {
			if err := store.CheckAgentToolAllowed(perm, tool); err != nil {
				writeError(w, http.StatusForbidden, err.Error())
				return
			}
			if perm.RateLimit > 0 {
				scope := "mcp:" + id + ":" + agentID
				if !s.mcpRateAllowed(scope, perm.RateLimit) {
					writeError(w, http.StatusTooManyRequests, "agent rate limit exceeded")
					return
				}
			}
		}
	}
	payload := map[string]any{
		"agent_id":  agentID,
		"tool_name": tool,
		"via":       "MCP",
	}
	if req.Arguments != nil {
		payload["arguments"] = req.Arguments
	}
	if req.Result != nil {
		payload["result"] = req.Result
	}
	if req.Error != "" {
		payload["error"] = req.Error
	}
	if req.DurationMs > 0 {
		payload["duration_ms"] = req.DurationMs
	}
	ev := &store.Event{
		ProjectID: id,
		SessionID: strings.TrimSpace(req.SessionID),
		UserID:    authSubject(r),
		// Agent name lives in payload: events.agent_id is UUID (registry id),
		// while MCP agents are free-form strings like "cursor-agent".
		EventType: EventMCPToolCall,
		Payload:   payload,
	}
	if err := s.Store.AppendEvent(r.Context(), ev); err != nil {
		writeError(w, http.StatusInternalServerError, "could not append event: "+err.Error())
		return
	}
	s.publishMCPToolCall(ev)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         ev.ID,
		"event_type": ev.EventType,
		"created_at": ev.CreatedAt,
		"payload":    payload,
	})
}

// publishMCPToolCall fans a MCP_TOOL_CALL event to the hub (nil-safe).
func (s *Server) publishMCPToolCall(ev *store.Event) {
	if ev == nil || strings.TrimSpace(ev.ProjectID) == "" {
		return
	}
	s.steerMu.RLock()
	h := s.hub
	s.steerMu.RUnlock()
	if h == nil {
		return
	}
	payload := map[string]any{
		"id":         ev.ID,
		"project_id": ev.ProjectID,
		"event_type": ev.EventType,
		"payload":    ev.Payload,
		"created_at": ev.CreatedAt.UTC().Format(time.RFC3339),
	}
	if ev.SessionID != "" {
		payload["session_id"] = ev.SessionID
	}
	if ev.UserID != "" {
		payload["user_id"] = ev.UserID
	}
	if aid, _ := ev.Payload["agent_id"].(string); aid != "" {
		payload["agent_id"] = aid
	} else if ev.AgentID != "" {
		payload["agent_id"] = ev.AgentID
	}
	func() {
		defer func() { _ = recover() }()
		h.PublishEvent(ev.ProjectID, ev.SessionID, ev.EventType, payload, ev.UserID)
	}()
}

// mcpRateAllowed enforces per-agent calls/minute.
func (s *Server) mcpRateAllowed(scope string, callsPerMinute int) bool {
	if callsPerMinute <= 0 {
		return true
	}
	if s.mcpAgentRates == nil {
		s.mcpAgentRates = newAgentMinuteLimiter()
	}
	return s.mcpAgentRates.allow(scope, callsPerMinute)
}

// agentMinuteLimiter is a sliding 60s window counter keyed by agent scope.
type agentMinuteLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newAgentMinuteLimiter() *agentMinuteLimiter {
	return &agentMinuteLimiter{hits: make(map[string][]time.Time)}
}

func (l *agentMinuteLimiter) allow(key string, limit int) bool {
	if l == nil || limit <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= limit {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}

// LogMCPToolCall is a package helper for internal emitters (tests / steers).
func LogMCPToolCall(ctx context.Context, st store.Store, hub *Hub, projectID, agentID, tool string, args, result map[string]any, errMsg string) (*store.Event, error) {
	payload := map[string]any{
		"agent_id":  agentID,
		"tool_name": tool,
		"via":       "MCP",
	}
	if args != nil {
		payload["arguments"] = args
	}
	if result != nil {
		payload["result"] = result
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	ev := &store.Event{
		ProjectID: projectID,
		EventType: EventMCPToolCall,
		Payload:   payload,
	}
	if err := st.AppendEvent(ctx, ev); err != nil {
		return nil, err
	}
	if hub != nil {
		hub.PublishEvent(projectID, "", EventMCPToolCall, map[string]any{
			"id": ev.ID, "project_id": projectID, "event_type": EventMCPToolCall,
			"agent_id": agentID, "payload": payload,
		}, "")
	}
	return ev, nil
}
