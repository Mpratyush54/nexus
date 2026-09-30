package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// OrgSessionShare is one active person grant in an org project (D20 Shares).
// It never includes titles, summaries, or file names.
type OrgSessionShare struct {
	SessionID string    `json:"session_id"`
	ProjectID string    `json:"project_id"`
	OwnerID   string    `json:"owner_id"`
	GranteeID string    `json:"grantee_id"`
	Live      bool      `json:"live"`
	CreatedAt time.Time `json:"created_at"`
}

// OrgSharesStore lists and revokes session grants across org projects without
// exposing session content (admin Shares tab).
type OrgSharesStore interface {
	ListOrgSessionShares(ctx context.Context, orgID string) ([]OrgSessionShare, error)
	RevokeOrgSessionShare(ctx context.Context, orgID, sessionID, granteeUserID string) error
}

func (m *MemStore) ListOrgSessionShares(ctx context.Context, orgID string) ([]OrgSessionShare, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	inOrg := map[string]bool{}
	for _, p := range m.projects {
		if p != nil && p.OrgID == orgID {
			inOrg[p.ID] = true
		}
	}
	var out []OrgSessionShare
	for sid, sess := range m.agentSessions {
		if sess == nil || !inOrg[sess.ProjectID] {
			continue
		}
		for _, g := range m.agentGrants[sid] {
			if g.Revoked || g.Grantee == "" || g.Team {
				continue
			}
			created := g.CreatedAt
			if created.IsZero() {
				created = sess.StartedAt
			}
			out = append(out, OrgSessionShare{
				SessionID: sid,
				ProjectID: sess.ProjectID,
				OwnerID:   sess.OwnerUserID,
				GranteeID: g.Grantee,
				Live:      g.Live,
				CreatedAt: created,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			if out[i].SessionID == out[j].SessionID {
				return out[i].GranteeID < out[j].GranteeID
			}
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if out == nil {
		out = []OrgSessionShare{}
	}
	return out, nil
}

func (m *MemStore) RevokeOrgSessionShare(ctx context.Context, orgID, sessionID, granteeUserID string) error {
	orgID = strings.TrimSpace(orgID)
	sessionID = strings.TrimSpace(sessionID)
	granteeUserID = strings.TrimSpace(granteeUserID)
	if orgID == "" || sessionID == "" || granteeUserID == "" {
		return errors.New("store: org share revoke requires org, session, and user")
	}
	m.mu.RLock()
	sess := m.agentSessions[sessionID]
	if sess == nil {
		m.mu.RUnlock()
		return ErrNotFound
	}
	proj := m.projects[sess.ProjectID]
	if proj == nil || proj.OrgID != orgID {
		m.mu.RUnlock()
		return ErrNotFound
	}
	m.mu.RUnlock()
	return m.RevokeAgentSessionGrant(ctx, sessionID, granteeUserID)
}

func (s *PostgresStore) ListOrgSessionShares(ctx context.Context, orgID string) ([]OrgSessionShare, error) {
	if !looksLikeUUID(orgID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT g.session_id::text, s.project_id::text, COALESCE(s.owner_user_id::text,''),
		       g.grantee_user_id::text, g.live, g.created_at
		FROM session_grants g
		JOIN agent_sessions s ON s.id = g.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE p.org_id = $1::uuid
		  AND g.revoked_at IS NULL
		  AND g.grantee_user_id IS NOT NULL
		ORDER BY g.created_at DESC, g.session_id, g.grantee_user_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgSessionShare
	for rows.Next() {
		var row OrgSessionShare
		if err := rows.Scan(&row.SessionID, &row.ProjectID, &row.OwnerID, &row.GranteeID, &row.Live, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if out == nil {
		out = []OrgSessionShare{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) RevokeOrgSessionShare(ctx context.Context, orgID, sessionID, granteeUserID string) error {
	if !looksLikeUUID(orgID) || !looksLikeUUID(sessionID) {
		return ErrNotFound
	}
	granteeUserID = strings.TrimSpace(granteeUserID)
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int
		FROM agent_sessions s
		JOIN projects p ON p.id = s.project_id
		WHERE s.id = $1::uuid AND p.org_id = $2::uuid`, sessionID, orgID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.RevokeAgentSessionGrant(ctx, sessionID, granteeUserID)
}

// Ensure interfaces compile against MemStore / PostgresStore.
var (
	_ OrgSharesStore = (*MemStore)(nil)
	_ OrgSharesStore = (*PostgresStore)(nil)
)
