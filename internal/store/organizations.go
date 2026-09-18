// organizations.go — org CRUD + membership (issue #168 / Phase 8).
//
// Organizations sit above projects. Creators become ADMIN; org ADMINS get
// full access to every project with projects.org_id set (see IsProjectMember).
// Both MemStore and PostgresStore implement the same surface so HTTP handlers
// stay store-agnostic via the orgStore seam in the server package.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Organization role constants (migration 017).
const (
	OrgRoleAdmin  = "ADMIN"
	OrgRoleMember = "MEMBER"
)

// ValidOrgRole reports whether role is ADMIN or MEMBER.
func ValidOrgRole(role string) bool {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case OrgRoleAdmin, OrgRoleMember:
		return true
	default:
		return false
	}
}

// NormalizeOrgRole uppercases and validates; empty becomes MEMBER.
func NormalizeOrgRole(role string) (string, error) {
	r := strings.ToUpper(strings.TrimSpace(role))
	if r == "" {
		return OrgRoleMember, nil
	}
	if !ValidOrgRole(r) {
		return "", fmt.Errorf("store: invalid org role %q (want ADMIN|MEMBER)", role)
	}
	return r, nil
}

func cloneOrganization(o *Organization) *Organization {
	if o == nil {
		return nil
	}
	cp := *o
	return &cp
}

func cloneOrganizationMember(m *OrganizationMember) *OrganizationMember {
	if m == nil {
		return nil
	}
	cp := *m
	return &cp
}

// ---- MemStore ----

// CreateOrganization inserts an org and records creator as ADMIN.
func (s *MemStore) CreateOrganization(ctx context.Context, name, slug, createdBy string) (*Organization, error) {
	name = strings.TrimSpace(name)
	slug = strings.TrimSpace(slug)
	createdBy = strings.TrimSpace(createdBy)
	if name == "" {
		return nil, errors.New("store: organization name is required")
	}
	if createdBy == "" {
		return nil, errors.New("store: organization created_by is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug != "" {
		for _, o := range s.orgs {
			if o.Slug == slug {
				return nil, fmt.Errorf("store: organization slug %q: %w", slug, ErrConflict)
			}
		}
	}
	id := newID("org")
	now := time.Now().UTC()
	o := &Organization{
		ID:        id,
		Name:      name,
		Slug:      slug,
		CreatedBy: createdBy,
		CreatedAt: now,
	}
	s.orgs[id] = o
	if s.orgMembers[id] == nil {
		s.orgMembers[id] = make(map[string]string)
	}
	s.orgMembers[id][createdBy] = OrgRoleAdmin
	return cloneOrganization(o), nil
}

// ListOrganizations returns orgs where userID is a member, sorted by name then id.
func (s *MemStore) ListOrganizations(ctx context.Context, userID string) ([]*Organization, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("store: user id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Organization
	for orgID, members := range s.orgMembers {
		if _, ok := members[userID]; !ok {
			continue
		}
		if o := s.orgs[orgID]; o != nil {
			out = append(out, cloneOrganization(o))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	if out == nil {
		out = []*Organization{}
	}
	return out, nil
}

// GetOrganization fetches one org by id.
func (s *MemStore) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	id = strings.TrimSpace(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orgs[id]
	if !ok || o == nil {
		return nil, fmt.Errorf("store: organization %s: %w", id, ErrNotFound)
	}
	return cloneOrganization(o), nil
}

// IsOrgMember reports whether userID belongs to the org.
func (s *MemStore) IsOrgMember(ctx context.Context, userID, orgID string) (bool, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(orgID) == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.orgMembers[orgID][userID]
	return ok, nil
}

// GetOrgMemberRole returns the member's role or ErrNotFound.
func (s *MemStore) GetOrgMemberRole(ctx context.Context, orgID, userID string) (string, error) {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	if orgID == "" || userID == "" {
		return "", errors.New("store: org id and user id are required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	role, ok := s.orgMembers[orgID][userID]
	if !ok {
		return "", fmt.Errorf("store: org member %s/%s: %w", orgID, userID, ErrNotFound)
	}
	return role, nil
}

// ListOrgMembers returns members sorted by user_id.
func (s *MemStore) ListOrgMembers(ctx context.Context, orgID string) ([]*OrganizationMember, error) {
	orgID = strings.TrimSpace(orgID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var out []*OrganizationMember
	for uid, role := range s.orgMembers[orgID] {
		out = append(out, &OrganizationMember{
			OrgID:  orgID,
			UserID: uid,
			Role:   role,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	if out == nil {
		out = []*OrganizationMember{}
	}
	return out, nil
}

// ListOrgProjects returns projects owned by the org, sorted by created_at then id.
func (s *MemStore) ListOrgProjects(ctx context.Context, orgID string) ([]*Project, error) {
	orgID = strings.TrimSpace(orgID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var out []*Project
	for _, p := range s.projects {
		if p != nil && p.OrgID == orgID {
			out = append(out, cloneProject(p))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if out == nil {
		out = []*Project{}
	}
	return out, nil
}

// AddOrgMember grants membership. Idempotent upsert of role when already present.
func (s *MemStore) AddOrgMember(ctx context.Context, orgID, userID, role, grantedBy string) (*OrganizationMember, error) {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	grantedBy = strings.TrimSpace(grantedBy)
	norm, err := NormalizeOrgRole(role)
	if err != nil {
		return nil, err
	}
	if orgID == "" || userID == "" {
		return nil, errors.New("store: org id and user id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	if s.orgMembers[orgID] == nil {
		s.orgMembers[orgID] = make(map[string]string)
	}
	s.orgMembers[orgID][userID] = norm
	return &OrganizationMember{
		OrgID:     orgID,
		UserID:    userID,
		Role:      norm,
		GrantedBy: grantedBy,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// SetOrgMemberRole updates an existing member's role.
func (s *MemStore) SetOrgMemberRole(ctx context.Context, orgID, userID, role string) (*OrganizationMember, error) {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	norm, err := NormalizeOrgRole(role)
	if err != nil {
		return nil, err
	}
	if orgID == "" || userID == "" {
		return nil, errors.New("store: org id and user id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	if _, ok := s.orgMembers[orgID][userID]; !ok {
		return nil, fmt.Errorf("store: org member %s/%s: %w", orgID, userID, ErrNotFound)
	}
	if norm != OrgRoleAdmin {
		admins := 0
		for _, r := range s.orgMembers[orgID] {
			if r == OrgRoleAdmin {
				admins++
			}
		}
		if s.orgMembers[orgID][userID] == OrgRoleAdmin && admins <= 1 {
			return nil, fmt.Errorf("store: cannot demote last org admin: %w", ErrConflict)
		}
	}
	s.orgMembers[orgID][userID] = norm
	return &OrganizationMember{OrgID: orgID, UserID: userID, Role: norm}, nil
}

// RemoveOrgMember deletes a membership. The last ADMIN cannot be removed.
func (s *MemStore) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	if orgID == "" || userID == "" {
		return errors.New("store: org id and user id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	role, ok := s.orgMembers[orgID][userID]
	if !ok {
		return nil
	}
	if role == OrgRoleAdmin {
		admins := 0
		for _, r := range s.orgMembers[orgID] {
			if r == OrgRoleAdmin {
				admins++
			}
		}
		if admins <= 1 {
			return fmt.Errorf("store: cannot remove last org admin: %w", ErrConflict)
		}
	}
	delete(s.orgMembers[orgID], userID)
	return nil
}

// CreateOrgProject provisions a project under an organization and claims
// createdBy as creator. Org ADMINS already inherit access via IsProjectMember.
func (s *MemStore) CreateOrgProject(ctx context.Context, orgID, folderName, displayName, canonicalURL, rootCommit, createdBy string) (*Project, error) {
	orgID = strings.TrimSpace(orgID)
	folderName = strings.TrimSpace(folderName)
	displayName = strings.TrimSpace(displayName)
	createdBy = strings.TrimSpace(createdBy)
	if orgID == "" {
		return nil, errors.New("store: org id is required")
	}
	if folderName == "" {
		return nil, errors.New("store: folder_name is required")
	}
	if createdBy == "" {
		return nil, errors.New("store: created_by is required")
	}
	if displayName == "" {
		displayName = folderName
	}
	normURL := NormalizeGitURL(canonicalURL)
	storedURL := canonicalURL
	if normURL != "" {
		storedURL = normURL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	id := newID("proj")
	now := time.Now().UTC()
	p := &Project{
		ID:           id,
		CanonicalURL: storedURL,
		RootCommit:   rootCommit,
		FolderName:   folderName,
		DisplayName:  displayName,
		OrgID:        orgID,
		CreatedBy:    createdBy,
		CreatedAt:    now,
	}
	s.projects[id] = p
	return cloneProject(p), nil
}

// ---- PostgresStore ----

const orgColumns = `id::TEXT AS id, name, COALESCE(slug, '') AS slug,
	COALESCE(created_by::TEXT, '') AS created_by, created_at`

func scanOrganization(row pgx.Row) (*Organization, error) {
	var o Organization
	if err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedBy, &o.CreatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

const orgMemberColumns = `org_id::TEXT AS org_id, user_id::TEXT AS user_id, role,
	COALESCE(granted_by::TEXT, '') AS granted_by, created_at`

func scanOrganizationMember(row pgx.Row) (*OrganizationMember, error) {
	var m OrganizationMember
	if err := row.Scan(&m.OrgID, &m.UserID, &m.Role, &m.GrantedBy, &m.CreatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// CreateOrganization inserts an org and ADMIN membership for the creator.
func (s *PostgresStore) CreateOrganization(ctx context.Context, name, slug, createdBy string) (*Organization, error) {
	name = strings.TrimSpace(name)
	slug = strings.TrimSpace(slug)
	createdBy = strings.TrimSpace(createdBy)
	if name == "" {
		return nil, errors.New("store: organization name is required")
	}
	if createdBy == "" {
		return nil, errors.New("store: organization created_by is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin create org: %w", err)
	}
	defer tx.Rollback(ctx)

	o, err := scanOrganization(tx.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, created_by)
		 VALUES ($1, NULLIF($2,''), $3::uuid)
		 RETURNING `+orgColumns,
		name, slug, createdBy))
	if err != nil {
		return nil, fmt.Errorf("store: create organization: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization_members (org_id, user_id, role, granted_by)
		 VALUES ($1::uuid, $2::uuid, 'ADMIN', $2::uuid)`,
		o.ID, createdBy); err != nil {
		return nil, fmt.Errorf("store: create org admin membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: commit create org: %w", err)
	}
	return o, nil
}

// ListOrganizations returns orgs where userID is a member.
func (s *PostgresStore) ListOrganizations(ctx context.Context, userID string) ([]*Organization, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("store: user id is required")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgColumns+`
		   FROM organizations o
		   JOIN organization_members om ON om.org_id = o.id
		  WHERE om.user_id = $1::uuid
		  ORDER BY o.name ASC, o.id::TEXT ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list organizations: %w", err)
	}
	defer rows.Close()
	var out []*Organization
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list organizations scan: %w", err)
		}
		out = append(out, o)
	}
	if out == nil {
		out = []*Organization{}
	}
	return out, rows.Err()
}

// GetOrganization fetches one org by id.
func (s *PostgresStore) GetOrganization(ctx context.Context, id string) (*Organization, error) {
	id = strings.TrimSpace(id)
	o, err := scanOrganization(s.pool.QueryRow(ctx,
		`SELECT `+orgColumns+` FROM organizations WHERE id = $1::uuid`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: organization %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get organization: %w", err)
	}
	return o, nil
}

// IsOrgMember reports whether userID belongs to the org.
func (s *PostgresStore) IsOrgMember(ctx context.Context, userID, orgID string) (bool, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(orgID) == "" {
		return false, nil
	}
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(
		    SELECT 1 FROM organization_members
		     WHERE org_id = $1::uuid AND user_id = $2::uuid)`,
		orgID, userID).Scan(&ok); err != nil {
		return false, fmt.Errorf("store: org membership check: %w", err)
	}
	return ok, nil
}

// GetOrgMemberRole returns the member's role or ErrNotFound.
func (s *PostgresStore) GetOrgMemberRole(ctx context.Context, orgID, userID string) (string, error) {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	if orgID == "" || userID == "" {
		return "", errors.New("store: org id and user id are required")
	}
	var role string
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM organization_members
		  WHERE org_id = $1::uuid AND user_id = $2::uuid`,
		orgID, userID).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("store: org member %s/%s: %w", orgID, userID, ErrNotFound)
		}
		return "", fmt.Errorf("store: get org member role: %w", err)
	}
	return role, nil
}

// ListOrgMembers returns members sorted by user_id.
func (s *PostgresStore) ListOrgMembers(ctx context.Context, orgID string) ([]*OrganizationMember, error) {
	orgID = strings.TrimSpace(orgID)
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgMemberColumns+`
		   FROM organization_members
		  WHERE org_id = $1::uuid
		  ORDER BY user_id::TEXT ASC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: list org members: %w", err)
	}
	defer rows.Close()
	var out []*OrganizationMember
	for rows.Next() {
		m, err := scanOrganizationMember(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list org members scan: %w", err)
		}
		out = append(out, m)
	}
	if out == nil {
		out = []*OrganizationMember{}
	}
	return out, rows.Err()
}

// ListOrgProjects returns projects owned by the org.
func (s *PostgresStore) ListOrgProjects(ctx context.Context, orgID string) ([]*Project, error) {
	orgID = strings.TrimSpace(orgID)
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectColumns+`
		   FROM projects
		  WHERE org_id = $1::uuid
		  ORDER BY created_at ASC, id ASC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: list org projects: %w", err)
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list org projects scan: %w", err)
		}
		out = append(out, p)
	}
	if out == nil {
		out = []*Project{}
	}
	return out, rows.Err()
}

// AddOrgMember grants membership (upsert role).
func (s *PostgresStore) AddOrgMember(ctx context.Context, orgID, userID, role, grantedBy string) (*OrganizationMember, error) {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	grantedBy = strings.TrimSpace(grantedBy)
	norm, err := NormalizeOrgRole(role)
	if err != nil {
		return nil, err
	}
	if orgID == "" || userID == "" {
		return nil, errors.New("store: org id and user id are required")
	}
	m, err := scanOrganizationMember(s.pool.QueryRow(ctx,
		`INSERT INTO organization_members (org_id, user_id, role, granted_by)
		 VALUES ($1::uuid, $2::uuid, $3, $4::uuid)
		 ON CONFLICT (org_id, user_id) DO UPDATE
		   SET role = EXCLUDED.role,
		       granted_by = COALESCE(EXCLUDED.granted_by, organization_members.granted_by)
		 RETURNING `+orgMemberColumns,
		orgID, userID, norm, nullText(grantedBy)))
	if err != nil {
		return nil, fmt.Errorf("store: add org member: %w", err)
	}
	return m, nil
}

// SetOrgMemberRole updates an existing member's role.
func (s *PostgresStore) SetOrgMemberRole(ctx context.Context, orgID, userID, role string) (*OrganizationMember, error) {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	norm, err := NormalizeOrgRole(role)
	if err != nil {
		return nil, err
	}
	if orgID == "" || userID == "" {
		return nil, errors.New("store: org id and user id are required")
	}
	if norm != OrgRoleAdmin {
		var admins int
		if err := s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM organization_members
			  WHERE org_id = $1::uuid AND role = 'ADMIN'`, orgID).Scan(&admins); err != nil {
			return nil, fmt.Errorf("store: count org admins: %w", err)
		}
		var current string
		cerr := s.pool.QueryRow(ctx,
			`SELECT role FROM organization_members
			  WHERE org_id = $1::uuid AND user_id = $2::uuid`,
			orgID, userID).Scan(&current)
		if errors.Is(cerr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: org member %s/%s: %w", orgID, userID, ErrNotFound)
		}
		if cerr != nil {
			return nil, fmt.Errorf("store: get org member: %w", cerr)
		}
		if current == OrgRoleAdmin && admins <= 1 {
			return nil, fmt.Errorf("store: cannot demote last org admin: %w", ErrConflict)
		}
	}
	m, err := scanOrganizationMember(s.pool.QueryRow(ctx,
		`UPDATE organization_members SET role = $3
		  WHERE org_id = $1::uuid AND user_id = $2::uuid
		  RETURNING `+orgMemberColumns,
		orgID, userID, norm))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: org member %s/%s: %w", orgID, userID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: set org member role: %w", err)
	}
	return m, nil
}

// RemoveOrgMember deletes a membership. The last ADMIN cannot be removed.
func (s *PostgresStore) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	orgID = strings.TrimSpace(orgID)
	userID = strings.TrimSpace(userID)
	if orgID == "" || userID == "" {
		return errors.New("store: org id and user id are required")
	}
	var role string
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM organization_members
		  WHERE org_id = $1::uuid AND user_id = $2::uuid`,
		orgID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: get org member: %w", err)
	}
	if role == OrgRoleAdmin {
		var admins int
		if err := s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM organization_members
			  WHERE org_id = $1::uuid AND role = 'ADMIN'`, orgID).Scan(&admins); err != nil {
			return fmt.Errorf("store: count org admins: %w", err)
		}
		if admins <= 1 {
			return fmt.Errorf("store: cannot remove last org admin: %w", ErrConflict)
		}
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM organization_members
		  WHERE org_id = $1::uuid AND user_id = $2::uuid`,
		orgID, userID); err != nil {
		return fmt.Errorf("store: remove org member: %w", err)
	}
	return nil
}

// CreateOrgProject provisions a project under an organization.
func (s *PostgresStore) CreateOrgProject(ctx context.Context, orgID, folderName, displayName, canonicalURL, rootCommit, createdBy string) (*Project, error) {
	orgID = strings.TrimSpace(orgID)
	folderName = strings.TrimSpace(folderName)
	displayName = strings.TrimSpace(displayName)
	createdBy = strings.TrimSpace(createdBy)
	if orgID == "" {
		return nil, errors.New("store: org id is required")
	}
	if folderName == "" {
		return nil, errors.New("store: folder_name is required")
	}
	if createdBy == "" {
		return nil, errors.New("store: created_by is required")
	}
	if displayName == "" {
		displayName = folderName
	}
	normURL := NormalizeGitURL(canonicalURL)
	storedURL := canonicalURL
	if normURL != "" {
		storedURL = normURL
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("store: check organization: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	p, err := scanProject(s.pool.QueryRow(ctx,
		`INSERT INTO projects (canonical_url, root_commit, folder_name, display_name, org_id, created_by)
		 VALUES (NULLIF($1,''), NULLIF($2,''), $3, $4, $5::uuid, $6::uuid)
		 RETURNING `+projectColumns,
		storedURL, rootCommit, folderName, displayName, orgID, createdBy))
	if err != nil {
		return nil, fmt.Errorf("store: create org project: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, role, granted_by)
		 VALUES ($1::uuid, $2::uuid, 'OWNER', $2::uuid)
		 ON CONFLICT DO NOTHING`,
		p.ID, createdBy); err != nil {
		// project_members.role may still be OWNER|MEMBER (012) or expanded (013).
		// Fall back to default role insert if OWNER is rejected.
		if _, err2 := s.pool.Exec(ctx,
			`INSERT INTO project_members (project_id, user_id, granted_by)
			 VALUES ($1::uuid, $2::uuid, $2::uuid)
			 ON CONFLICT DO NOTHING`,
			p.ID, createdBy); err2 != nil {
			return nil, fmt.Errorf("store: grant org project creator: %w", err)
		}
	}
	return p, nil
}
