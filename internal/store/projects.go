package store

// projects.go — Postgres project CRUD + canonical resolver (issue #2).
//
// Resolution priority (mirrors MemStore.ResolveProject and the plan §1.2):
// canonical_url (normalized) → root_commit → folder_name → create.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const projectColumns = `id, canonical_url, root_commit, folder_name,
	display_name, org_id, created_by, created_at`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var canonicalURL, rootCommit, displayName, orgID, createdBy *string
	if err := row.Scan(&p.ID, &canonicalURL, &rootCommit, &p.FolderName,
		&displayName, &orgID, &createdBy, &p.CreatedAt); err != nil {
		return nil, err
	}
	if canonicalURL != nil {
		p.CanonicalURL = *canonicalURL
	}
	if rootCommit != nil {
		p.RootCommit = *rootCommit
	}
	if displayName != nil {
		p.DisplayName = *displayName
	}
	if orgID != nil {
		p.OrgID = *orgID
	}
	if createdBy != nil {
		p.CreatedBy = *createdBy
	}
	return &p, nil
}

// ResolveProject returns the canonical project for an identity triple,
// creating it when nothing matches. Stores the normalized URL so
// `git@host:x/y.git` and `https://host/x/y` converge on one row.
//
// Atomicity (issue #86): the lookup-then-insert sequence races when two
// daemons register the same unseen project concurrently. The INSERT uses
// ON CONFLICT DO NOTHING (any UNIQUE violation: canonical_url or
// root_commit) and reconciles by re-reading in priority order, so the loser
// returns the canonical row instead of a raw unique-violation.
func (s *PostgresStore) ResolveProject(ctx context.Context, canonicalURL, rootCommit, folderName string) (*Project, error) {
	normURL := NormalizeGitURL(canonicalURL)

	if p, err := s.lookupProject(ctx, normURL, rootCommit, folderName); err != nil {
		return nil, err
	} else if p != nil {
		return p, nil
	}

	storedURL := canonicalURL
	if normURL != "" {
		storedURL = normURL
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO projects (canonical_url, root_commit, folder_name, display_name)
		 VALUES (NULLIF($1,''), NULLIF($2,''), $3, $3)
		 ON CONFLICT DO NOTHING
		 RETURNING id, created_at`,
		storedURL, rootCommit, folderName)
	var id string
	var createdAt time.Time
	if err := row.Scan(&id, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Lost the insert race: another resolver won. Re-read in
			// priority order so identity priority still holds.
			if p, lerr := s.lookupProject(ctx, normURL, rootCommit, folderName); lerr != nil {
				return nil, lerr
			} else if p != nil {
				return p, nil
			}
			return nil, errors.New("store: project resolution lost insert race and row is missing")
		}
		return nil, err
	}
	return &Project{
		ID:           id,
		CanonicalURL: storedURL,
		RootCommit:   rootCommit,
		FolderName:   folderName,
		DisplayName:  folderName,
		CreatedAt:    createdAt,
	}, nil
}

// lookupProject reads in identity-priority order (URL -> root commit ->
// folder, first-registered wins). Returns (nil, nil) when nothing matches.
func (s *PostgresStore) lookupProject(ctx context.Context, normURL, rootCommit, folderName string) (*Project, error) {
	if normURL != "" {
		p, err := scanProject(s.pool.QueryRow(ctx,
			`SELECT `+projectColumns+` FROM projects WHERE canonical_url = $1`, normURL))
		if err == nil {
			return p, nil
		}
		if err != pgx.ErrNoRows {
			return nil, err
		}
	}
	if rootCommit != "" {
		p, err := scanProject(s.pool.QueryRow(ctx,
			`SELECT `+projectColumns+` FROM projects WHERE root_commit = $1`, rootCommit))
		if err == nil {
			return p, nil
		}
		if err != pgx.ErrNoRows {
			return nil, err
		}
	}
	// Folder fallback is ambiguous by nature; first-registered wins.
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE folder_name = $1 ORDER BY created_at ASC LIMIT 1`, folderName))
	if err == nil {
		return p, nil
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	return nil, nil
}

// GetProject fetches one project by id.
func (s *PostgresStore) GetProject(ctx context.Context, id string) (*Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1::uuid`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return p, err
}
