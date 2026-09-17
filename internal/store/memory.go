package store

// memory.go — Postgres memory-item CRUD + hybrid search (Phase 1.5).
//
// SearchMemory is the keyword/tag fallback (works with zero embeddings);
// SearchMemoryVector is the primary semantic path once the Memory Processor
// backfills embedding vector(1536). Both honor project scoping: only rows
// with a matching project_id, or NULL-project rows explicitly marked
// level='organization' (the org tier is the global tier by design), are
// visible. NULL-project personal/session rows never leak across projects.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const memoryColumns = `id, project_id, user_id, session_id, org_id,
	"key", content, context_snippet, level, scope, embedding::text,
	tags, confidence, status, source, source_event_id,
	proposed_by, confirmed_by, superseded_by,
	use_count, last_used_at, created_at, updated_at`

func scanMemoryItem(row pgx.Row) (*MemoryItem, error) {
	var m MemoryItem
	var projectID, userID, sessionID, orgID *string
	var contextSnippet, source *string
	var embeddingText *string
	var sourceEventID *int64
	var proposedBy, confirmedBy, supersededBy *string
	var lastUsedAt *time.Time
	if err := row.Scan(&m.ID, &projectID, &userID, &sessionID, &orgID,
		&m.Key, &m.Content, &contextSnippet, &m.Level, &m.Scope, &embeddingText,
		&m.Tags, &m.Confidence, &m.Status, &source, &sourceEventID,
		&proposedBy, &confirmedBy, &supersededBy,
		&m.UseCount, &lastUsedAt, &m.CreatedAt, &m.UpdatedAt); err != nil {
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
	if lastUsedAt != nil {
		m.LastUsedAt = *lastUsedAt
	}
	m.Embedding = parseEmbedding(embeddingText)
	return &m, nil
}

// CreateMemoryItem inserts one memory, applying MemStore-identical defaults.
func (s *PostgresStore) CreateMemoryItem(ctx context.Context, item *MemoryItem) error {
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
	row := s.pool.QueryRow(ctx,
		`INSERT INTO memory_items
			(project_id, user_id, session_id, org_id, "key", content,
			 context_snippet, level, scope, embedding, tags, confidence,
			 status, source, source_event_id, proposed_by)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6,
		         NULLIF($7,''), $8, $9, $10::vector, $11, $12,
		         $13, NULLIF($14,''), $15, $16::uuid)
		 RETURNING id, created_at, updated_at`,
		nullText(item.ProjectID), nullText(item.UserID),
		nullText(item.SessionID), nullText(item.OrgID),
		item.Key, item.Content, item.ContextSnippet,
		item.Level, item.Scope, encodeEmbedding(item.Embedding),
		item.Tags, item.Confidence, item.Status, item.Source,
		nullEventID(item.SourceEventID), nullText(item.ProposedBy))
	return row.Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
}

func nullEventID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// GetMemoryItem fetches one memory by id.
func (s *PostgresStore) GetMemoryItem(ctx context.Context, id string) (*MemoryItem, error) {
	m, err := scanMemoryItem(s.pool.QueryRow(ctx,
		`SELECT `+memoryColumns+` FROM memory_items WHERE id = $1::uuid`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return m, err
}

// ConfirmMemory flips PROPOSED -> CONFIRMED and records who confirmed.
func (s *PostgresStore) ConfirmMemory(ctx context.Context, id string, confirmedBy string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET status = 'CONFIRMED',
			confirmed_by = $2::uuid, updated_at = now()
		 WHERE id = $1::uuid`, id, nullText(confirmedBy))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchMemory is the text fallback: substring match on key/content, exact
// tag hit, or empty query (list). MemStore parity: same CONFIRMED/PROPOSED
// visibility; NULL-project rows match only when level='organization'.
func (s *PostgresStore) SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
		    AND status IN ('CONFIRMED','PROPOSED')
		    AND ($2 = '' OR content ILIKE '%'||$2||'%'
		         OR "key" ILIKE '%'||$2||'%' OR $2 = ANY(tags))
		    AND ($3::text[] IS NULL OR tags && $3)
		  ORDER BY confidence DESC, created_at DESC
		  LIMIT $4`,
		nullText(projectID), query, nilTextArray(tags), limit)
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

func nilTextArray(tags []string) any {
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// SearchMemoryVector is the primary semantic path (plan §1.5): cosine
// similarity over pgvector, CONFIRMED only, confidence floor 0.3.
func (s *PostgresStore) SearchMemoryVector(ctx context.Context, projectID string, queryVec []float32, limit int) ([]*MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
		    AND status = 'CONFIRMED'
		    AND confidence > 0.3
		    AND embedding IS NOT NULL
		  ORDER BY embedding <=> $2::vector
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
