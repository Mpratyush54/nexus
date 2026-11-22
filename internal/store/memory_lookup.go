package store

import (
	"context"
	"strings"
)

// Lean memory reads (cold-latency fix).
//
// memoryColumns selects embedding::text: a vector(1536) is ~6 KB on disk,
// stored out of line (TOAST), and ~18 KB once rendered as text. Listing 800
// rows to show 20 (project_knowledge) read and parsed ~15 MB, and on a cold
// buffer cache each row's vector is a separate TOAST fetch; that was the
// 3.7 s first call. Callers that only show content mark their context with
// WithoutEmbeddings and get rows with a nil Embedding.

type noEmbeddingsKey struct{}

// WithoutEmbeddings marks a read whose rows do not need their embedding.
// Postgres then skips the vector column; MemStore ignores the flag.
func WithoutEmbeddings(ctx context.Context) context.Context {
	return context.WithValue(ctx, noEmbeddingsKey{}, true)
}

func embeddingsSkipped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(noEmbeddingsKey{}).(bool)
	return v
}

// memoryColumnsLean is memoryColumns without the vector (same scan shape).
var memoryColumnsLean = strings.Replace(memoryColumns, "embedding::text", "NULL::text", 1)

// memorySelectColumns picks the column list for ctx.
func memorySelectColumns(ctx context.Context) string {
	if embeddingsSkipped(ctx) {
		return memoryColumnsLean
	}
	return memoryColumns
}

// FindMemoryByKey returns the active (CONFIRMED or PROPOSED, not
// superseded) row with exactly this key in projectID that the context's
// viewer may see, or ErrNotFound. Visibility follows SearchMemory. It
// replaces substring searches for the key ("project/brief",
// "session/<id>"), which scanned every row's content and could miss the
// row behind higher-confidence substring matches.
func (s *PostgresStore) FindMemoryByKey(ctx context.Context, projectID, key string) (*MemoryItem, error) {
	key = strings.TrimSpace(key)
	if key == "" || !looksLikeUUID(projectID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memorySelectColumns(ctx)+` FROM memory_items
		  WHERE project_id = $1::uuid AND "key" = $2
		    AND status IN ('CONFIRMED','PROPOSED')
		    AND superseded_by IS NULL
		  ORDER BY (status = 'CONFIRMED') DESC, updated_at DESC
		  LIMIT 20`, projectID, key)
	if err != nil {
		return nil, err
	}
	var found []*MemoryItem
	for rows.Next() {
		m, err := scanMemoryItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrNotFound
	}
	viewerID := ViewerFrom(ctx)
	isMember := false
	if viewerID != "" {
		if isMember, err = s.IsProjectMember(ctx, viewerID, projectID); err != nil {
			return nil, err
		}
	}
	for _, m := range found {
		if viewerID == "" {
			vis := NormalizeVisibility(m.Visibility)
			if vis == VisibilityProject || vis == VisibilityPublic {
				return m, nil
			}
			continue
		}
		hasShare := false
		if NormalizeVisibility(m.Visibility) == VisibilityShared {
			if hasShare, err = s.hasMemoryShare(ctx, m.ID, viewerID, m.ProjectID); err != nil {
				return nil, err
			}
		}
		if CanViewMemory(m, viewerID, isMember, hasShare) {
			return m, nil
		}
	}
	return nil, ErrNotFound
}

// FindMemoryByKey: see PostgresStore.FindMemoryByKey.
func (s *MemStore) FindMemoryByKey(ctx context.Context, projectID, key string) (*MemoryItem, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrNotFound
	}
	items, err := s.SearchMemory(ctx, projectID, key, nil, 100000)
	if err != nil {
		return nil, err
	}
	var best *MemoryItem
	for _, it := range items {
		if it == nil || it.Key != key || it.ProjectID != projectID {
			continue
		}
		if best == nil || (it.Status == StatusConfirmed && best.Status != StatusConfirmed) ||
			(it.Status == best.Status && it.UpdatedAt.After(best.UpdatedAt)) {
			best = it
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}
