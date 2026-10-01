package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrInviteExpired is returned when an org invite token is past expires_at.
var ErrInviteExpired = errors.New("invite expired")

// OrgInvite is a link invite into an organization. Email is recorded for the
// admin; delivery is not performed here.
type OrgInvite struct {
	ID         string     `json:"id"`
	OrgID      string     `json:"org_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	Token      string     `json:"token,omitempty"`
	InvitedBy  string     `json:"invited_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// MemberStorageAggregate is the D21 view of one member's private sessions:
// counts and a single last-active time, never titles or per-session times.
type MemberStorageAggregate struct {
	UserID       string     `json:"user_id"`
	SessionCount int        `json:"session_count"`
	LastActiveAt *time.Time `json:"last_active_at,omitempty"`
}

// ProjectStorageAggregate is project-level capture and byte usage.
type ProjectStorageAggregate struct {
	ProjectID      string `json:"project_id"`
	FolderName     string `json:"folder_name"`
	DisplayName    string `json:"display_name,omitempty"`
	CaptureEnabled bool   `json:"capture_enabled"`
	SessionCount   int    `json:"session_count"`
	StorageBytes   int64  `json:"storage_bytes"`
}

// OrgStorageReport is the org Storage tab. It has no session content.
type OrgStorageReport struct {
	Members  []*MemberStorageAggregate  `json:"members"`
	Projects []*ProjectStorageAggregate `json:"projects"`
}

// OrgAdminStore is capture, audit listing, storage aggregates, and invites.
type OrgAdminStore interface {
	CaptureEnabled(ctx context.Context, projectID string) (bool, error)
	SetCaptureEnabled(ctx context.Context, projectID string, enabled bool, updatedBy string) error
	ListOrgAudit(ctx context.Context, orgID, actorUserID string, limit int) ([]AuditEvent, error)
	OrgStorageAggregates(ctx context.Context, orgID string) (*OrgStorageReport, error)
	CreateOrgInvite(ctx context.Context, orgID, email, role, invitedBy string, expires time.Time) (*OrgInvite, error)
	ListOrgInvites(ctx context.Context, orgID string) ([]*OrgInvite, error)
	RevokeOrgInvite(ctx context.Context, orgID, inviteID string) error
	AcceptOrgInvite(ctx context.Context, token, userID string) (*OrganizationMember, error)
}

func inviteToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func normalizeInviteEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || strings.Contains(email, " ") || !strings.Contains(email, "@") {
		return "", errors.New("store: email is required")
	}
	return email, nil
}

func (m *MemStore) CaptureEnabled(ctx context.Context, projectID string) (bool, error) {
	_ = ctx
	projectID = strings.TrimSpace(projectID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.projects[projectID]; !ok {
		return false, fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	enabled, ok := m.projectCapture[projectID]
	if !ok {
		return true, nil
	}
	return enabled, nil
}

func (m *MemStore) SetCaptureEnabled(ctx context.Context, projectID string, enabled bool, updatedBy string) error {
	_ = ctx
	_ = updatedBy
	projectID = strings.TrimSpace(projectID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[projectID]; !ok {
		return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	if m.projectCapture == nil {
		m.projectCapture = map[string]bool{}
	}
	m.projectCapture[projectID] = enabled
	return nil
}

func (m *MemStore) ListOrgAudit(ctx context.Context, orgID, actorUserID string, limit int) ([]AuditEvent, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	actorUserID = strings.TrimSpace(actorUserID)
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var out []AuditEvent
	for i := len(m.auditEvents) - 1; i >= 0; i-- {
		ev := m.auditEvents[i]
		if ev.TenantID != orgID {
			continue
		}
		if actorUserID != "" && ev.ActorUserID != actorUserID {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	if out == nil {
		out = []AuditEvent{}
	}
	return out, nil
}

func (m *MemStore) OrgStorageAggregates(ctx context.Context, orgID string) (*OrgStorageReport, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	type acc struct {
		count int
		last  time.Time
	}
	byUser := map[string]*acc{}
	var projects []*ProjectStorageAggregate
	for _, p := range m.projects {
		if p == nil || p.OrgID != orgID {
			continue
		}
		sessCount := 0
		for _, s := range m.agentSessions {
			if s == nil || s.ProjectID != p.ID || s.OwnerUserID == "" {
				continue
			}
			sessCount++
			a := byUser[s.OwnerUserID]
			if a == nil {
				a = &acc{}
				byUser[s.OwnerUserID] = a
			}
			a.count++
			if s.LastActiveAt.After(a.last) {
				a.last = s.LastActiveAt
			}
		}
		var bytes int64
		prefix := p.ID + "\x00"
		for key, blob := range m.blobs {
			if strings.HasPrefix(key, prefix) {
				bytes += blob.Size
			}
		}
		enabled := true
		if v, ok := m.projectCapture[p.ID]; ok {
			enabled = v
		}
		projects = append(projects, &ProjectStorageAggregate{
			ProjectID: p.ID, FolderName: p.FolderName, DisplayName: p.DisplayName,
			CaptureEnabled: enabled, SessionCount: sessCount, StorageBytes: bytes,
		})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ProjectID < projects[j].ProjectID })
	members := make([]*MemberStorageAggregate, 0, len(byUser))
	for userID, a := range byUser {
		row := &MemberStorageAggregate{UserID: userID, SessionCount: a.count}
		if !a.last.IsZero() {
			t := a.last
			row.LastActiveAt = &t
		}
		members = append(members, row)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].UserID < members[j].UserID })
	if projects == nil {
		projects = []*ProjectStorageAggregate{}
	}
	return &OrgStorageReport{Members: members, Projects: projects}, nil
}

func (m *MemStore) CreateOrgInvite(ctx context.Context, orgID, email, role, invitedBy string, expires time.Time) (*OrgInvite, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	invitedBy = strings.TrimSpace(invitedBy)
	email, err := normalizeInviteEmail(email)
	if err != nil {
		return nil, err
	}
	norm, err := NormalizeOrgRole(role)
	if err != nil {
		return nil, err
	}
	if orgID == "" {
		return nil, errors.New("store: org id is required")
	}
	token, err := inviteToken()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	if m.orgInvites == nil {
		m.orgInvites = map[string]*OrgInvite{}
	}
	if m.inviteByToken == nil {
		m.inviteByToken = map[string]string{}
	}
	inv := &OrgInvite{
		ID: newID("inv"), OrgID: orgID, Email: email, Role: norm, Token: token,
		InvitedBy: invitedBy, CreatedAt: time.Now().UTC(), ExpiresAt: expires.UTC(),
	}
	m.orgInvites[inv.ID] = inv
	m.inviteByToken[token] = inv.ID
	copyInv := *inv
	return &copyInv, nil
}

func (m *MemStore) ListOrgInvites(ctx context.Context, orgID string) ([]*OrgInvite, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var out []*OrgInvite
	for _, inv := range m.orgInvites {
		if inv == nil || inv.OrgID != orgID {
			continue
		}
		copyInv := *inv
		copyInv.Token = ""
		out = append(out, &copyInv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if out == nil {
		out = []*OrgInvite{}
	}
	return out, nil
}

func (m *MemStore) RevokeOrgInvite(ctx context.Context, orgID, inviteID string) error {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	inviteID = strings.TrimSpace(inviteID)
	m.mu.Lock()
	defer m.mu.Unlock()
	inv := m.orgInvites[inviteID]
	if inv == nil || inv.OrgID != orgID {
		return fmt.Errorf("store: invite %s: %w", inviteID, ErrNotFound)
	}
	if inv.RevokedAt == nil {
		now := time.Now().UTC()
		inv.RevokedAt = &now
	}
	return nil
}

func (m *MemStore) AcceptOrgInvite(ctx context.Context, token, userID string) (*OrganizationMember, error) {
	token = strings.TrimSpace(token)
	userID = strings.TrimSpace(userID)
	if token == "" || userID == "" {
		return nil, errors.New("store: token and user id are required")
	}
	m.mu.Lock()
	id := m.inviteByToken[token]
	inv := m.orgInvites[id]
	if inv == nil || inv.RevokedAt != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("store: invite: %w", ErrNotFound)
	}
	if inv.AcceptedAt != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("store: invite already accepted: %w", ErrConflict)
	}
	if !inv.ExpiresAt.IsZero() && time.Now().After(inv.ExpiresAt) {
		m.mu.Unlock()
		return nil, ErrInviteExpired
	}
	orgID, role, invitedBy := inv.OrgID, inv.Role, inv.InvitedBy
	m.mu.Unlock()
	member, err := m.AddOrgMember(ctx, orgID, userID, role, invitedBy)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	m.mu.Lock()
	if cur := m.orgInvites[id]; cur != nil && cur.AcceptedAt == nil {
		cur.AcceptedAt = &now
	}
	m.mu.Unlock()
	return member, nil
}

func (s *PostgresStore) CaptureEnabled(ctx context.Context, projectID string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	if !looksLikeUUID(projectID) {
		return false, fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	var enabled bool
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(
		  (SELECT enabled FROM project_capture WHERE project_id = $1::uuid),
		  true
		)`, projectID).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("store: capture: %w", err)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1::uuid)`, projectID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	return enabled, nil
}

func (s *PostgresStore) SetCaptureEnabled(ctx context.Context, projectID string, enabled bool, updatedBy string) error {
	projectID = strings.TrimSpace(projectID)
	if !looksLikeUUID(projectID) {
		return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1::uuid)`, projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project_capture (project_id, enabled, updated_by, updated_at)
		VALUES ($1::uuid, $2, NULLIF($3, ''), now())
		ON CONFLICT (project_id) DO UPDATE
		  SET enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		projectID, enabled, updatedBy)
	if err != nil {
		return fmt.Errorf("store: set capture: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListOrgAudit(ctx context.Context, orgID, actorUserID string, limit int) ([]AuditEvent, error) {
	orgID = strings.TrimSpace(orgID)
	actorUserID = strings.TrimSpace(actorUserID)
	if limit <= 0 {
		limit = 50
	}
	if !looksLikeUUID(orgID) {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, at, COALESCE(tenant_id::text, ''), COALESCE(actor_user_id, ''),
		       actor_kind, action, COALESCE(resource_kind, ''), COALESCE(resource_id, ''),
		       COALESCE(project_id::text, ''), COALESCE(ip, ''), COALESCE(device_id, ''),
		       COALESCE(user_agent, ''), COALESCE(reason, ''), COALESCE(request_id, ''),
		       outcome, COALESCE(prev_hash, ''), metadata
		FROM audit_events
		WHERE tenant_id = $1::uuid
		  AND ($2 = '' OR actor_user_id = $2)
		ORDER BY at DESC, id DESC
		LIMIT $3`, orgID, actorUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list org audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var ev AuditEvent
		var meta []byte
		if err := rows.Scan(
			&ev.ID, &ev.At, &ev.TenantID, &ev.ActorUserID, &ev.ActorKind, &ev.Action,
			&ev.ResourceKind, &ev.ResourceID, &ev.ProjectID, &ev.IP, &ev.DeviceID,
			&ev.UserAgent, &ev.Reason, &ev.RequestID, &ev.Outcome, &ev.PrevHash, &meta,
		); err != nil {
			return nil, err
		}
		if len(meta) > 0 && string(meta) != "{}" && string(meta) != "null" {
			ev.Metadata = map[string]any{}
			_ = json.Unmarshal(meta, &ev.Metadata)
		}
		out = append(out, ev)
	}
	if out == nil {
		out = []AuditEvent{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) OrgStorageAggregates(ctx context.Context, orgID string) (*OrgStorageReport, error) {
	orgID = strings.TrimSpace(orgID)
	if !looksLikeUUID(orgID) {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	report := &OrgStorageReport{Members: []*MemberStorageAggregate{}, Projects: []*ProjectStorageAggregate{}}
	mrows, err := s.pool.Query(ctx, `
		SELECT s.owner_user_id::text, COUNT(*)::int, MAX(s.last_active_at)
		FROM agent_sessions s
		JOIN projects p ON p.id = s.project_id
		WHERE p.org_id = $1::uuid AND s.owner_user_id IS NOT NULL
		GROUP BY s.owner_user_id
		ORDER BY s.owner_user_id::text`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: member storage: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var row MemberStorageAggregate
		var last *time.Time
		if err := mrows.Scan(&row.UserID, &row.SessionCount, &last); err != nil {
			return nil, err
		}
		row.LastActiveAt = last
		report.Members = append(report.Members, &row)
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	prows, err := s.pool.Query(ctx, `
		SELECT p.id::text, p.folder_name, COALESCE(p.display_name, ''),
		       COALESCE(pc.enabled, true),
		       (SELECT COUNT(*)::int FROM agent_sessions s WHERE s.project_id = p.id),
		       (SELECT COALESCE(SUM(b.size), 0)::bigint FROM blobs b WHERE b.project_id = p.id)
		FROM projects p
		LEFT JOIN project_capture pc ON pc.project_id = p.id
		WHERE p.org_id = $1::uuid
		ORDER BY p.id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: project storage: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var row ProjectStorageAggregate
		if err := prows.Scan(&row.ProjectID, &row.FolderName, &row.DisplayName, &row.CaptureEnabled, &row.SessionCount, &row.StorageBytes); err != nil {
			return nil, err
		}
		report.Projects = append(report.Projects, &row)
	}
	return report, prows.Err()
}

func (s *PostgresStore) CreateOrgInvite(ctx context.Context, orgID, email, role, invitedBy string, expires time.Time) (*OrgInvite, error) {
	orgID = strings.TrimSpace(orgID)
	if !looksLikeUUID(orgID) {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	email, err := normalizeInviteEmail(email)
	if err != nil {
		return nil, err
	}
	norm, err := NormalizeOrgRole(role)
	if err != nil {
		return nil, err
	}
	token, err := inviteToken()
	if err != nil {
		return nil, err
	}
	var inv OrgInvite
	err = s.pool.QueryRow(ctx, `
		INSERT INTO org_invites (org_id, email, role, token, invited_by, expires_at)
		VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6)
		RETURNING id::text, org_id::text, email, role, token, COALESCE(invited_by, ''), created_at, expires_at, accepted_at, revoked_at`,
		orgID, email, norm, token, invitedBy, expires.UTC()).Scan(
		&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.Token, &inv.InvitedBy,
		&inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("store: create invite: %w", err)
	}
	return &inv, nil
}

func (s *PostgresStore) ListOrgInvites(ctx context.Context, orgID string) ([]*OrgInvite, error) {
	orgID = strings.TrimSpace(orgID)
	if !looksLikeUUID(orgID) {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, org_id::text, email, role, COALESCE(invited_by, ''), created_at, expires_at, accepted_at, revoked_at
		FROM org_invites WHERE org_id = $1::uuid ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: list invites: %w", err)
	}
	defer rows.Close()
	var out []*OrgInvite
	for rows.Next() {
		var inv OrgInvite
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &inv)
	}
	if out == nil {
		out = []*OrgInvite{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) RevokeOrgInvite(ctx context.Context, orgID, inviteID string) error {
	if !looksLikeUUID(orgID) || !looksLikeUUID(inviteID) {
		return fmt.Errorf("store: invite %s: %w", inviteID, ErrNotFound)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE org_invites SET revoked_at = COALESCE(revoked_at, now())
		WHERE id = $1::uuid AND org_id = $2::uuid`, inviteID, orgID)
	if err != nil {
		return fmt.Errorf("store: revoke invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: invite %s: %w", inviteID, ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) AcceptOrgInvite(ctx context.Context, token, userID string) (*OrganizationMember, error) {
	token = strings.TrimSpace(token)
	userID = strings.TrimSpace(userID)
	if token == "" || userID == "" {
		return nil, errors.New("store: token and user id are required")
	}
	if !looksLikeUUID(userID) {
		return nil, errors.New("store: user id must be a uuid")
	}
	var inv OrgInvite
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, org_id::text, email, role, COALESCE(invited_by, ''), expires_at, accepted_at, revoked_at
		FROM org_invites WHERE token = $1`, token).Scan(
		&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && inv.RevokedAt != nil) {
		return nil, fmt.Errorf("store: invite: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: invite: %w", err)
	}
	if inv.AcceptedAt != nil {
		return nil, fmt.Errorf("store: invite already accepted: %w", ErrConflict)
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	member, err := s.AddOrgMember(ctx, inv.OrgID, userID, inv.Role, inv.InvitedBy)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE org_invites SET accepted_at = now() WHERE id = $1::uuid AND accepted_at IS NULL`, inv.ID); err != nil {
		return nil, fmt.Errorf("store: accept invite: %w", err)
	}
	return member, nil
}
