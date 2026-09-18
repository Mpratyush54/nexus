// memory_edit.go — in-place memory updates + version history (issue #162).
//
// Phase 2 allows PROPOSED and CONFIRMED rows to be edited after creation.
// Every UpdateMemory / RevertMemory call snapshots the pre-change state into
// memory_versions, then applies the patch. SoftDeleteMemory sets SUPERSEDED
// (search hides it; history remains). Both MemStore and PostgresStore share
// the same MemoryPatch / MemoryVersion shapes so HTTP handlers stay store-
// agnostic via the memoryEditStore seam in the server package.
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MemoryVersion is one immutable snapshot of a memory item taken before an
// edit or revert (migration 016).
type MemoryVersion struct {
	ID        int64     `json:"id,omitempty"`
	MemoryID  string    `json:"memory_id"`
	Version   int       `json:"version"`
	Key       string    `json:"key"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Level     string    `json:"level,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	EditedBy  string    `json:"edited_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// MemoryPatch is a partial update: nil fields are left unchanged. Tags uses
// a pointer-to-slice so callers can clear tags with &[]string{}.
type MemoryPatch struct {
	Key            *string
	Content        *string
	Tags           *[]string
	ContextSnippet *string
	Level          *string
	Scope          *string
}

// Empty reports whether the patch carries any field.
func (p MemoryPatch) Empty() bool {
	return p.Key == nil && p.Content == nil && p.Tags == nil &&
		p.ContextSnippet == nil && p.Level == nil && p.Scope == nil
}

func memoryEditable(status string) bool {
	return status == StatusProposed || status == StatusConfirmed
}

func cloneMemoryVersion(v *MemoryVersion) *MemoryVersion {
	if v == nil {
		return nil
	}
	cp := *v
	cp.Tags = append([]string{}, v.Tags...)
	return &cp
}

func snapshotFromMemory(m *MemoryItem, version int, editedBy string) *MemoryVersion {
	tags := append([]string{}, m.Tags...)
	if tags == nil {
		tags = []string{}
	}
	return &MemoryVersion{
		MemoryID:  m.ID,
		Version:   version,
		Key:       m.Key,
		Content:   m.Content,
		Tags:      tags,
		Level:     m.Level,
		Scope:     m.Scope,
		EditedBy:  editedBy,
		CreatedAt: time.Now().UTC(),
	}
}

func applyMemoryPatch(m *MemoryItem, patch MemoryPatch) error {
	if patch.Key != nil {
		key := strings.TrimSpace(*patch.Key)
		if key == "" {
			return fmt.Errorf("store: memory key is required")
		}
		m.Key = key
	}
	if patch.Content != nil {
		content := strings.TrimSpace(*patch.Content)
		if err := ValidateMemoryContent(content); err != nil {
			return err
		}
		m.Content = content
	}
	if patch.Tags != nil {
		tags := append([]string{}, (*patch.Tags)...)
		if tags == nil {
			tags = []string{}
		}
		m.Tags = tags
	}
	if patch.ContextSnippet != nil {
		m.ContextSnippet = strings.TrimSpace(*patch.ContextSnippet)
	}
	if patch.Level != nil {
		level := strings.TrimSpace(*patch.Level)
		if err := ValidateMemoryLevel(level); err != nil {
			return err
		}
		if err := ValidateMemoryScopeRules(level, m.SessionID, m.UserID); err != nil {
			return err
		}
		m.Level = level
	}
	if patch.Scope != nil {
		scope := strings.TrimSpace(*patch.Scope)
		if err := ValidateMemoryScope(scope); err != nil {
			return err
		}
		m.Scope = scope
	}
	m.UpdatedAt = time.Now().UTC()
	return nil
}

// ---- MemStore ----

// UpdateMemory snapshots the current row, applies patch, and returns the
// updated item. Only PROPOSED/CONFIRMED rows are editable.
func (s *MemStore) UpdateMemory(ctx context.Context, id string, patch MemoryPatch, editedBy string) (*MemoryItem, error) {
	_ = ctx
	if patch.Empty() {
		return nil, fmt.Errorf("store: memory patch is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !memoryEditable(m.Status) {
		return nil, fmt.Errorf("store: memory status %s is not editable: %w", m.Status, ErrConflict)
	}
	next := len(s.versions[id]) + 1
	snap := snapshotFromMemory(m, next, editedBy)
	s.versions[id] = append(s.versions[id], snap)
	if err := applyMemoryPatch(m, patch); err != nil {
		// Roll back the just-appended snapshot on validation failure.
		s.versions[id] = s.versions[id][:len(s.versions[id])-1]
		return nil, err
	}
	return cloneMemoryItem(m), nil
}

// SoftDeleteMemory marks a PROPOSED/CONFIRMED row SUPERSEDED (soft delete).
func (s *MemStore) SoftDeleteMemory(ctx context.Context, id string) (*MemoryItem, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !memoryEditable(m.Status) {
		return nil, fmt.Errorf("store: memory status %s cannot be deleted: %w", m.Status, ErrConflict)
	}
	m.Status = StatusSuperseded
	m.UpdatedAt = time.Now().UTC()
	return cloneMemoryItem(m), nil
}

// ListMemoryVersions returns snapshots newest-first.
func (s *MemStore) ListMemoryVersions(ctx context.Context, id string) ([]*MemoryVersion, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.memories[id]; !ok {
		return nil, ErrNotFound
	}
	src := s.versions[id]
	out := make([]*MemoryVersion, 0, len(src))
	for i := len(src) - 1; i >= 0; i-- {
		out = append(out, cloneMemoryVersion(src[i]))
	}
	return out, nil
}

// RevertMemory restores key/content/tags/level/scope from version N after
// snapshotting the current state.
func (s *MemStore) RevertMemory(ctx context.Context, id string, version int, editedBy string) (*MemoryItem, error) {
	_ = ctx
	if version < 1 {
		return nil, fmt.Errorf("store: version must be >= 1")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !memoryEditable(m.Status) {
		return nil, fmt.Errorf("store: memory status %s is not editable: %w", m.Status, ErrConflict)
	}
	var target *MemoryVersion
	for _, v := range s.versions[id] {
		if v.Version == version {
			target = v
			break
		}
	}
	if target == nil {
		return nil, ErrNotFound
	}
	next := len(s.versions[id]) + 1
	s.versions[id] = append(s.versions[id], snapshotFromMemory(m, next, editedBy))
	m.Key = target.Key
	m.Content = target.Content
	m.Tags = append([]string{}, target.Tags...)
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if target.Level != "" {
		m.Level = target.Level
	}
	if target.Scope != "" {
		m.Scope = target.Scope
	}
	m.UpdatedAt = time.Now().UTC()
	return cloneMemoryItem(m), nil
}

// ---- PostgresStore ----

func (s *PostgresStore) nextMemoryVersion(ctx context.Context, tx pgx.Tx, memoryID string) (int, error) {
	var next int
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM memory_versions WHERE memory_id = $1::uuid`,
		memoryID).Scan(&next)
	return next, err
}

func (s *PostgresStore) insertMemoryVersion(ctx context.Context, tx pgx.Tx, v *MemoryVersion) error {
	tags := v.Tags
	if tags == nil {
		tags = []string{}
	}
	return tx.QueryRow(ctx,
		`INSERT INTO memory_versions
			(memory_id, version, key, content, tags, level, scope, edited_by)
		 VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), $8::uuid)
		 RETURNING id, created_at`,
		v.MemoryID, v.Version, v.Key, v.Content, tags, v.Level, v.Scope, nullText(v.EditedBy),
	).Scan(&v.ID, &v.CreatedAt)
}

// UpdateMemory snapshots then patches one editable memory row in a transaction.
func (s *PostgresStore) UpdateMemory(ctx context.Context, id string, patch MemoryPatch, editedBy string) (*MemoryItem, error) {
	if patch.Empty() {
		return nil, fmt.Errorf("store: memory patch is empty")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	m, err := scanMemoryItem(tx.QueryRow(ctx,
		`SELECT `+memoryColumns+` FROM memory_items WHERE id = $1::uuid FOR UPDATE`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !memoryEditable(m.Status) {
		return nil, fmt.Errorf("store: memory status %s is not editable: %w", m.Status, ErrConflict)
	}
	next, err := s.nextMemoryVersion(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	snap := snapshotFromMemory(m, next, editedBy)
	if err := s.insertMemoryVersion(ctx, tx, snap); err != nil {
		return nil, err
	}
	if err := applyMemoryPatch(m, patch); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE memory_items SET
			"key" = $2, content = $3, tags = $4,
			context_snippet = NULLIF($5,''), level = $6, scope = $7,
			updated_at = now()
		 WHERE id = $1::uuid AND status IN ('PROPOSED','CONFIRMED')`,
		id, m.Key, m.Content, m.Tags, m.ContextSnippet, m.Level, m.Scope)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("store: memory status changed concurrently: %w", ErrConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetMemoryItem(ctx, id)
}

// SoftDeleteMemory sets status SUPERSEDED for an editable memory row.
func (s *PostgresStore) SoftDeleteMemory(ctx context.Context, id string) (*MemoryItem, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET status = 'SUPERSEDED', updated_at = now()
		 WHERE id = $1::uuid AND status IN ('PROPOSED','CONFIRMED')`, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.GetMemoryItem(ctx, id); gerr != nil {
			return nil, gerr
		}
		return nil, fmt.Errorf("store: memory is not editable for delete: %w", ErrConflict)
	}
	return s.GetMemoryItem(ctx, id)
}

func scanMemoryVersion(row pgx.Row) (*MemoryVersion, error) {
	var v MemoryVersion
	var tags []string
	var level, scope, editedBy *string
	if err := row.Scan(&v.ID, &v.MemoryID, &v.Version, &v.Key, &v.Content,
		&tags, &level, &scope, &editedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	if tags == nil {
		tags = []string{}
	}
	v.Tags = tags
	if level != nil {
		v.Level = *level
	}
	if scope != nil {
		v.Scope = *scope
	}
	if editedBy != nil {
		v.EditedBy = *editedBy
	}
	return &v, nil
}

// ListMemoryVersions returns snapshots newest-first.
func (s *PostgresStore) ListMemoryVersions(ctx context.Context, id string) ([]*MemoryVersion, error) {
	if _, err := s.GetMemoryItem(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, memory_id::text, version, key, content,
		        COALESCE(tags, '{}'), level, scope, edited_by::text, created_at
		   FROM memory_versions
		  WHERE memory_id = $1::uuid
		  ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryVersion
	for rows.Next() {
		v, err := scanMemoryVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RevertMemory restores fields from a prior version after snapshotting current.
func (s *PostgresStore) RevertMemory(ctx context.Context, id string, version int, editedBy string) (*MemoryItem, error) {
	if version < 1 {
		return nil, fmt.Errorf("store: version must be >= 1")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	m, err := scanMemoryItem(tx.QueryRow(ctx,
		`SELECT `+memoryColumns+` FROM memory_items WHERE id = $1::uuid FOR UPDATE`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !memoryEditable(m.Status) {
		return nil, fmt.Errorf("store: memory status %s is not editable: %w", m.Status, ErrConflict)
	}
	target, err := scanMemoryVersion(tx.QueryRow(ctx,
		`SELECT id, memory_id::text, version, key, content,
		        COALESCE(tags, '{}'), level, scope, edited_by::text, created_at
		   FROM memory_versions
		  WHERE memory_id = $1::uuid AND version = $2`, id, version))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	next, err := s.nextMemoryVersion(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := s.insertMemoryVersion(ctx, tx, snapshotFromMemory(m, next, editedBy)); err != nil {
		return nil, err
	}
	level := target.Level
	if level == "" {
		level = m.Level
	}
	scope := target.Scope
	if scope == "" {
		scope = m.Scope
	}
	tags := target.Tags
	if tags == nil {
		tags = []string{}
	}
	tag, err := tx.Exec(ctx,
		`UPDATE memory_items SET
			"key" = $2, content = $3, tags = $4, level = $5, scope = $6,
			updated_at = now()
		 WHERE id = $1::uuid AND status IN ('PROPOSED','CONFIRMED')`,
		id, target.Key, target.Content, tags, level, scope)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("store: memory status changed concurrently: %w", ErrConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetMemoryItem(ctx, id)
}
