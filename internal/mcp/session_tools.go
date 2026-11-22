package mcp

import (
	"context"
	"encoding/json"
	"strings"
)

// TurnPage is one raw turn returned by session_fetch.
type TurnPage struct {
	Idx  int    `json:"idx"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// SessionCatalog is implemented by the cloud MCP adapter. Stores that
// cannot read cloud sessions leave the tools registered and return a
// clear error from the call.
type SessionCatalog interface {
	GetSessionSummary(ctx context.Context, sessionID string) (string, error)
	FetchSessionTurns(ctx context.Context, sessionID, cursor string, limit int) ([]TurnPage, string, error)
	ProjectKnowledge(ctx context.Context, projectID string, limit int) ([]*MemoryItem, error)
}

func publicMemoryWord(stored string) string {
	switch strings.ToUpper(strings.TrimSpace(stored)) {
	case "", "PROPOSED", "CONFIRMED", "ACTIVE":
		return "active"
	case "REJECTED", "FORGOTTEN":
		return "forgotten"
	case "SUPERSEDED":
		return "superseded"
	default:
		return strings.ToLower(strings.TrimSpace(stored))
	}
}

func (s *Server) catalog() (SessionCatalog, *RPCError) {
	if s == nil || s.store == nil {
		return nil, &RPCError{Code: ErrInternal, Message: "no store configured"}
	}
	cat, ok := s.store.(SessionCatalog)
	if !ok {
		return nil, &RPCError{Code: ErrInternal, Message: "Nexus is offline; memory unavailable"}
	}
	return cat, nil
}

func (s *Server) handleSessionSummary(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	cat, rpcErr := s.catalog()
	if rpcErr != nil {
		return nil, rpcErr
	}
	var a struct {
		SessionID string `json:"session_id"`
	}
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(a.SessionID) == "" {
		return nil, invalidParams("missing required field: session_id")
	}
	summary, err := cat.GetSessionSummary(ctx, a.SessionID)
	if err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: err.Error()}
	}
	return map[string]any{"summary": summary}, nil
}

func (s *Server) handleSessionFetch(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	cat, rpcErr := s.catalog()
	if rpcErr != nil {
		return nil, rpcErr
	}
	var a struct {
		SessionID string `json:"session_id"`
		Cursor    string `json:"cursor"`
		Limit     int    `json:"limit"`
	}
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(a.SessionID) == "" {
		return nil, invalidParams("missing required field: session_id")
	}
	turns, next, err := cat.FetchSessionTurns(ctx, a.SessionID, a.Cursor, a.Limit)
	if err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: err.Error()}
	}
	if turns == nil {
		turns = []TurnPage{}
	}
	return map[string]any{"items": turns, "next_cursor": next}, nil
}

func (s *Server) handleProjectKnowledgeTool(ctx context.Context, raw json.RawMessage) (any, *RPCError) {
	cat, rpcErr := s.catalog()
	if rpcErr != nil {
		return nil, rpcErr
	}
	var a struct {
		ProjectID string `json:"project_id"`
		Limit     int    `json:"limit"`
	}
	if rpcErr := decodeArgs(raw, &a); rpcErr != nil {
		return nil, rpcErr
	}
	projectID := strings.TrimSpace(a.ProjectID)
	if projectID == "" {
		projectID = s.cfg.ProjectID
	}
	if projectID == "" {
		return nil, invalidParams("missing required field: project_id")
	}
	items, err := cat.ProjectKnowledge(ctx, projectID, a.Limit)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: err.Error()}
	}
	// The project brief leads: it is the one-screen overview, then the top
	// memories follow (without a second copy of the brief row).
	brief := s.projectBrief(ctx, projectID)
	out := make([]map[string]any, 0, len(items)+1)
	if brief != "" {
		out = append(out, map[string]any{
			"key": ProjectBriefKey, "content": brief, "level": "project",
			"status": "active", "confidence": 1.0,
		})
		items = withoutKey(items, ProjectBriefKey)
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		row := map[string]any{
			"id": item.ID, "key": item.Key, "content": item.Content,
			"level": item.Level, "status": publicMemoryWord(item.Status),
			"confidence": item.Confidence,
		}
		if d := itemDate(item); d != "" {
			row["date"] = d
		}
		if src := itemSource(item); src != "" {
			row["source"] = src
		}
		if files := itemFiles(item); files != "" {
			row["files"] = files
		}
		if item.StaleSince != nil {
			row["stale"] = true
			row["note"] = StaleNote
		}
		out = append(out, row)
	}
	result := map[string]any{"items": out, "count": len(out)}
	if brief != "" {
		result["brief"] = brief
	}
	return result, nil
}
