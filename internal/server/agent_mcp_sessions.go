package server

import (
	"context"
	"errors"
	"sort"
	"strings"

	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

func (a agentMCPStore) cloud() (store.AgentCloudStore, error) {
	cs, ok := a.Store.(store.AgentCloudStore)
	if !ok {
		return nil, errors.New("Nexus is offline; memory unavailable")
	}
	return cs, nil
}

func (a agentMCPStore) GetSessionSummary(ctx context.Context, sessionID string) (string, error) {
	cs, err := a.cloud()
	if err != nil {
		return "", err
	}
	ok, err := cs.CanReadAgentSession(ctx, a.UserID, sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("session is private")
	}
	row, err := cs.GetAgentSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return row.Summary, nil
}

func (a agentMCPStore) FetchSessionTurns(ctx context.Context, sessionID, cursor string, limit int) ([]mcp.TurnPage, string, error) {
	cs, err := a.cloud()
	if err != nil {
		return nil, "", err
	}
	ok, err := cs.CanReadAgentSession(ctx, a.UserID, sessionID)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", errors.New("session is private")
	}
	turns, err := cs.ListSessionTurns(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = 20
	}
	var after *int
	if strings.TrimSpace(cursor) != "" {
		n, convErr := strconvAtoi(cursor)
		if convErr != nil {
			return nil, "", errors.New("cursor must be a decimal turn index")
		}
		after = &n
	}
	page, next := pageSessionTurns(turns, after, limit)
	out := make([]mcp.TurnPage, 0, len(page))
	for _, turn := range page {
		out = append(out, mcp.TurnPage{Idx: turn.Idx, Role: turn.Role, Text: turn.TextPreview})
	}
	return out, next, nil
}

func strconvAtoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("cursor must be a decimal turn index")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func (a agentMCPStore) ProjectKnowledge(ctx context.Context, projectID string, limit int) ([]*mcp.MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	found, err := a.Store.SearchMemory(store.WithViewer(ctx, a.UserID), projectID, "", nil, 200)
	if err != nil {
		return nil, err
	}
	items := make([]*mcp.MemoryItem, 0, len(found))
	for _, item := range found {
		if item == nil || !projectKnowledgeLevel(item.Level) {
			continue
		}
		cp := storeToMCPMemory(item)
		items = append(items, cp)
	}
	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := pinnedTag(items[i]), pinnedTag(items[j])
		if pi != pj {
			return pi
		}
		return items[i].Confidence > items[j].Confidence
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func pinnedTag(item *mcp.MemoryItem) bool {
	if item == nil {
		return false
	}
	for _, tag := range item.Tags {
		if strings.EqualFold(tag, "pinned") {
			return true
		}
	}
	return false
}
