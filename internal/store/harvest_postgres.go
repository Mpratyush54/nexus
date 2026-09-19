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

const harvestJobSelectCols = `
	id::text, project_id::text, dedupe_key, status, COALESCE(source,''), turns,
	raw_preview, turn_count, result_count, COALESCE(error,''), COALESCE(provider,''),
	COALESCE(attempt_count,0), next_attempt_at,
	created_at, updated_at, started_at, finished_at`

func (q *PostgresHarvestQueue) EnqueueHarvestJob(ctx context.Context, projectID, source string, turns []HarvestTurn) (*HarvestJob, bool, error) {
	if q == nil || q.pool == nil {
		return nil, false, fmt.Errorf("harvest: no database")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, false, fmt.Errorf("harvest: project_id required")
	}
	cleaned := CleanHarvestTurns(turns)
	if len(cleaned) == 0 {
		return nil, false, fmt.Errorf("harvest: no turns")
	}
	raw, err := MarshalHarvestTurns(cleaned)
	if err != nil {
		return nil, false, err
	}
	dedupe := HarvestDedupeKey(cleaned)
	preview := HarvestRawPreview(cleaned, 8000)
	source = SanitizeUTF8(strings.TrimSpace(source))
	if len(source) > 500 {
		source = TruncateUTF8(source, 500)
	}

	var id string
	var status string
	var createdAt, updatedAt time.Time
	err = q.pool.QueryRow(ctx, `
		INSERT INTO harvest_jobs (project_id, dedupe_key, status, source, turns, raw_preview, turn_count)
		VALUES ($1::uuid, $2, 'queued', $3, $4::jsonb, $5, $6)
		ON CONFLICT (project_id, dedupe_key) DO NOTHING
		RETURNING id::text, status, created_at, updated_at`,
		projectID, dedupe, source, raw, preview, len(cleaned),
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
		SELECT `+harvestJobSelectCols+`
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

	// Reclaim jobs stuck in processing (crashed worker / hung OpenRouter).
	_, _ = tx.Exec(ctx, `
		UPDATE harvest_jobs
		SET status = 'queued', started_at = NULL, updated_at = now(),
		    next_attempt_at = now(),
		    error = 'requeued: processing timed out'
		WHERE status = 'processing'
		  AND started_at < now() - interval '3 minutes'`)

	// Re-queue recent terminal failures that look transient (e.g. OpenRouter 429
	// before credits). Caps via attempt_count so we do not loop forever.
	_, _ = tx.Exec(ctx, `
		UPDATE harvest_jobs
		SET status = 'queued',
		    started_at = NULL,
		    finished_at = NULL,
		    next_attempt_at = now(),
		    attempt_count = GREATEST(attempt_count, 1),
		    updated_at = now()
		WHERE status = 'failed'
		  AND attempt_count < $1
		  AND finished_at > now() - interval '48 hours'
		  AND (
		    error ILIKE '%429%' OR error ILIKE '%rate limit%' OR error ILIKE '%throttle%'
		    OR error ILIKE '%timeout%' OR error ILIKE '%timed out%'
		    OR error ILIKE '%502%' OR error ILIKE '%503%' OR error ILIKE '%504%'
		    OR error ILIKE '%529%' OR error ILIKE '%overloaded%' OR error ILIKE '%unavailable%'
		  )`, HarvestMaxAttempts)

	row := tx.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM harvest_jobs
			WHERE status = 'queued'
			  AND (next_attempt_at IS NULL OR next_attempt_at <= now())
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
		          COALESCE(j.attempt_count,0), j.next_attempt_at,
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
		    finished_at = now(), updated_at = now(), next_attempt_at = NULL
		WHERE id = $1::uuid`,
		id, status, provider, errMsg, resultCount)
	return err
}

func (q *PostgresHarvestQueue) RequeueHarvestJob(ctx context.Context, id, provider, errMsg string, delay time.Duration) error {
	if q == nil || q.pool == nil {
		return fmt.Errorf("harvest: no database")
	}
	if delay < 0 {
		delay = 0
	}
	_, err := q.pool.Exec(ctx, `
		UPDATE harvest_jobs
		SET status = 'queued',
		    provider = $2,
		    error = $3,
		    attempt_count = attempt_count + 1,
		    next_attempt_at = now() + ($4 * interval '1 second'),
		    started_at = NULL,
		    finished_at = NULL,
		    updated_at = now()
		WHERE id = $1::uuid`,
		id, provider, errMsg, int(delay.Seconds()))
	return err
}

func (q *PostgresHarvestQueue) ListHarvestJobs(ctx context.Context, projectID string, limit int) ([]*HarvestJob, error) {
	return q.ListHarvestJobsOpt(ctx, projectID, limit, false)
}

// ListHarvestJobsOpt lists jobs; when includeTurns is true, full turn payloads are returned.
func (q *PostgresHarvestQueue) ListHarvestJobsOpt(ctx context.Context, projectID string, limit int, includeTurns bool) ([]*HarvestJob, error) {
	if q == nil || q.pool == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	rows, err := q.pool.Query(ctx, `
		SELECT `+harvestJobSelectCols+`
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
		if !includeTurns {
			job.Turns = nil
		} else if job.RawPreview == "" || len(job.RawPreview) < 200 {
			job.RawPreview = HarvestRawPreview(job.Turns, 8000)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (q *PostgresHarvestQueue) GetHarvestJob(ctx context.Context, id string) (*HarvestJob, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT `+harvestJobSelectCols+`
		FROM harvest_jobs WHERE id = $1::uuid`, id)
	return scanHarvestJob(row)
}

func scanHarvestJob(row interface{ Scan(dest ...any) error }) (*HarvestJob, error) {
	var j HarvestJob
	var turnsRaw []byte
	var started, finished, nextAttempt *time.Time
	err := row.Scan(
		&j.ID, &j.ProjectID, &j.DedupeKey, &j.Status, &j.Source, &turnsRaw,
		&j.RawPreview, &j.TurnCount, &j.ResultCount, &j.Error, &j.Provider,
		&j.AttemptCount, &nextAttempt,
		&j.CreatedAt, &j.UpdatedAt, &started, &finished,
	)
	if err != nil {
		return nil, err
	}
	j.NextAttemptAt = nextAttempt
	j.StartedAt = started
	j.FinishedAt = finished
	if len(turnsRaw) > 0 {
		_ = json.Unmarshal(turnsRaw, &j.Turns)
	}
	return &j, nil
}
