// memory_sharing.go — visibility modes + share grants + cross-project copy
// (issue #164 / migration 015_memory_sharing).
//
// Visibility (per memory_items.visibility):
//
//	private  — creator only
//	shared   — creator + memory_shares (user or role)
//	project  — all project members (default; pre-015 behavior)
//	public   — anyone, including unauthenticated viewers
//
// SearchMemory / GetMemoryItem honor these when a viewer is present on the
// context (WithViewer). Empty viewer: SearchMemory returns project+public
// only; GetMemoryItem stays a raw fetch for internal callers.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Visibility constants for memory_items.visibility (migration 015).
const (
	VisibilityPrivate = "private"
	VisibilityShared  = "shared"
	VisibilityProject = "project"
	VisibilityPublic  = "public"
)

// MemoryShare is one explicit grant on a SHARED memory.
type MemoryShare struct {
	ID               string    `json:"id"`
	MemoryID         string    `json:"memory_id"`
	SharedWithUserID string    `json:"shared_with_user_id,omitempty"`
	SharedWithRole   string    `json:"shared_with_role,omitempty"`
	SharedBy         string    `json:"shared_by,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// viewerCtxKey carries the authenticated subject for visibility checks.
type viewerCtxKey struct{}

// WithViewer attaches viewerID to ctx for SearchMemory / GetMemoryItem.
func WithViewer(ctx context.Context, viewerID string) context.Context {
	viewerID = strings.TrimSpace(viewerID)
	if viewerID == "" {
		return ctx
	}
	return context.WithValue(ctx, viewerCtxKey{}, viewerID)
}

// ViewerFrom returns the viewer ID attached by WithViewer, or "".
func ViewerFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(viewerCtxKey{}).(string)
	return strings.TrimSpace(v)
}

// NormalizeVisibility maps blank to project (default) and unknown values to
// private (fail-closed: never widen access on a typo).
func NormalizeVisibility(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", VisibilityProject:
		return VisibilityProject
	case VisibilityPrivate:
		return VisibilityPrivate
	case VisibilityShared:
		return VisibilityShared
	case VisibilityPublic:
		return VisibilityPublic
	default:
		return VisibilityPrivate
	}
}

// IsValidVisibility reports whether v is an accepted visibility mode.
func IsValidVisibility(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case VisibilityPrivate, VisibilityShared, VisibilityProject, VisibilityPublic:
		return true
	default:
		return false
	}
}

// MemoryCreatorID returns the attribution subject used for PRIVATE/SHARED
// ownership: proposed_by first, then user_id.
func MemoryCreatorID(m *MemoryItem) string {
	if m == nil {
		return ""
	}
	if id := strings.TrimSpace(m.ProposedBy); id != "" {
		return id
	}
	return strings.TrimSpace(m.UserID)
}

// CanViewMemory reports whether viewerID may see item given membership and
// an explicit share hit. PUBLIC always passes; PRIVATE is creator-only;
// SHARED is creator or share; PROJECT requires membership.
func CanViewMemory(item *MemoryItem, viewerID string, isMember, hasShare bool) bool {
	if item == nil {
		return false
	}
	viewerID = strings.TrimSpace(viewerID)
	creator := MemoryCreatorID(item)
	switch NormalizeVisibility(item.Visibility) {
	case VisibilityPublic:
		return true
	case VisibilityPrivate:
		return viewerID != "" && viewerID == creator
	case VisibilityShared:
		if viewerID != "" && viewerID == creator {
			return true
		}
		return hasShare
	case VisibilityProject:
		return isMember
	default:
		return viewerID != "" && viewerID == creator
	}
}

// ---- MemStore share bookkeeping ----

type memShare struct {
	ID               string
	MemoryID         string
	SharedWithUserID string
	SharedWithRole   string
	SharedBy         string
	CreatedAt        time.Time
}

func (s *MemStore) ensureShares() {
	if s.shares == nil {
		s.shares = make(map[string]*memShare)
	}
	if s.memberRoles == nil {
		s.memberRoles = make(map[string]map[string]string)
	}
}

func cloneMemoryShare(sh *MemoryShare) *MemoryShare {
	if sh == nil {
		return nil
	}
	cp := *sh
	return &cp
}

func (ms *memShare) toShare() *MemoryShare {
	return &MemoryShare{
		ID:               ms.ID,
		MemoryID:         ms.MemoryID,
		SharedWithUserID: ms.SharedWithUserID,
		SharedWithRole:   ms.SharedWithRole,
		SharedBy:         ms.SharedBy,
		CreatedAt:        ms.CreatedAt,
	}
}

// SetMemberRole records a project role for MemStore share-role matching
// (OWNER/MEMBER today; custom roles land with Phase 3).
func (s *MemStore) SetMemberRole(projectID, userID, role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureShares()
	if s.memberRoles[projectID] == nil {
		s.memberRoles[projectID] = make(map[string]string)
	}
	s.memberRoles[projectID][userID] = strings.ToUpper(strings.TrimSpace(role))
}

// MemberRole returns the recorded or inferred role for a project member.
func (s *MemStore) MemberRole(projectID, userID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.memberRoleLocked(projectID, userID)
}

func (s *MemStore) memberRoleLocked(projectID, userID string) string {
	if role := s.memberRoles[projectID][userID]; role != "" {
		return role
	}
	if p, ok := s.projects[projectID]; ok && p != nil && p.CreatedBy == userID {
		return "OWNER"
	}
	if s.members[projectID][userID] {
		return "MEMBER"
	}
	return ""
}

func (s *MemStore) hasShareLocked(memoryID, viewerID, projectID string) bool {
	if viewerID == "" {
		return false
	}
	role := s.memberRoleLocked(projectID, viewerID)
	for _, sh := range s.shares {
		if sh.MemoryID != memoryID {
			continue
		}
		if sh.SharedWithUserID != "" && sh.SharedWithUserID == viewerID {
			return true
		}
		if sh.SharedWithRole != "" && role != "" &&
			strings.EqualFold(sh.SharedWithRole, role) {
			return true
		}
	}
	return false
}

func (s *MemStore) memoryVisibleLocked(item *MemoryItem, viewerID string, isMember bool) bool {
	hasShare := false
	if NormalizeVisibility(item.Visibility) == VisibilityShared {
		hasShare = s.hasShareLocked(item.ID, viewerID, item.ProjectID)
	}
	return CanViewMemory(item, viewerID, isMember, hasShare)
}

// ShareMemory grants a user or role access to a memory and flips visibility
// to shared when it was private/project.
func (s *MemStore) ShareMemory(ctx context.Context, memoryID, userID, role, sharedBy string) (*MemoryShare, error) {
	memoryID = strings.TrimSpace(memoryID)
	userID = strings.TrimSpace(userID)
	role = strings.TrimSpace(role)
	sharedBy = strings.TrimSpace(sharedBy)
	if memoryID == "" {
		return nil, errors.New("store: memory id is required")
	}
	if (userID == "" && role == "") || (userID != "" && role != "") {
		return nil, errors.New("store: share requires exactly one of user_id or role")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureShares()
	item, ok := s.memories[memoryID]
	if !ok {
		return nil, fmt.Errorf("store: memory %s: %w", memoryID, ErrNotFound)
	}
	for _, sh := range s.shares {
		if sh.MemoryID != memoryID {
			continue
		}
		if userID != "" && sh.SharedWithUserID == userID {
			return sh.toShare(), nil
		}
		if role != "" && strings.EqualFold(sh.SharedWithRole, role) {
			return sh.toShare(), nil
		}
	}
	ms := &memShare{
		ID:               newID("share"),
		MemoryID:         memoryID,
		SharedWithUserID: userID,
		SharedWithRole:   role,
		SharedBy:         sharedBy,
		CreatedAt:        time.Now().UTC(),
	}
	s.shares[ms.ID] = ms
	vis := NormalizeVisibility(item.Visibility)
	if vis == VisibilityPrivate || vis == VisibilityProject {
		item.Visibility = VisibilityShared
		item.UpdatedAt = time.Now().UTC()
	}
	return ms.toShare(), nil
}

// UnshareMemory removes a user grant. Missing grants are ErrNotFound.
func (s *MemStore) UnshareMemory(ctx context.Context, memoryID, userID string) error {
	memoryID = strings.TrimSpace(memoryID)
	userID = strings.TrimSpace(userID)
	if memoryID == "" || userID == "" {
		return errors.New("store: memory id and user id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureShares()
	if _, ok := s.memories[memoryID]; !ok {
		return fmt.Errorf("store: memory %s: %w", memoryID, ErrNotFound)
	}
	for id, sh := range s.shares {
		if sh.MemoryID == memoryID && sh.SharedWithUserID == userID {
			delete(s.shares, id)
			return nil
		}
	}
	return fmt.Errorf("store: share %s/%s: %w", memoryID, userID, ErrNotFound)
}

// ListMemoryShares returns all grants for a memory.
func (s *MemStore) ListMemoryShares(ctx context.Context, memoryID string) ([]*MemoryShare, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return nil, errors.New("store: memory id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.memories[memoryID]; !ok {
		return nil, fmt.Errorf("store: memory %s: %w", memoryID, ErrNotFound)
	}
	var out []*MemoryShare
	for _, sh := range s.shares {
		if sh.MemoryID == memoryID {
			out = append(out, sh.toShare())
		}
	}
	if out == nil {
		out = []*MemoryShare{}
	}
	return out, nil
}

// SetMemoryVisibility updates visibility on a memory row.
func (s *MemStore) SetMemoryVisibility(ctx context.Context, memoryID, visibility string) error {
	memoryID = strings.TrimSpace(memoryID)
	visibility = strings.ToLower(strings.TrimSpace(visibility))
	if memoryID == "" {
		return errors.New("store: memory id is required")
	}
	if !IsValidVisibility(visibility) {
		return fmt.Errorf("store: invalid visibility %q", visibility)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.memories[memoryID]
	if !ok {
		return fmt.Errorf("store: memory %s: %w", memoryID, ErrNotFound)
	}
	item.Visibility = visibility
	item.UpdatedAt = time.Now().UTC()
	return nil
}

// CopyMemory duplicates a memory into targetProjectID (new id, PROPOSED,
// proposed_by = copiedBy). Caller must authorize memory:write on the target.
func (s *MemStore) CopyMemory(ctx context.Context, memoryID, targetProjectID, copiedBy string) (*MemoryItem, error) {
	memoryID = strings.TrimSpace(memoryID)
	targetProjectID = strings.TrimSpace(targetProjectID)
	copiedBy = strings.TrimSpace(copiedBy)
	if memoryID == "" || targetProjectID == "" {
		return nil, errors.New("store: memory id and target project_id are required")
	}
	s.mu.RLock()
	src, ok := s.memories[memoryID]
	if !ok {
		s.mu.RUnlock()
		return nil, fmt.Errorf("store: memory %s: %w", memoryID, ErrNotFound)
	}
	dup := cloneMemoryItem(src)
	s.mu.RUnlock()

	dup.ID = ""
	dup.ProjectID = targetProjectID
	dup.ProposedBy = copiedBy
	dup.ConfirmedBy = ""
	dup.SupersededBy = ""
	dup.Status = StatusProposed
	dup.UseCount = 0
	dup.LastUsedAt = time.Time{}
	dup.SessionID = ""
	// Copied rows start as project-scoped in the destination.
	dup.Visibility = VisibilityProject
	if err := s.CreateMemoryItem(ctx, dup); err != nil {
		return nil, err
	}
	return dup, nil
}

// ---- PostgresStore ----

const memoryShareColumns = `id::TEXT, memory_id::TEXT,
	COALESCE(shared_with_user_id::TEXT, ''), COALESCE(shared_with_role, ''),
	COALESCE(shared_by::TEXT, ''), created_at`

func scanMemoryShare(row pgx.Row) (*MemoryShare, error) {
	var sh MemoryShare
	if err := row.Scan(&sh.ID, &sh.MemoryID, &sh.SharedWithUserID, &sh.SharedWithRole,
		&sh.SharedBy, &sh.CreatedAt); err != nil {
		return nil, err
	}
	return &sh, nil
}

func (s *PostgresStore) ShareMemory(ctx context.Context, memoryID, userID, role, sharedBy string) (*MemoryShare, error) {
	memoryID = strings.TrimSpace(memoryID)
	userID = strings.TrimSpace(userID)
	role = strings.TrimSpace(role)
	sharedBy = strings.TrimSpace(sharedBy)
	if memoryID == "" {
		return nil, errors.New("store: memory id is required")
	}
	if (userID == "" && role == "") || (userID != "" && role != "") {
		return nil, errors.New("store: share requires exactly one of user_id or role")
	}
	if _, err := s.GetMemoryItem(ctx, memoryID); err != nil {
		return nil, err
	}
	sh, err := scanMemoryShare(s.pool.QueryRow(ctx,
		`INSERT INTO memory_shares (memory_id, shared_with_user_id, shared_with_role, shared_by)
		 VALUES ($1::uuid, $2::uuid, NULLIF($3,''), $4::uuid)
		 ON CONFLICT DO NOTHING
		 RETURNING `+memoryShareColumns,
		memoryID, nullText(userID), role, nullText(sharedBy)))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: share memory: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) || sh == nil {
		// Conflict: fetch existing grant.
		if userID != "" {
			sh, err = scanMemoryShare(s.pool.QueryRow(ctx,
				`SELECT `+memoryShareColumns+` FROM memory_shares
				  WHERE memory_id = $1::uuid AND shared_with_user_id = $2::uuid`,
				memoryID, userID))
		} else {
			sh, err = scanMemoryShare(s.pool.QueryRow(ctx,
				`SELECT `+memoryShareColumns+` FROM memory_shares
				  WHERE memory_id = $1::uuid AND shared_with_role = $2`,
				memoryID, role))
		}
		if err != nil {
			return nil, fmt.Errorf("store: share memory existing: %w", err)
		}
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE memory_items SET visibility = 'shared', updated_at = now()
		  WHERE id = $1::uuid AND visibility IN ('private','project')`, memoryID)
	return sh, nil
}

func (s *PostgresStore) UnshareMemory(ctx context.Context, memoryID, userID string) error {
	memoryID = strings.TrimSpace(memoryID)
	userID = strings.TrimSpace(userID)
	if memoryID == "" || userID == "" {
		return errors.New("store: memory id and user id are required")
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM memory_shares
		  WHERE memory_id = $1::uuid AND shared_with_user_id = $2::uuid`,
		memoryID, userID)
	if err != nil {
		return fmt.Errorf("store: unshare memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.GetMemoryItem(ctx, memoryID); gerr != nil {
			return gerr
		}
		return fmt.Errorf("store: share %s/%s: %w", memoryID, userID, ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) ListMemoryShares(ctx context.Context, memoryID string) ([]*MemoryShare, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return nil, errors.New("store: memory id is required")
	}
	if _, err := s.GetMemoryItem(ctx, memoryID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryShareColumns+` FROM memory_shares
		  WHERE memory_id = $1::uuid ORDER BY created_at ASC`, memoryID)
	if err != nil {
		return nil, fmt.Errorf("store: list memory shares: %w", err)
	}
	defer rows.Close()
	var out []*MemoryShare
	for rows.Next() {
		sh, err := scanMemoryShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	if out == nil {
		out = []*MemoryShare{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) SetMemoryVisibility(ctx context.Context, memoryID, visibility string) error {
	memoryID = strings.TrimSpace(memoryID)
	visibility = strings.ToLower(strings.TrimSpace(visibility))
	if memoryID == "" {
		return errors.New("store: memory id is required")
	}
	if !IsValidVisibility(visibility) {
		return fmt.Errorf("store: invalid visibility %q", visibility)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET visibility = $2, updated_at = now()
		  WHERE id = $1::uuid`, memoryID, visibility)
	if err != nil {
		return fmt.Errorf("store: set visibility: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: memory %s: %w", memoryID, ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) CopyMemory(ctx context.Context, memoryID, targetProjectID, copiedBy string) (*MemoryItem, error) {
	memoryID = strings.TrimSpace(memoryID)
	targetProjectID = strings.TrimSpace(targetProjectID)
	copiedBy = strings.TrimSpace(copiedBy)
	if memoryID == "" || targetProjectID == "" {
		return nil, errors.New("store: memory id and target project_id are required")
	}
	src, err := s.GetMemoryItem(ctx, memoryID)
	if err != nil {
		return nil, err
	}
	dup := cloneMemoryItem(src)
	dup.ID = ""
	dup.ProjectID = targetProjectID
	dup.ProposedBy = copiedBy
	dup.ConfirmedBy = ""
	dup.SupersededBy = ""
	dup.Status = StatusProposed
	dup.UseCount = 0
	dup.LastUsedAt = time.Time{}
	dup.SessionID = ""
	dup.Visibility = VisibilityProject
	if err := s.CreateMemoryItem(ctx, dup); err != nil {
		return nil, err
	}
	return dup, nil
}

func (s *PostgresStore) hasMemoryShare(ctx context.Context, memoryID, viewerID, projectID string) (bool, error) {
	if viewerID == "" {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM memory_shares
			 WHERE memory_id = $1::uuid AND shared_with_user_id = $2::uuid
		) OR EXISTS(
			SELECT 1 FROM memory_shares ms
			 WHERE ms.memory_id = $1::uuid AND ms.shared_with_role IS NOT NULL
			   AND (
			     EXISTS(SELECT 1 FROM projects p
			             WHERE p.id = $3::uuid AND p.created_by = $2::uuid
			               AND upper(ms.shared_with_role) = 'OWNER')
			     OR EXISTS(SELECT 1 FROM project_members pm
			                WHERE pm.project_id = $3::uuid AND pm.user_id = $2::uuid
			                  AND upper(pm.role) = upper(ms.shared_with_role))
			   )
		)`, memoryID, viewerID, nullText(projectID)).Scan(&ok)
	if err != nil {
		return false, err
	}
	return ok, nil
}
