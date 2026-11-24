package store

import (
	"context"
	"sort"
	"strings"
	"time"
)

// SessionGrantInfo is one person a session is currently shared with.
type SessionGrantInfo struct {
	UserID    string    `json:"user_id"`
	Live      bool      `json:"live"`
	VersionID string    `json:"version_id,omitempty"`
	GrantedAt time.Time `json:"granted_at"`
}

// ListAgentSessionGrants returns the active per-person grants of a session,
// oldest first. Project-team sharing is the session's visibility, not a row.
func (m *MemStore) ListAgentSessionGrants(ctx context.Context, sessionID string) ([]SessionGrantInfo, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.agentSessions[sessionID] == nil {
		return nil, ErrNotFound
	}
	out := []SessionGrantInfo{}
	for _, g := range m.agentGrants[sessionID] {
		if g.Revoked || g.Team || g.Grantee == "" {
			continue
		}
		out = append(out, SessionGrantInfo{UserID: g.Grantee, Live: g.Live, VersionID: g.VersionID, GrantedAt: g.CreatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GrantedAt.Before(out[j].GrantedAt) })
	return out, nil
}

// ListAgentSessionGrants: see MemStore.ListAgentSessionGrants.
func (s *PostgresStore) ListAgentSessionGrants(ctx context.Context, sessionID string) ([]SessionGrantInfo, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT grantee_user_id::text, live, COALESCE(version_id::text, ''), created_at
		  FROM session_grants
		 WHERE session_id = $1::uuid AND revoked_at IS NULL AND grantee_user_id IS NOT NULL
		 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionGrantInfo{}
	for rows.Next() {
		var g SessionGrantInfo
		if err := rows.Scan(&g.UserID, &g.Live, &g.VersionID, &g.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
