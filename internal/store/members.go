package store

// members.go — explicit project membership (issue #149).
//
// Membership used to be derived: any workspace row conferred access, so any
// authenticated account could claim any project by registering a workspace
// on it. Membership is now explicit: the project creator (claimed at
// resolve time) plus granted rows in project_members (migration 012).
// Workspace registration requires membership; it never creates it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ---- MemStore ----

// IsProjectMember reports whether userID may access projectID: the
// project's creator or an explicit grant. Empty inputs are never members.
func (s *MemStore) IsProjectMember(ctx context.Context, userID, projectID string) (bool, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(projectID) == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.projects[projectID]; ok && p != nil && p.CreatedBy == userID {
		return true, nil
	}
	if s.members[projectID][userID] {
		return true, nil
	}
	_, hasRole := s.memberRoles[projectID][userID]
	return hasRole, nil
}

// ClaimProject records userID as creator iff none is set. Returns true
// when this call claimed it.
func (s *MemStore) ClaimProject(ctx context.Context, projectID, userID string) (bool, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(userID) == "" {
		return false, errors.New("store: claim requires project id and user id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[projectID]
	if !ok || p == nil {
		return false, fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	if strings.TrimSpace(p.CreatedBy) != "" {
		return false, nil
	}
	p.CreatedBy = userID
	return true, nil
}

// GrantMember adds userID as a project member with EDITOR role (issue #163).
// Idempotent: existing members keep their current role.
func (s *MemStore) GrantMember(ctx context.Context, projectID, userID, grantedBy string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("store: grant requires project id and user id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	if s.members[projectID] == nil {
		s.members[projectID] = make(map[string]bool)
	}
	if s.memberRoles == nil {
		s.memberRoles = make(map[string]map[string]string)
	}
	if s.memberRoles[projectID] == nil {
		s.memberRoles[projectID] = make(map[string]string)
	}
	already := s.members[projectID][userID] || s.memberRoles[projectID][userID] != ""
	s.members[projectID][userID] = true
	if !already {
		s.memberRoles[projectID][userID] = RoleEditor
	}
	return nil
}

// RevokeMember removes a grant. The creator cannot be revoked (ownership is
// structural); revoking a non-member is a no-op nil.
func (s *MemStore) RevokeMember(ctx context.Context, projectID, userID string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("store: revoke requires project id and user id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[projectID]; ok && p != nil && p.CreatedBy == userID {
		return fmt.Errorf("store: cannot revoke project creator: %w", ErrConflict)
	}
	delete(s.members[projectID], userID)
	delete(s.memberRoles[projectID], userID)
	return nil
}

// ListMembers returns granted user IDs on the project, sorted.
func (s *MemStore) ListMembers(ctx context.Context, projectID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for u := range s.members[projectID] {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for u := range s.memberRoles[projectID] {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---- PostgresStore ----

// IsProjectMember reports whether userID may access projectID: creator or
// an explicit grant row. Malformed UUIDs fail closed with an error.
func (s *PostgresStore) IsProjectMember(ctx context.Context, userID, projectID string) (bool, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(projectID) == "" {
		return false, nil
	}
	var member bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM projects WHERE id = $2::uuid AND created_by = $1::uuid)
		    OR EXISTS(SELECT 1 FROM project_members WHERE project_id = $2::uuid AND user_id = $1::uuid)`,
		userID, projectID).Scan(&member); err != nil {
		return false, fmt.Errorf("store: membership check: %w", err)
	}
	return member, nil
}

// ClaimProject records userID as creator iff none is set.
func (s *PostgresStore) ClaimProject(ctx context.Context, projectID, userID string) (bool, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(userID) == "" {
		return false, errors.New("store: claim requires project id and user id")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET created_by = $2::uuid
		  WHERE id = $1::uuid AND created_by IS NULL`,
		projectID, userID)
	if err != nil {
		return false, fmt.Errorf("store: claim project: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// GrantMember adds userID as a project member with EDITOR role (issue #163).
// Idempotent: existing members keep their current role (ON CONFLICT DO NOTHING).
func (s *PostgresStore) GrantMember(ctx context.Context, projectID, userID, grantedBy string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("store: grant requires project id and user id")
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, role, granted_by)
		 VALUES ($1::uuid, $2::uuid, $3, $4::uuid)
		 ON CONFLICT DO NOTHING`,
		projectID, userID, RoleEditor, nullText(grantedBy)); err != nil {
		return fmt.Errorf("store: grant member: %w", err)
	}
	return nil
}

// RevokeMember removes a grant; the creator cannot be revoked.
func (s *PostgresStore) RevokeMember(ctx context.Context, projectID, userID string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("store: revoke requires project id and user id")
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM project_members
		  WHERE project_id = $1::uuid AND user_id = $2::uuid
		    AND NOT EXISTS(SELECT 1 FROM projects WHERE id = $1::uuid AND created_by = $2::uuid)`,
		projectID, userID)
	if err != nil {
		return fmt.Errorf("store: revoke member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var creator string
		if qerr := s.pool.QueryRow(ctx,
			`SELECT COALESCE(created_by::TEXT, '') FROM projects WHERE id = $1::uuid`,
			projectID).Scan(&creator); qerr == nil && creator == userID {
			return fmt.Errorf("store: cannot revoke project creator: %w", ErrConflict)
		}
	}
	return nil
}

// ListMembers returns granted user IDs on the project, sorted.
func (s *PostgresStore) ListMembers(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id::TEXT FROM project_members WHERE project_id = $1::uuid ORDER BY user_id::TEXT`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list members: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("store: list members scan: %w", err)
		}
		out = append(out, u)
	}
	if out == nil {
		out = []string{}
	}
	return out, rows.Err()
}

// GetWorkspace fetches one workspace by id.
func (s *PostgresStore) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	ws, err := scanWorkspace(s.pool.QueryRow(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1::uuid`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: workspace %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get workspace: %w", err)
	}
	return ws, nil
}
