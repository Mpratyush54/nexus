package store

// workspaces.go — Postgres workspace registration + heartbeat (issue #2).
//
// Heartbeat contract (mirrors MemStore + plan §1.3): every heartbeat sets
// last_seen=now() and is_online=true; a workspace is "active" while
// last_seen is within OfflineThreshold (90s).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const workspaceColumns = `id, project_id, user_id, machine_id, path,
	branch, commit_sha, is_dirty, is_online, is_designated_processor,
	last_seen, daemon_url, created_at`

func scanWorkspace(row pgx.Row) (*Workspace, error) {
	var ws Workspace
	var projectID, userID string
	var branch, commitSHA, daemonURL *string
	var lastSeen *time.Time
	if err := row.Scan(&ws.ID, &projectID, &userID, &ws.MachineID, &ws.Path,
		&branch, &commitSHA, &ws.IsDirty, &ws.IsOnline, &ws.IsDesignatedProcessor,
		&lastSeen, &daemonURL, &ws.CreatedAt); err != nil {
		return nil, err
	}
	ws.ProjectID = projectID
	ws.UserID = userID
	if branch != nil {
		ws.Branch = *branch
	}
	if commitSHA != nil {
		ws.CommitSHA = *commitSHA
	}
	if daemonURL != nil {
		ws.DaemonURL = *daemonURL
	}
	if lastSeen != nil {
		ws.LastSeen = *lastSeen
	}
	return &ws, nil
}

// RegisterWorkspace upserts on (machine_id, path): daemons re-register on
// every startup, so a plain INSERT would collide with the UNIQUE constraint.
//
// Identity protection (issue #87): the conflict branch refreshes ONLY
// mutable liveness/git fields. project_id, user_id, and
// is_designated_processor are insert-only — a caller that knows a
// machine/path cannot rebind an existing workspace to another
// project/user, nor seize the designated-processor role.
func (s *PostgresStore) RegisterWorkspace(ctx context.Context, ws *Workspace) error {
	// Designation is server-managed (issue #149): the INSERT hardcodes
	// false — only the election path may designate. A client-supplied
	// true would otherwise forge processor ownership at insert.
	ws.IsDesignatedProcessor = false
	row := s.pool.QueryRow(ctx,
		`INSERT INTO workspaces
			(project_id, user_id, machine_id, path, branch, commit_sha,
			 is_dirty, is_online, is_designated_processor, last_seen, daemon_url)
		 VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5,''), NULLIF($6,''),
		         $7, true, false, now(), NULLIF($8,''))
		 ON CONFLICT (machine_id, path) DO UPDATE SET
			branch = EXCLUDED.branch,
			commit_sha = EXCLUDED.commit_sha,
			is_dirty = EXCLUDED.is_dirty,
			is_online = true,
			last_seen = now(),
			daemon_url = EXCLUDED.daemon_url
		 RETURNING id, last_seen, created_at`,
		ws.ProjectID, ws.UserID, ws.MachineID, ws.Path, ws.Branch, ws.CommitSHA,
		ws.IsDirty, ws.DaemonURL)
	if err := row.Scan(&ws.ID, &ws.LastSeen, &ws.CreatedAt); err != nil {
		return err
	}
	ws.IsOnline = true
	return nil
}

// Heartbeat refreshes liveness + git state. Unknown id -> ErrNotFound.
func (s *PostgresStore) Heartbeat(ctx context.Context, workspaceID string, branch, commitSHA string, isDirty bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspaces SET branch = NULLIF($2,''),
			commit_sha = NULLIF($3,''), is_dirty = $4,
			is_online = true, last_seen = now()
		 WHERE id = $1::uuid`,
		workspaceID, branch, commitSHA, isDirty)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetActiveWorkspace returns the most recently seen online workspace for a
// project, or ErrNotFound when none beat the OfflineThreshold.
func (s *PostgresStore) GetActiveWorkspace(ctx context.Context, projectID string) (*Workspace, error) {
	ws, err := scanWorkspace(s.pool.QueryRow(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces
		  WHERE project_id = $1::uuid AND is_online AND last_seen >= $2
		  ORDER BY last_seen DESC LIMIT 1`,
		projectID, time.Now().UTC().Add(-OfflineThreshold)))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return ws, err
}
