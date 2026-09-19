package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresHarvestQueue implements HarvestQueue on Postgres.
type PostgresHarvestQueue struct {
	pool *pgxpool.Pool
}

// NewPostgresHarvestQueue wraps the shared pgx pool.
func NewPostgresHarvestQueue(pool *pgxpool.Pool) *PostgresHarvestQueue {
	return &PostgresHarvestQueue{pool: pool}
}

// Pool exposes the underlying pool for wiring from PostgresStore.
func (s *PostgresStore) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

func (q *PostgresHarvestQueue) EnqueueHarvestJob(ctx context.Context, projectID, source string, turns []HarvestTurn) (*HarvestJob, bool, error) {
	if q == nil || q.pool == nil {
		return nil, false, fmt.Errorf("harvest: no database")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, false, fmt.Errorf("harvest: project_id required")
	}
	cleaned := make([]HarvestTurn, 0, len(turns))
	for _, t := range turns {
		c := strings.TrimSpace(t.Content)
		if c == "" {
			continue
		}
		c = strings.ToValidUTF8(c, "")
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if len(c) > 8000 {
			c = c[:8000]
		}
		cleaned = append(cleaned, HarvestTurn{
			Speaker:   strings.ToValidUTF8(strings.TrimSpace(t.Speaker), ""),
			Content:   c,
			Timestamp: strings.TrimSpace(t.Timestamp),
		})
	}
	if len(cleaned) == 0 {
		return nil, false, fmt.Errorf("harvest: no turns")
	}
	raw, err := MarshalHarvestTurns(cleaned)
	if err != nil {
		return nil, false, err
	}
	dedupe := HarvestDedupeKey(cleaned)
	preview := HarvestRawPreview(cleaned, 600)
	source = strings.TrimSpace(source)

	var id string
	var status string
	var createdAt, updatedAt time.Time
	err = q.pool.QueryRow(ctx, `
		INSERT INTO harvest_jobs (project_id, dedupe_key, status, source, turns, raw_preview, turn_count)
		VALUES ($1::uuid, $2, 'queued', $3, $4::jsonb, $5, $6)
		ON CONFLICT (project_id, dedupe_key) DO NOTHING
		RETURNING id::text, status, created_at, updated_at`,
		projectID, dedupe, source, string(raw), preview, len(cleaned),
	).Scan(&id, &status, &createdAt, &updatedAt)
	if err == nil {
		return &HarvestJob{
			ID:         id,
			ProjectID:  projectID,
			DedupeKey:  dedupe,
			Status:     status,
			Source:     source,
			Turns:      cleaned,
			RawPreview: preview,
			TurnCount:  len(cleaned),
			CreatedAt:  createdAt,
			UpdatedAt:  updatedAt,
		}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	job, gerr := q.lookupByDedupe(ctx, projectID, dedupe)
	if gerr != nil {
		return nil, false, gerr
	}
	return job, false, nil
}

func (q *PostgresHarvestQueue) lookupByDedupe(ctx context.Context, projectID, dedupe string) (*HarvestJob, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id::text, project_id::text, dedupe_key, status, COALESCE(source,''), turns,
		       raw_preview, turn_count, result_count, COALESCE(error,''), COALESCE(provider,''),
		       created_at, updated_at, started_at, finished_at
		FROM harvest_jobs WHERE project_id = $1::uuid AND dedupe_key = $2`, projectID, dedupe)
	return scanHarvestJob(row)
}

func (q *PostgresHarvestQueue) ClaimNextHarvestJob(ctx context.Context) (*HarvestJob, error) {
	if q == nil || q.pool == nil {
		return nil, nil
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM harvest_jobs
			WHERE status = 'queued'
			ORDER BY created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE harvest_jobs j
		SET status = 'processing', started_at = now(), updated_at = now()
		FROM next
		WHERE j.id = next.id
		RETURNING j.id::text, j.project_id::text, j.dedupe_key, j.status, COALESCE(j.source,''), j.turns,
		          j.raw_preview, j.turn_count, j.result_count, COALESCE(j.error,''), COALESCE(j.provider,''),
		          j.created_at, j.updated_at, j.started_at, j.finished_at`)
	job, err := scanHarvestJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

func (q *PostgresHarvestQueue) FinishHarvestJob(ctx context.Context, id, status, provider, errMsg string, resultCount int) error {
	if q == nil || q.pool == nil {
		return fmt.Errorf("harvest: no database")
	}
	switch status {
	case HarvestDone, HarvestFailed:
	default:
		return fmt.Errorf("harvest: invalid finish status %q", status)
	}
	_, err := q.pool.Exec(ctx, `
		UPDATE harvest_jobs
		SET status = $2, provider = $3, error = $4, result_count = $5,
		    finished_at = now(), updated_at = now()
		WHERE id = $1::uuid`,
		id, status, provider, errMsg, resultCount)
	return err
}

func (q *PostgresHarvestQueue) ListHarvestJobs(ctx context.Context, projectID string, limit int) ([]*HarvestJob, error) {
	if q == nil || q.pool == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	rows, err := q.pool.Query(ctx, `
		SELECT id::text, project_id::text, dedupe_key, status, COALESCE(source,''), turns,
		       raw_preview, turn_count, result_count, COALESCE(error,''), COALESCE(provider,''),
		       created_at, updated_at, started_at, finished_at
		FROM harvest_jobs
		WHERE project_id = $1::uuid
		ORDER BY created_at DESC
		LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*HarvestJob
	for rows.Next() {
		job, err := scanHarvestJob(rows)
		if err != nil {
			return nil, err
		}
		job.Turns = nil
		out = append(out, job)
	}
	return out, rows.Err()
}

func (q *PostgresHarvestQueue) GetHarvestJob(ctx context.Context, id string) (*HarvestJob, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id::text, project_id::text, dedupe_key, status, COALESCE(source,''), turns,
		       raw_preview, turn_count, result_count, COALESCE(error,''), COALESCE(provider,''),
		       created_at, updated_at, started_at, finished_at
		FROM harvest_jobs WHERE id = $1::uuid`, id)
	return scanHarvestJob(row)
}

func scanHarvestJob(row interface{ Scan(dest ...any) error }) (*HarvestJob, error) {
	var j HarvestJob
	var turnsRaw []byte
	var started, finished *time.Time
	err := row.Scan(
		&j.ID, &j.ProjectID, &j.DedupeKey, &j.Status, &j.Source, &turnsRaw,
		&j.RawPreview, &j.TurnCount, &j.ResultCount, &j.Error, &j.Provider,
		&j.CreatedAt, &j.UpdatedAt, &started, &finished,
	)
	if err != nil {
		return nil, err
	}
	j.StartedAt = started
	j.FinishedAt = finished
	if len(turnsRaw) > 0 {
		_ = json.Unmarshal(turnsRaw, &j.Turns)
	}
	return &j, nil
}
