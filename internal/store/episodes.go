package store

// episodes.go — Postgres episode CRUD + retrieval (Phase 2.4).
//
// Mirrors MemStore semantics: error-pattern match, narrative substring
// match, or empty filters (list). Semantic vector search over the episode
// narrative reuses the same `<=>` pattern as SearchMemoryVector.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const episodeColumns = `id, project_id, session_id, title, episode_type,
	trigger, investigation, root_cause, resolution, verification,
	tags, embedding::text, files_involved, error_patterns,
	status, opened_at, resolved_at, created_by, resolved_by`

func scanEpisode(row pgx.Row) (*Episode, error) {
	var ep Episode
	var sessionID *string
	var trigger, investigation, rootCause, resolution, verification *string
	var embeddingText *string
	var resolvedAt *time.Time
	var createdBy, resolvedBy *string
	if err := row.Scan(&ep.ID, &ep.ProjectID, &sessionID, &ep.Title, &ep.EpisodeType,
		&trigger, &investigation, &rootCause, &resolution, &verification,
		&ep.Tags, &embeddingText, &ep.FilesInvolved, &ep.ErrorPatterns,
		&ep.Status, &ep.OpenedAt, &resolvedAt, &createdBy, &resolvedBy); err != nil {
		return nil, err
	}
	if sessionID != nil {
		ep.SessionID = *sessionID
	}
	if trigger != nil {
		ep.Trigger = *trigger
	}
	if investigation != nil {
		ep.Investigation = *investigation
	}
	if rootCause != nil {
		ep.RootCause = *rootCause
	}
	if resolution != nil {
		ep.Resolution = *resolution
	}
	if verification != nil {
		ep.Verification = *verification
	}
	if createdBy != nil {
		ep.CreatedBy = *createdBy
	}
	if resolvedBy != nil {
		ep.ResolvedBy = *resolvedBy
	}
	if resolvedAt != nil {
		ep.ResolvedAt = *resolvedAt
	}
	ep.Embedding = parseEmbedding(embeddingText)
	return &ep, nil
}

// CreateEpisode opens a new episode arc. The episode type is validated
// against the episode_type CHECK set (issue #119) and the status defaults
// to OPEN (a caller-supplied status must still be a known state).
func (s *PostgresStore) CreateEpisode(ctx context.Context, ep *Episode) error {
	if strings.TrimSpace(ep.Title) == "" {
		return fmt.Errorf("store: episode title is required")
	}
	if strings.TrimSpace(ep.EpisodeType) == "" {
		return fmt.Errorf("store: episode_type is required (want bug_fix|feature|refactor|incident|investigation|onboarding)")
	}
	if err := ValidateEpisodeType(ep.EpisodeType); err != nil {
		return err
	}
	// Normalize to the CHECK set's lowercase form (issue #131): validation
	// is case-insensitive but Postgres CHECK is not, and MemStore stores
	// lowercase — without this, "Bug_Fix" passes Go and dies in SQL.
	ep.EpisodeType = strings.ToLower(strings.TrimSpace(ep.EpisodeType))
	if err := ValidateEmbeddingDim(ep.Embedding); err != nil {
		return err
	}
	if strings.TrimSpace(ep.Status) == "" {
		ep.Status = "OPEN"
	} else {
		ep.Status = strings.ToUpper(strings.TrimSpace(ep.Status))
		if err := ValidateEpisodeStatus(ep.Status); err != nil {
			return err
		}
	}
	if len(ep.Tags) == 0 {
		ep.Tags = []string{}
	}
	if len(ep.FilesInvolved) == 0 {
		ep.FilesInvolved = []string{}
	}
	if len(ep.ErrorPatterns) == 0 {
		ep.ErrorPatterns = []string{}
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO episodes
			(project_id, session_id, title, episode_type, trigger,
			 investigation, root_cause, resolution, verification,
			 tags, embedding, files_involved, error_patterns,
			 status, created_by)
		 VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5,''),
		         NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''),
		         $10, $11::vector, $12, $13,
		         $14, $15::uuid)
		 RETURNING id, opened_at`,
		ep.ProjectID, nullText(ep.SessionID), ep.Title, ep.EpisodeType,
		ep.Trigger, ep.Investigation, ep.RootCause, ep.Resolution, ep.Verification,
		ep.Tags, encodeEmbedding(ep.Embedding), ep.FilesInvolved, ep.ErrorPatterns,
		ep.Status, nullText(ep.CreatedBy))
	return row.Scan(&ep.ID, &ep.OpenedAt)
}

// GetEpisode fetches one episode by id.
func (s *PostgresStore) GetEpisode(ctx context.Context, id string) (*Episode, error) {
	ep, err := scanEpisode(s.pool.QueryRow(ctx,
		`SELECT `+episodeColumns+` FROM episodes WHERE id = $1::uuid`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return ep, err
}

// ResolveEpisode closes the arc with resolution + verification notes
// (issue #103). The write is conditional on the arc still being open
// (OPEN or INVESTIGATING): RESOLVED/WONT_FIX are terminal and re-resolution
// fails instead of overwriting. Zero touched rows distinguish unknown ids
// (ErrNotFound) from terminal-state conflicts (ErrConflict).
func (s *PostgresStore) ResolveEpisode(ctx context.Context, id, resolution, verification, resolvedBy string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE episodes SET status = 'RESOLVED',
			resolution = NULLIF($2,''), verification = NULLIF($3,''),
			resolved_by = $4::uuid, resolved_at = now()
		 WHERE id = $1::uuid AND status IN ('OPEN','INVESTIGATING')`, id, resolution, verification, nullText(resolvedBy))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.GetEpisode(ctx, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("store: episode %s is not open (only OPEN/INVESTIGATING resolve): %w", id, ErrConflict)
	}
	return nil
}

// SearchEpisodes matches on error pattern, narrative text, or lists all
// when both filters are empty (MemStore parity). An empty projectID returns
// an empty list (avoids a raw uuid-syntax error from $1::uuid).
func (s *PostgresStore) SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*Episode, error) {
	if strings.TrimSpace(projectID) == "" {
		return []*Episode{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+episodeColumns+` FROM episodes
		  WHERE project_id = $1::uuid
		    AND ($2 = '' AND $3 = ''
		         OR $2 <> '' AND EXISTS (
		               SELECT 1 FROM unnest(error_patterns) p
		                WHERE p ILIKE '%'||$2||'%')
		         OR $3 <> '' AND (title ILIKE '%'||$3||'%'
		                          OR COALESCE(trigger,'') ILIKE '%'||$3||'%'
		                          OR COALESCE(investigation,'') ILIKE '%'||$3||'%'
		                          OR COALESCE(root_cause,'') ILIKE '%'||$3||'%'
		                          OR COALESCE(resolution,'') ILIKE '%'||$3||'%'))
		  ORDER BY opened_at DESC, id DESC
		  LIMIT $4`,
		projectID, errorPattern, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Episode
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}
