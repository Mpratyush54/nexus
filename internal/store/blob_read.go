package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ReadBlob is intentionally project-scoped. HTTP callers must additionally
// prove that the hash belongs to a version visible to the requesting user.
func (m *MemStore) ReadBlob(_ context.Context, projectID, hash string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.blobs[blobKey(projectID, normalizeHash(hash))]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), rec.Body...), nil
}

func (s *PostgresStore) ReadBlob(ctx context.Context, projectID, hash string) ([]byte, error) {
	var body []byte
	err := s.pool.QueryRow(ctx,
		`SELECT body FROM blobs WHERE project_id = $1::uuid AND sha256 = $2 AND verified_at IS NOT NULL`,
		projectID, normalizeHash(hash)).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return body, err
}
