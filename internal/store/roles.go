// roles.go — project RBAC: built-in roles, custom roles, HasPermission (issue #163).
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

// Permission flags (15 types) — issue #163 / implementation-plan-v2.md.
const (
	PermMemoryRead    = "memory:read"
	PermMemoryWrite   = "memory:write"
	PermMemoryConfirm = "memory:confirm"
	PermMemoryDelete  = "memory:delete"
	PermMemoryPromote = "memory:promote"
	PermMemoryEdit    = "memory:edit"
	PermBranchRead    = "branch:read"
	PermBranchWrite   = "branch:write"
	PermEpisodeRead   = "episode:read"
	PermEpisodeWrite  = "episode:write"
	PermSessionRead   = "session:read"
	PermSessionWrite  = "session:write"
	PermSessionSteer  = "session:steer"
	PermMemberInvite  = "member:invite"
	PermMemberManage  = "member:manage"
)

// AllPermissions is the canonical ordered list of the 15 permission flags.
var AllPermissions = []string{
	PermMemoryRead, PermMemoryWrite, PermMemoryConfirm, PermMemoryDelete,
	PermMemoryPromote, PermMemoryEdit,
	PermBranchRead, PermBranchWrite,
	PermEpisodeRead, PermEpisodeWrite,
	PermSessionRead, PermSessionWrite, PermSessionSteer,
	PermMemberInvite, PermMemberManage,
}

// Built-in project role names (uppercase, matching project_members.role).
const (
	RoleOwner  = "OWNER"
	RoleAdmin  = "ADMIN"
	RoleEditor = "EDITOR"
	RoleViewer = "VIEWER"
)

// ProjectRole is a built-in or custom role definition for a project.
type ProjectRole struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	IsBuiltin   bool      `json:"is_builtin"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// RoleStore is the RBAC surface (issue #163). MemStore and PostgresStore
// both implement it; the server type-asserts like SessionStore.
type RoleStore interface {
	ListProjectRoles(ctx context.Context, projectID string) ([]*ProjectRole, error)
	CreateProjectRole(ctx context.Context, role *ProjectRole) error
	UpdateProjectRole(ctx context.Context, role *ProjectRole) error
	DeleteProjectRole(ctx context.Context, projectID, roleID string) error
	GetProjectRole(ctx context.Context, projectID, roleID string) (*ProjectRole, error)

	GetMemberRole(ctx context.Context, userID, projectID string) (string, error)
	SetMemberRole(ctx context.Context, projectID, userID, role string) error
	HasPermission(ctx context.Context, userID, projectID, permission string) (bool, error)
}

var _ RoleStore = (*MemStore)(nil)
var _ RoleStore = (*PostgresStore)(nil)

// BuiltinRolePermissions maps built-in role → permission set.
func BuiltinRolePermissions(role string) ([]string, bool) {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case RoleOwner, RoleAdmin:
		out := make([]string, len(AllPermissions))
		copy(out, AllPermissions)
		return out, true
	case RoleEditor:
		return []string{
			PermMemoryRead, PermMemoryWrite, PermMemoryConfirm, PermMemoryDelete,
			PermMemoryPromote, PermMemoryEdit,
			PermBranchRead, PermBranchWrite,
			PermEpisodeRead, PermEpisodeWrite,
			PermSessionRead, PermSessionWrite, PermSessionSteer,
		}, true
	case RoleViewer:
		return []string{
			PermMemoryRead, PermBranchRead, PermEpisodeRead, PermSessionRead,
		}, true
	default:
		return nil, false
	}
}

// IsBuiltinRole reports whether name is one of the four built-in roles.
func IsBuiltinRole(name string) bool {
	_, ok := BuiltinRolePermissions(name)
	return ok
}

// ValidPermission reports whether p is one of the 15 known flags.
func ValidPermission(p string) bool {
	p = strings.TrimSpace(p)
	for _, known := range AllPermissions {
		if p == known {
			return true
		}
	}
	return false
}

// NormalizePermissions dedupes, validates, and sorts permission flags.
func NormalizePermissions(perms []string) ([]string, error) {
	seen := make(map[string]bool, len(perms))
	var out []string
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !ValidPermission(p) {
			return nil, fmt.Errorf("store: unknown permission %q", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// NormalizeProjectRoleName uppercases built-ins; custom names keep casing
// after trim. Rejects empty and reserved collisions handled by callers.
func NormalizeProjectRoleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("store: role name is required")
	}
	upper := strings.ToUpper(name)
	if IsBuiltinRole(upper) {
		return upper, nil
	}
	// Custom: letters, digits, space, underscore, hyphen; start with letter.
	if len(name) > 64 {
		return "", errors.New("store: role name too long")
	}
	for i, r := range name {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == ' ' || r == '_' || r == '-'
		if !ok || (i == 0 && !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))) {
			return "", fmt.Errorf("store: invalid role name %q", name)
		}
	}
	return name, nil
}

// BuiltinProjectRoles returns the four built-in role definitions for listing.
func BuiltinProjectRoles(projectID string) []*ProjectRole {
	names := []string{RoleOwner, RoleAdmin, RoleEditor, RoleViewer}
	out := make([]*ProjectRole, 0, len(names))
	for _, n := range names {
		perms, _ := BuiltinRolePermissions(n)
		out = append(out, &ProjectRole{
			ID:          "builtin:" + n,
			ProjectID:   projectID,
			Name:        n,
			Description: builtinDescription(n),
			Permissions: perms,
			IsBuiltin:   true,
		})
	}
	return out
}

func builtinDescription(role string) string {
	switch role {
	case RoleOwner:
		return "Everything. Cannot be removed."
	case RoleAdmin:
		return "Manage members, roles, settings. All read/write."
	case RoleEditor:
		return "Read/write memories, branches, episodes, sessions. No member management."
	case RoleViewer:
		return "Read-only everything. No writes."
	default:
		return ""
	}
}

func cloneRole(r *ProjectRole) *ProjectRole {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Permissions != nil {
		cp.Permissions = append([]string(nil), r.Permissions...)
	} else {
		cp.Permissions = []string{}
	}
	return &cp
}

func roleHasPerm(perms []string, permission string) bool {
	permission = strings.TrimSpace(permission)
	for _, p := range perms {
		if p == permission {
			return true
		}
	}
	return false
}

// ---- MemStore ----

func (s *MemStore) ensureRolesMaps() {
	if s.roles == nil {
		s.roles = make(map[string]map[string]*ProjectRole) // projectID -> roleID -> role
	}
	if s.memberRoles == nil {
		s.memberRoles = make(map[string]map[string]string) // projectID -> userID -> role name
	}
}

// ListProjectRoles returns built-ins plus custom roles for the project.
func (s *MemStore) ListProjectRoles(ctx context.Context, projectID string) ([]*ProjectRole, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("store: project id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.projects[projectID]; !ok {
		return nil, fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	out := BuiltinProjectRoles(projectID)
	for _, r := range s.roles[projectID] {
		out = append(out, cloneRole(r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsBuiltin != out[j].IsBuiltin {
			return out[i].IsBuiltin
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// CreateProjectRole inserts a custom role. Built-in names are rejected.
func (s *MemStore) CreateProjectRole(ctx context.Context, role *ProjectRole) error {
	if role == nil {
		return errors.New("store: role is required")
	}
	projectID := strings.TrimSpace(role.ProjectID)
	name, err := NormalizeProjectRoleName(role.Name)
	if err != nil {
		return err
	}
	if IsBuiltinRole(name) {
		return fmt.Errorf("store: cannot create built-in role %s: %w", name, ErrConflict)
	}
	perms, err := NormalizePermissions(role.Permissions)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRolesMaps()
	if _, ok := s.projects[projectID]; !ok {
		return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	for _, existing := range s.roles[projectID] {
		if strings.EqualFold(existing.Name, name) {
			return fmt.Errorf("store: role %q: %w", name, ErrConflict)
		}
	}
	now := time.Now().UTC()
	stored := &ProjectRole{
		ID:          newID("role"),
		ProjectID:   projectID,
		Name:        name,
		Description: strings.TrimSpace(role.Description),
		Permissions: perms,
		IsBuiltin:   false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if s.roles[projectID] == nil {
		s.roles[projectID] = make(map[string]*ProjectRole)
	}
	s.roles[projectID][stored.ID] = stored
	*role = *cloneRole(stored)
	return nil
}

// UpdateProjectRole updates a custom role's name/description/permissions.
func (s *MemStore) UpdateProjectRole(ctx context.Context, role *ProjectRole) error {
	if role == nil || strings.TrimSpace(role.ID) == "" {
		return errors.New("store: role id is required")
	}
	projectID := strings.TrimSpace(role.ProjectID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRolesMaps()
	existing, ok := s.roles[projectID][role.ID]
	if !ok || existing == nil {
		return fmt.Errorf("store: role %s: %w", role.ID, ErrNotFound)
	}
	if existing.IsBuiltin {
		return fmt.Errorf("store: cannot update built-in role: %w", ErrConflict)
	}
	name := existing.Name
	if strings.TrimSpace(role.Name) != "" {
		n, err := NormalizeProjectRoleName(role.Name)
		if err != nil {
			return err
		}
		if IsBuiltinRole(n) {
			return fmt.Errorf("store: cannot rename to built-in role: %w", ErrConflict)
		}
		for id, other := range s.roles[projectID] {
			if id != role.ID && strings.EqualFold(other.Name, n) {
				return fmt.Errorf("store: role %q: %w", n, ErrConflict)
			}
		}
		name = n
	}
	perms := existing.Permissions
	if role.Permissions != nil {
		p, err := NormalizePermissions(role.Permissions)
		if err != nil {
			return err
		}
		perms = p
	}
	desc := existing.Description
	if role.Description != "" || role.Name != "" || role.Permissions != nil {
		// Allow clearing description when explicitly provided as empty with other fields.
		desc = strings.TrimSpace(role.Description)
	}
	oldName := existing.Name
	existing.Name = name
	existing.Description = desc
	existing.Permissions = append([]string(nil), perms...)
	existing.UpdatedAt = time.Now().UTC()
	// Remap members that still hold the old custom role name.
	if oldName != name {
		for uid, rname := range s.memberRoles[projectID] {
			if rname == oldName {
				s.memberRoles[projectID][uid] = name
			}
		}
	}
	*role = *cloneRole(existing)
	return nil
}

// DeleteProjectRole removes a custom role. Members using it fall back to VIEWER.
func (s *MemStore) DeleteProjectRole(ctx context.Context, projectID, roleID string) error {
	projectID = strings.TrimSpace(projectID)
	roleID = strings.TrimSpace(roleID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRolesMaps()
	existing, ok := s.roles[projectID][roleID]
	if !ok || existing == nil {
		return fmt.Errorf("store: role %s: %w", roleID, ErrNotFound)
	}
	if existing.IsBuiltin {
		return fmt.Errorf("store: cannot delete built-in role: %w", ErrConflict)
	}
	name := existing.Name
	delete(s.roles[projectID], roleID)
	for uid, rname := range s.memberRoles[projectID] {
		if rname == name {
			s.memberRoles[projectID][uid] = RoleViewer
		}
	}
	return nil
}

// GetProjectRole fetches one custom role by id (builtins use id builtin:NAME).
func (s *MemStore) GetProjectRole(ctx context.Context, projectID, roleID string) (*ProjectRole, error) {
	projectID = strings.TrimSpace(projectID)
	roleID = strings.TrimSpace(roleID)
	if strings.HasPrefix(roleID, "builtin:") {
		name := strings.TrimPrefix(roleID, "builtin:")
		if !IsBuiltinRole(name) {
			return nil, fmt.Errorf("store: role %s: %w", roleID, ErrNotFound)
		}
		perms, _ := BuiltinRolePermissions(name)
		return &ProjectRole{
			ID: roleID, ProjectID: projectID, Name: strings.ToUpper(name),
			Description: builtinDescription(strings.ToUpper(name)),
			Permissions: perms, IsBuiltin: true,
		}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[projectID][roleID]
	if !ok || r == nil {
		return nil, fmt.Errorf("store: role %s: %w", roleID, ErrNotFound)
	}
	return cloneRole(r), nil
}

// GetMemberRole returns the member's role name. Creators are always OWNER.
func (s *MemStore) GetMemberRole(ctx context.Context, userID, projectID string) (string, error) {
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return "", nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.projects[projectID]; ok && p != nil && p.CreatedBy == userID {
		return RoleOwner, nil
	}
	if role, ok := s.memberRoles[projectID][userID]; ok && role != "" {
		return role, nil
	}
	// Legacy bool grant map (pre-RBAC tests / GrantMember path).
	if s.members[projectID][userID] {
		return RoleEditor, nil
	}
	return "", nil
}

// SetMemberRole assigns a built-in or custom role to a member.
func (s *MemStore) SetMemberRole(ctx context.Context, projectID, userID, role string) error {
	projectID = strings.TrimSpace(projectID)
	userID = strings.TrimSpace(userID)
	roleName, err := NormalizeProjectRoleName(role)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureRolesMaps()
	p, ok := s.projects[projectID]
	if !ok || p == nil {
		return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	if p.CreatedBy == userID {
		if roleName != RoleOwner {
			return fmt.Errorf("store: cannot change project creator role: %w", ErrConflict)
		}
		return nil
	}
	if roleName == RoleOwner {
		return fmt.Errorf("store: OWNER is reserved for the project creator: %w", ErrConflict)
	}
	if !IsBuiltinRole(roleName) {
		found := false
		for _, r := range s.roles[projectID] {
			if r != nil && r.Name == roleName {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("store: unknown role %q: %w", roleName, ErrNotFound)
		}
	}
	member := s.members[projectID][userID] || s.memberRoles[projectID][userID] != ""
	if !member {
		return fmt.Errorf("store: user %s is not a member: %w", userID, ErrNotFound)
	}
	if s.memberRoles[projectID] == nil {
		s.memberRoles[projectID] = make(map[string]string)
	}
	s.memberRoles[projectID][userID] = roleName
	if s.members[projectID] == nil {
		s.members[projectID] = make(map[string]bool)
	}
	s.members[projectID][userID] = true
	return nil
}

// HasPermission reports whether userID holds permission on projectID.
func (s *MemStore) HasPermission(ctx context.Context, userID, projectID, permission string) (bool, error) {
	permission = strings.TrimSpace(permission)
	if !ValidPermission(permission) {
		return false, fmt.Errorf("store: unknown permission %q", permission)
	}
	role, err := s.GetMemberRole(ctx, userID, projectID)
	if err != nil {
		return false, err
	}
	if role == "" {
		return false, nil
	}
	if perms, ok := BuiltinRolePermissions(role); ok {
		return roleHasPerm(perms, permission), nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.roles[projectID] {
		if r != nil && r.Name == role {
			return roleHasPerm(r.Permissions, permission), nil
		}
	}
	return false, nil
}

// ---- PostgresStore ----

const projectRoleColumns = `id::TEXT AS id, project_id::TEXT AS project_id, name,
	COALESCE(description, '') AS description, COALESCE(permissions, '{}') AS permissions,
	is_builtin, created_at, updated_at`

func scanProjectRole(row pgx.Row) (*ProjectRole, error) {
	var r ProjectRole
	if err := row.Scan(
		&r.ID, &r.ProjectID, &r.Name, &r.Description, &r.Permissions,
		&r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if r.Permissions == nil {
		r.Permissions = []string{}
	}
	return &r, nil
}

func (s *PostgresStore) ListProjectRoles(ctx context.Context, projectID string) ([]*ProjectRole, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("store: project id is required")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1::uuid)`, projectID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("store: list roles: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
	}
	out := BuiltinProjectRoles(projectID)
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectRoleColumns+` FROM project_roles
		  WHERE project_id = $1::uuid AND is_builtin = FALSE
		  ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanProjectRole(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list roles scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateProjectRole(ctx context.Context, role *ProjectRole) error {
	if role == nil {
		return errors.New("store: role is required")
	}
	projectID := strings.TrimSpace(role.ProjectID)
	name, err := NormalizeProjectRoleName(role.Name)
	if err != nil {
		return err
	}
	if IsBuiltinRole(name) {
		return fmt.Errorf("store: cannot create built-in role %s: %w", name, ErrConflict)
	}
	perms, err := NormalizePermissions(role.Permissions)
	if err != nil {
		return err
	}
	r, err := scanProjectRole(s.pool.QueryRow(ctx,
		`INSERT INTO project_roles (project_id, name, description, permissions, is_builtin)
		 VALUES ($1::uuid, $2, $3, $4, FALSE)
		 RETURNING `+projectRoleColumns,
		projectID, name, strings.TrimSpace(role.Description), perms))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("store: role %q: %w", name, ErrConflict)
		}
		return fmt.Errorf("store: create role: %w", err)
	}
	*role = *r
	return nil
}

func (s *PostgresStore) UpdateProjectRole(ctx context.Context, role *ProjectRole) error {
	if role == nil || strings.TrimSpace(role.ID) == "" {
		return errors.New("store: role id is required")
	}
	projectID := strings.TrimSpace(role.ProjectID)
	existing, err := s.GetProjectRole(ctx, projectID, role.ID)
	if err != nil {
		return err
	}
	if existing.IsBuiltin {
		return fmt.Errorf("store: cannot update built-in role: %w", ErrConflict)
	}
	name := existing.Name
	if strings.TrimSpace(role.Name) != "" {
		n, nerr := NormalizeProjectRoleName(role.Name)
		if nerr != nil {
			return nerr
		}
		if IsBuiltinRole(n) {
			return fmt.Errorf("store: cannot rename to built-in role: %w", ErrConflict)
		}
		name = n
	}
	perms := existing.Permissions
	if role.Permissions != nil {
		p, perr := NormalizePermissions(role.Permissions)
		if perr != nil {
			return perr
		}
		perms = p
	}
	desc := strings.TrimSpace(role.Description)
	if role.Description == "" && role.Name == "" && role.Permissions == nil {
		desc = existing.Description
	}
	oldName := existing.Name
	r, err := scanProjectRole(s.pool.QueryRow(ctx,
		`UPDATE project_roles
		    SET name = $3, description = $4, permissions = $5, updated_at = now()
		  WHERE id = $1::uuid AND project_id = $2::uuid AND is_builtin = FALSE
		  RETURNING `+projectRoleColumns,
		role.ID, projectID, name, desc, perms))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store: role %s: %w", role.ID, ErrNotFound)
		}
		if isUniqueViolation(err) {
			return fmt.Errorf("store: role %q: %w", name, ErrConflict)
		}
		return fmt.Errorf("store: update role: %w", err)
	}
	if oldName != name {
		_, _ = s.pool.Exec(ctx,
			`UPDATE project_members SET role = $3
			  WHERE project_id = $1::uuid AND role = $2`,
			projectID, oldName, name)
	}
	*role = *r
	return nil
}

func (s *PostgresStore) DeleteProjectRole(ctx context.Context, projectID, roleID string) error {
	projectID = strings.TrimSpace(projectID)
	roleID = strings.TrimSpace(roleID)
	var name string
	err := s.pool.QueryRow(ctx,
		`DELETE FROM project_roles
		  WHERE id = $1::uuid AND project_id = $2::uuid AND is_builtin = FALSE
		  RETURNING name`, roleID, projectID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Distinguish built-in / missing.
			var builtin bool
			if qerr := s.pool.QueryRow(ctx,
				`SELECT is_builtin FROM project_roles WHERE id = $1::uuid AND project_id = $2::uuid`,
				roleID, projectID).Scan(&builtin); qerr == nil && builtin {
				return fmt.Errorf("store: cannot delete built-in role: %w", ErrConflict)
			}
			return fmt.Errorf("store: role %s: %w", roleID, ErrNotFound)
		}
		return fmt.Errorf("store: delete role: %w", err)
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE project_members SET role = $3
		  WHERE project_id = $1::uuid AND role = $2`,
		projectID, name, RoleViewer)
	return nil
}

func (s *PostgresStore) GetProjectRole(ctx context.Context, projectID, roleID string) (*ProjectRole, error) {
	projectID = strings.TrimSpace(projectID)
	roleID = strings.TrimSpace(roleID)
	if strings.HasPrefix(roleID, "builtin:") {
		name := strings.ToUpper(strings.TrimPrefix(roleID, "builtin:"))
		if !IsBuiltinRole(name) {
			return nil, fmt.Errorf("store: role %s: %w", roleID, ErrNotFound)
		}
		perms, _ := BuiltinRolePermissions(name)
		return &ProjectRole{
			ID: "builtin:" + name, ProjectID: projectID, Name: name,
			Description: builtinDescription(name), Permissions: perms, IsBuiltin: true,
		}, nil
	}
	r, err := scanProjectRole(s.pool.QueryRow(ctx,
		`SELECT `+projectRoleColumns+` FROM project_roles
		  WHERE id = $1::uuid AND project_id = $2::uuid`, roleID, projectID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: role %s: %w", roleID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get role: %w", err)
	}
	return r, nil
}

func (s *PostgresStore) GetMemberRole(ctx context.Context, userID, projectID string) (string, error) {
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return "", nil
	}
	var role string
	err := s.pool.QueryRow(ctx, `
		SELECT CASE
			WHEN EXISTS(SELECT 1 FROM projects WHERE id = $2::uuid AND created_by = $1::uuid)
				THEN 'OWNER'
			ELSE COALESCE(
				(SELECT role FROM project_members WHERE project_id = $2::uuid AND user_id = $1::uuid),
				''
			)
		END`, userID, projectID).Scan(&role)
	if err != nil {
		return "", fmt.Errorf("store: get member role: %w", err)
	}
	if role == "MEMBER" {
		// Pre-migration rows if any remain.
		return RoleEditor, nil
	}
	return role, nil
}

func (s *PostgresStore) SetMemberRole(ctx context.Context, projectID, userID, role string) error {
	projectID = strings.TrimSpace(projectID)
	userID = strings.TrimSpace(userID)
	roleName, err := NormalizeProjectRoleName(role)
	if err != nil {
		return err
	}
	var creator string
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(created_by::TEXT, '') FROM projects WHERE id = $1::uuid`,
		projectID).Scan(&creator); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store: project %s: %w", projectID, ErrNotFound)
		}
		return fmt.Errorf("store: set member role: %w", err)
	}
	if creator == userID {
		if roleName != RoleOwner {
			return fmt.Errorf("store: cannot change project creator role: %w", ErrConflict)
		}
		return nil
	}
	if roleName == RoleOwner {
		return fmt.Errorf("store: OWNER is reserved for the project creator: %w", ErrConflict)
	}
	if !IsBuiltinRole(roleName) {
		var ok bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM project_roles
			   WHERE project_id = $1::uuid AND name = $2 AND is_builtin = FALSE)`,
			projectID, roleName).Scan(&ok); err != nil {
			return fmt.Errorf("store: set member role: %w", err)
		}
		if !ok {
			return fmt.Errorf("store: unknown role %q: %w", roleName, ErrNotFound)
		}
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE project_members SET role = $3
		  WHERE project_id = $1::uuid AND user_id = $2::uuid`,
		projectID, userID, roleName)
	if err != nil {
		return fmt.Errorf("store: set member role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: user %s is not a member: %w", userID, ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) HasPermission(ctx context.Context, userID, projectID, permission string) (bool, error) {
	permission = strings.TrimSpace(permission)
	if !ValidPermission(permission) {
		return false, fmt.Errorf("store: unknown permission %q", permission)
	}
	role, err := s.GetMemberRole(ctx, userID, projectID)
	if err != nil {
		return false, err
	}
	if role == "" {
		return false, nil
	}
	if perms, ok := BuiltinRolePermissions(role); ok {
		return roleHasPerm(perms, permission), nil
	}
	var perms []string
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(permissions, '{}') FROM project_roles
		  WHERE project_id = $1::uuid AND name = $2`,
		projectID, role).Scan(&perms)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: has permission: %w", err)
	}
	return roleHasPerm(perms, permission), nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}
