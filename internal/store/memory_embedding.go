package store

// memory_embedding.go — embedding provenance + re-embed seams.
// memory_items.embedding_model (migration 043) records
// which model produced each vector; the reembed backfill lists rows whose
// tag differs from the live model and overwrites them (batched).

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// embeddingModelFor is the embedding_model insert value: the item's tag, or
// "" (NULL) when the row carries no vector.
func embeddingModelFor(item *MemoryItem) string {
	if item == nil || len(item.Embedding) == 0 {
		return ""
	}
	return strings.TrimSpace(item.EmbeddingModel)
}

// LegacyEmbeddingModel is how a NULL embedding_model is treated by the
// model filter: rows written before migration 043 hold HashEmbed vectors.
const LegacyEmbeddingModel = "hash"

// VectorQuery is a query vector plus the model that produced it and the
// similarity floor that suits that model.
type VectorQuery struct {
	Vec []float32
	// Model restricts the search to rows embedded by the same model (NULL
	// rows count as LegacyEmbeddingModel). "" disables the filter (client-
	// supplied vectors of unknown origin, legacy callers).
	Model string
	// MinSimilarity is the cosine floor; values outside (0,1) mean
	// MinVectorSimilarity.
	MinSimilarity float64
}

func (q VectorQuery) floor() float64 {
	if q.MinSimilarity > 0 && q.MinSimilarity < 1 {
		return q.MinSimilarity
	}
	return MinVectorSimilarity
}

func (q VectorQuery) modelMatches(rowModel string) bool {
	want := strings.TrimSpace(q.Model)
	if want == "" {
		return true
	}
	got := strings.TrimSpace(rowModel)
	if got == "" {
		got = LegacyEmbeddingModel
	}
	return got == want
}

// ReembedCandidate is one row the backfill must (re-)embed.
type ReembedCandidate struct {
	ID      string
	Key     string
	Content string
}

// ---- PostgresStore ----

// ListMemoryForReembed returns live (PROPOSED/CONFIRMED) rows with no vector
// or a vector from a model other than model, in id order after afterID
// (keyset pagination; "" starts at the beginning). Superseded and rejected
// rows are skipped: they are never served by search.
func (s *PostgresStore) ListMemoryForReembed(ctx context.Context, model, afterID string, limit int) ([]ReembedCandidate, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, "key", content
		   FROM memory_items
		  WHERE status IN ('PROPOSED','CONFIRMED')
		    AND (embedding IS NULL OR embedding_model IS DISTINCT FROM $1)
		    AND id > COALESCE(NULLIF($2, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  ORDER BY id
		  LIMIT $3`, model, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReembedCandidate
	for rows.Next() {
		var c ReembedCandidate
		if err := rows.Scan(&c.ID, &c.Key, &c.Content); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountMemoryForReembed counts the rows ListMemoryForReembed would visit.
func (s *PostgresStore) CountMemoryForReembed(ctx context.Context, model string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM memory_items
		  WHERE status IN ('PROPOSED','CONFIRMED')
		    AND (embedding IS NULL OR embedding_model IS DISTINCT FROM $1)`, model).Scan(&n)
	return n, err
}

// SetMemoryEmbedding overwrites one row's vector and model tag. It leaves
// updated_at alone (a re-embed is not an edit; decay and sort must not move).
func (s *PostgresStore) SetMemoryEmbedding(ctx context.Context, id string, vec []float32, model string) error {
	if err := ValidateEmbeddingDim(vec); err != nil {
		return err
	}
	if len(vec) == 0 {
		return fmt.Errorf("store: SetMemoryEmbedding needs a vector")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET embedding = $2::vector, embedding_model = NULLIF($3,'')
		  WHERE id = $1::uuid`, id, encodeEmbedding(vec), strings.TrimSpace(model))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- MemStore ----

// ListMemoryForReembed mirrors the Postgres semantics (tests, local dev).
func (s *MemStore) ListMemoryForReembed(ctx context.Context, model, afterID string, limit int) ([]ReembedCandidate, error) {
	_ = ctx
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ReembedCandidate
	for _, m := range s.memories {
		if !memReembedDue(m, model) {
			continue
		}
		if afterID != "" && m.ID <= afterID {
			continue
		}
		out = append(out, ReembedCandidate{ID: m.ID, Key: m.Key, Content: m.Content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CountMemoryForReembed mirrors the Postgres semantics.
func (s *MemStore) CountMemoryForReembed(ctx context.Context, model string) (int, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, m := range s.memories {
		if memReembedDue(m, model) {
			n++
		}
	}
	return n, nil
}

func memReembedDue(m *MemoryItem, model string) bool {
	if m.Status != StatusProposed && m.Status != StatusConfirmed {
		return false
	}
	return len(m.Embedding) == 0 || m.EmbeddingModel != model
}

// SetMemoryEmbedding overwrites one row's vector and model tag.
func (s *MemStore) SetMemoryEmbedding(ctx context.Context, id string, vec []float32, model string) error {
	_ = ctx
	if err := ValidateEmbeddingDim(vec); err != nil {
		return err
	}
	if len(vec) == 0 {
		return fmt.Errorf("store: SetMemoryEmbedding needs a vector")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return ErrNotFound
	}
	m.Embedding = append([]float32(nil), vec...)
	m.EmbeddingModel = strings.TrimSpace(model)
	return nil
}
