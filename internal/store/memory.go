package store

// memory.go — Postgres memory-item CRUD + hybrid search (Phase 1.5).
//
// SearchMemory is the keyword/tag fallback (works with zero embeddings);
// SearchMemoryVector is the primary semantic path once the Memory Processor
// backfills embedding vector(1536). Both honor project scoping: only rows
// with an explicit organization level and NULL project_id are visible to
// every project (issue #102).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const memoryColumns = `id, project_id, user_id, session_id, org_id,
	"key", content, context_snippet, level, scope, embedding::text,
	tags, confidence, status, source, source_event_id,
	proposed_by, confirmed_by, superseded_by, visibility,
	use_count, last_used_at, created_at, updated_at,
	COALESCE(category, 'general'), COALESCE(files_affected, '{}'),
	COALESCE(tools_used, '{}'), COALESCE(supersedes_key, ''),
	COALESCE(outcome, 'active'), COALESCE(week_bucket, '')`

func scanMemoryItem(row pgx.Row) (*MemoryItem, error) {
	var m MemoryItem
	var projectID, userID, sessionID, orgID *string
	var contextSnippet, source *string
	var embeddingText *string
	var sourceEventID *int64
	var proposedBy, confirmedBy, supersededBy *string
	var visibility *string
	var lastUsedAt *time.Time
	var category, supersedesKey, outcome, weekBucket *string
	var filesAffected, toolsUsed []string
	if err := row.Scan(&m.ID, &projectID, &userID, &sessionID, &orgID,
		&m.Key, &m.Content, &contextSnippet, &m.Level, &m.Scope, &embeddingText,
		&m.Tags, &m.Confidence, &m.Status, &source, &sourceEventID,
		&proposedBy, &confirmedBy, &supersededBy, &visibility,
		&m.UseCount, &lastUsedAt, &m.CreatedAt, &m.UpdatedAt,
		&category, &filesAffected, &toolsUsed, &supersedesKey,
		&outcome, &weekBucket); err != nil {
		return nil, err
	}
	if projectID != nil {
		m.ProjectID = *projectID
	}
	if userID != nil {
		m.UserID = *userID
	}
	if sessionID != nil {
		m.SessionID = *sessionID
	}
	if orgID != nil {
		m.OrgID = *orgID
	}
	if contextSnippet != nil {
		m.ContextSnippet = *contextSnippet
	}
	if source != nil {
		m.Source = *source
	}
	if sourceEventID != nil {
		m.SourceEventID = *sourceEventID
	}
	if proposedBy != nil {
		m.ProposedBy = *proposedBy
	}
	if confirmedBy != nil {
		m.ConfirmedBy = *confirmedBy
	}
	if supersededBy != nil {
		m.SupersededBy = *supersededBy
	}
	if visibility != nil && *visibility != "" {
		m.Visibility = *visibility
	} else {
		m.Visibility = VisibilityProject
	}
	if lastUsedAt != nil {
		m.LastUsedAt = *lastUsedAt
	}
	if category != nil {
		m.Category = *category
	}
	m.FilesAffected = filesAffected
	if m.FilesAffected == nil {
		m.FilesAffected = []string{}
	}
	m.ToolsUsed = toolsUsed
	if m.ToolsUsed == nil {
		m.ToolsUsed = []string{}
	}
	if supersedesKey != nil {
		m.SupersedesKey = *supersedesKey
	}
	if outcome != nil {
		m.Outcome = *outcome
	}
	if weekBucket != nil {
		m.WeekBucket = *weekBucket
	}
	m.Embedding = parseEmbedding(embeddingText)
	return &m, nil
}

// CreateMemoryItem inserts one memory, applying MemStore-identical defaults.
// CHECK-mirroring validation runs before SQL so both backends reject the
// same rows (issues #102, #119).
func (s *PostgresStore) CreateMemoryItem(ctx context.Context, item *MemoryItem) error {
	if err := validateMemoryItemForCreate(item); err != nil {
		return err
	}
	if item.Confidence == 0 {
		item.Confidence = 1.0
	}
	if item.Status == "" {
		item.Status = "PROPOSED"
	}
	if item.Level == "" {
		item.Level = "project"
	}
	if item.Scope == "" {
		item.Scope = "fact"
	}
	if len(item.Tags) == 0 {
		item.Tags = []string{}
	}
	if item.Visibility == "" {
		item.Visibility = VisibilityProject
	} else {
		item.Visibility = NormalizeVisibility(item.Visibility)
	}
	targetKey := strings.TrimSpace(item.SupersedesKey)
	if targetKey == "" && strings.TrimSpace(item.Key) != "" && item.ProjectID != "" {
		targetKey = strings.TrimSpace(item.Key)
	}

	var supersededIDs []string
	if item.ProjectID != "" && targetKey != "" {
		rows, err := s.pool.Query(ctx,
			`SELECT id FROM memory_items 
			 WHERE project_id = $1::uuid AND key = $2 
			   AND status IN ('CONFIRMED', 'PROPOSED') AND superseded_by IS NULL`,
			item.ProjectID, targetKey)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var oldID string
				if err := rows.Scan(&oldID); err == nil && oldID != "" && oldID != item.ID {
					supersededIDs = append(supersededIDs, oldID)
				}
			}
		}
	}

	cat := item.Category
	if cat == "" {
		cat = "general"
	}
	outc := item.Outcome
	if outc == "" {
		outc = "active"
	}
	files := item.FilesAffected
	if files == nil {
		files = []string{}
	}
	tools := item.ToolsUsed
	if tools == nil {
		tools = []string{}
	}
	week := item.WeekBucket
	if week == "" {
		y, w := time.Now().UTC().ISOWeek()
		week = fmt.Sprintf("%04d-W%02d", y, w)
	}

	row := s.pool.QueryRow(ctx,
		`INSERT INTO memory_items
			(project_id, user_id, session_id, org_id, "key", content,
			 context_snippet, level, scope, embedding, tags, confidence,
			 status, source, source_event_id, proposed_by, visibility,
			 category, files_affected, tools_used, supersedes_key, outcome, week_bucket)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6,
		         NULLIF($7,''), $8, $9, $10::vector, $11, $12,
		         $13, NULLIF($14,''), $15, $16::uuid, $17,
		         $18, $19, $20, NULLIF($21,''), $22, $23)
		 RETURNING id, created_at, updated_at`,
		nullText(item.ProjectID), nullText(item.UserID),
		nullText(item.SessionID), nullText(item.OrgID),
		item.Key, item.Content, item.ContextSnippet,
		item.Level, item.Scope, encodeEmbedding(item.Embedding),
		item.Tags, item.Confidence, item.Status, item.Source,
		nullEventID(item.SourceEventID), nullText(item.ProposedBy),
		item.Visibility,
		cat, files, tools, item.SupersedesKey, outc, week)
	if err := row.Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return err
	}

	for _, oldID := range supersededIDs {
		_, _ = s.pool.Exec(ctx,
			`INSERT INTO memory_versions (memory_id, version, key, content, tags, level, scope, created_at)
			 SELECT id, COALESCE((SELECT MAX(version) FROM memory_versions WHERE memory_id = $1::uuid), 0) + 1,
			        key, content, tags, level, scope, now()
			 FROM memory_items WHERE id = $1::uuid`, oldID)
		_, _ = s.pool.Exec(ctx,
			`UPDATE memory_items 
			 SET status = 'SUPERSEDED', superseded_by = $1::uuid, outcome = 'superseded', updated_at = now()
			 WHERE id = $2::uuid`, item.ID, oldID)
	}
	return nil
}

func nullEventID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// GetMemoryItem fetches one memory by id. When WithViewer is set on ctx,
// visibility rules apply and inaccessible rows surface as ErrNotFound.
func (s *PostgresStore) GetMemoryItem(ctx context.Context, id string) (*MemoryItem, error) {
	m, err := scanMemoryItem(s.pool.QueryRow(ctx,
		`SELECT `+memoryColumns+` FROM memory_items WHERE id = $1::uuid`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	viewerID := ViewerFrom(ctx)
	if viewerID == "" {
		return m, nil
	}
	isMember := false
	if m.ProjectID != "" {
		ok, merr := s.IsProjectMember(ctx, viewerID, m.ProjectID)
		if merr != nil {
			return nil, merr
		}
		isMember = ok
	}
	hasShare := false
	if NormalizeVisibility(m.Visibility) == VisibilityShared {
		hasShare, err = s.hasMemoryShare(ctx, m.ID, viewerID, m.ProjectID)
		if err != nil {
			return nil, err
		}
	}
	if !CanViewMemory(m, viewerID, isMember, hasShare) {
		return nil, ErrNotFound
	}
	return m, nil
}

// ConfirmMemory flips PROPOSED -> CONFIRMED and records who confirmed
// (issue #89 DAG). The write is conditional: re-confirming CONFIRMED stays
// idempotent, but terminal states (REJECTED, SUPERSEDED) are never
// resurrected — zero touched rows distinguish unknown ids (ErrNotFound)
// from illegal edges (ErrConflict).
func (s *PostgresStore) ConfirmMemory(ctx context.Context, id string, confirmedBy string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET status = 'CONFIRMED',
			confirmed_by = $2::uuid, updated_at = now()
		 WHERE id = $1::uuid AND status IN ('PROPOSED','CONFIRMED')`, id, nullText(confirmedBy))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.GetMemoryItem(ctx, id); gerr != nil {
			return gerr
		}
		return errMemoryConflict("non-PROPOSED", "CONFIRMED")
	}
	return nil
}

// SearchMemory is the text fallback: substring match on key/content, exact
// SearchMemory is the keyword/tag fallback (works with zero embeddings).
// Scope isolation (issue #102): NULL-project rows match only when
// explicitly organization-level. Sharing visibility (issue #164): WithViewer
// filters private/shared rows; empty viewer returns project+public only.
func (s *PostgresStore) SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error) {
	items, _, err := s.SearchMemoryPage(ctx, projectID, query, tags, limit, 0)
	return items, err
}

// SearchMemoryPage returns a page of keyword matches plus the total visible count.
func (s *PostgresStore) SearchMemoryPage(ctx context.Context, projectID string, query string, tags []string, limit, offset int) ([]*MemoryItem, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	viewerID := ViewerFrom(ctx)
	// Over-fetch before visibility filter, then page in Go. For large libraries
	// this is still bounded by limit+offset+buffer.
	fetch := (limit + offset) * 4
	if fetch < 80 {
		fetch = 80
	}
	if fetch > 4000 {
		fetch = 4000
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
		    AND status IN ('CONFIRMED','PROPOSED')
		    AND superseded_by IS NULL
		    AND ($2 = '' OR content ILIKE '%'||$2||'%'
		         OR "key" ILIKE '%'||$2||'%' OR $2 = ANY(tags))
		    AND ($3::text[] IS NULL OR tags && $3)
		  ORDER BY confidence DESC, created_at DESC
		  LIMIT $4`,
		nullText(projectID), query, nilTextArray(tags), fetch)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	isMember := false
	if viewerID != "" {
		ok, merr := s.IsProjectMember(ctx, viewerID, projectID)
		if merr != nil {
			return nil, 0, merr
		}
		isMember = ok
	}

	var visible []*MemoryItem
	for rows.Next() {
		m, err := scanMemoryItem(rows)
		if err != nil {
			return nil, 0, err
		}
		if viewerID == "" {
			vis := NormalizeVisibility(m.Visibility)
			if vis != VisibilityProject && vis != VisibilityPublic {
				continue
			}
		} else {
			hasShare := false
			if NormalizeVisibility(m.Visibility) == VisibilityShared {
				hasShare, err = s.hasMemoryShare(ctx, m.ID, viewerID, m.ProjectID)
				if err != nil {
					return nil, 0, err
				}
			}
			if !CanViewMemory(m, viewerID, isMember, hasShare) {
				continue
			}
		}
		visible = append(visible, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Exact total when the fetch window covered all candidates.
	total := len(visible)
	if len(visible) >= fetch {
		// May be more — count without LIMIT for a true total.
		var cnt int
		cerr := s.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM memory_items
			  WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
			    AND status IN ('CONFIRMED','PROPOSED')
			    AND ($2 = '' OR content ILIKE '%'||$2||'%'
			         OR "key" ILIKE '%'||$2||'%' OR $2 = ANY(tags))
			    AND ($3::text[] IS NULL OR tags && $3)`,
			nullText(projectID), query, nilTextArray(tags)).Scan(&cnt)
		if cerr == nil && cnt > total {
			total = cnt
		}
	}

	if offset >= len(visible) {
		return []*MemoryItem{}, total, nil
	}
	end := offset + limit
	if end > len(visible) {
		end = len(visible)
	}
	return visible[offset:end], total, nil
}

func nilTextArray(tags []string) any {
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// SearchMemoryVector is the primary semantic path (plan §1.5): cosine
// similarity over pgvector. Active rows are PROPOSED and CONFIRMED (spec
// 2.2: knowledge is searchable on write). Confidence floor is 0.3.
// Pinned rows rank ahead of similarity.
func (s *PostgresStore) SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*MemoryItem, error) {
	if len(queryVec) == 0 {
		return nil, fmt.Errorf("store: vector search needs a query embedding (use text search when there is none)")
	}
	if err := ValidateEmbeddingDim(queryVec); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
		    AND status IN ('CONFIRMED', 'PROPOSED')
		    AND superseded_by IS NULL
		    AND confidence > 0.3
		    AND embedding IS NOT NULL
		  ORDER BY (CASE WHEN 'pinned' = ANY(tags) THEN 0 ELSE 1 END), embedding <=> $2::vector
		  LIMIT $3`,
		nullText(projectID), encodeEmbedding(queryVec), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryItem
	for rows.Next() {
		m, err := scanMemoryItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
