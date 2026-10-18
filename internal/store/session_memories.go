package store

import (
	"context"
	"sort"
)

// ListSessionMemories looks up session-scoped knowledge by source session ID.
// It applies the same viewer visibility rules as project memory search, but
// avoids truncating a session's results behind thousands of unrelated items.
func (s *MemStore) ListSessionMemories(ctx context.Context, projectID string, sessionIDs []string) ([]*MemoryItem, error) {
	if len(sessionIDs) == 0 {
		return []*MemoryItem{}, nil
	}
	ids := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		ids[id] = true
	}
	viewerID := ViewerFrom(ctx)
	isMember := false
	if viewerID != "" {
		var err error
		isMember, err = s.IsProjectMember(ctx, viewerID, projectID)
		if err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []*MemoryItem{}
	for _, item := range s.memories {
		if item.ProjectID != projectID || !ids[item.SessionID] ||
			(item.Status != StatusConfirmed && item.Status != StatusProposed) || item.SupersededBy != "" {
			continue
		}
		if viewerID == "" {
			vis := NormalizeVisibility(item.Visibility)
			if vis != VisibilityProject && vis != VisibilityPublic {
				continue
			}
		} else if !s.memoryVisibleLocked(item, viewerID, isMember) {
			continue
		}
		items = append(items, cloneMemoryItem(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *PostgresStore) ListSessionMemories(ctx context.Context, projectID string, sessionIDs []string) ([]*MemoryItem, error) {
	if len(sessionIDs) == 0 {
		return []*MemoryItem{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+memoryColumns+` FROM memory_items
		WHERE project_id = $1::uuid AND session_id = ANY($2::uuid[])
		  AND status IN ('CONFIRMED','PROPOSED') AND superseded_by IS NULL
		ORDER BY created_at DESC`, projectID, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	viewerID := ViewerFrom(ctx)
	isMember := false
	if viewerID != "" {
		isMember, err = s.IsProjectMember(ctx, viewerID, projectID)
		if err != nil {
			return nil, err
		}
	}
	items := []*MemoryItem{}
	for rows.Next() {
		item, err := scanMemoryItem(rows)
		if err != nil {
			return nil, err
		}
		if viewerID == "" {
			vis := NormalizeVisibility(item.Visibility)
			if vis != VisibilityProject && vis != VisibilityPublic {
				continue
			}
		} else {
			hasShare := false
			if NormalizeVisibility(item.Visibility) == VisibilityShared {
				hasShare, err = s.hasMemoryShare(ctx, item.ID, viewerID, item.ProjectID)
				if err != nil {
					return nil, err
				}
			}
			if !CanViewMemory(item, viewerID, isMember, hasShare) {
				continue
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
