package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// FileOperation mirrors session_file_operations.
type FileOperation struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	SessionID   string    `json:"session_id"`
	TurnIndex   int       `json:"turn_index,omitempty"`
	Harness     string    `json:"harness"`
	ToolName    string    `json:"tool_name"`
	FilePath    string    `json:"file_path"`
	OpType      string    `json:"op_type"`
	LineStart   int       `json:"line_start,omitempty"`
	LineEnd     int       `json:"line_end,omitempty"`
	DiffHunk    string    `json:"diff_hunk,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ToolExecution mirrors session_tool_executions.
type ToolExecution struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"project_id"`
	SessionID        string    `json:"session_id"`
	Harness          string    `json:"harness"`
	ToolName         string    `json:"tool_name"`
	CommandLine      string    `json:"command_line"`
	WorkingDirectory string    `json:"working_directory,omitempty"`
	ExitCode         *int      `json:"exit_code,omitempty"`
	OutputSnippet    string    `json:"output_snippet,omitempty"`
	Truncated        bool      `json:"truncated"`
	CreatedAt        time.Time `json:"created_at"`
}

// SessionSnapshot mirrors session_snapshots (gzip bytes for diff/transcript).
type SessionSnapshot struct {
	ID                string    `json:"id"`
	SessionID         string    `json:"session_id"`
	SnapshotVersion   int       `json:"snapshot_version"`
	ProjectID         string    `json:"project_id"`
	Harness           string    `json:"harness"`
	ConversationID    string    `json:"conversation_id"`
	TurnCount         int       `json:"turn_count"`
	GitBranch         string    `json:"git_branch,omitempty"`
	GitCommit         string    `json:"git_commit,omitempty"`
	GitDirty          bool      `json:"git_dirty"`
	UncommittedDiff   []byte    `json:"uncommitted_diff,omitempty"`
	DiffSizeBytes     int       `json:"diff_size_bytes,omitempty"`
	DiffTruncated     bool      `json:"diff_truncated"`
	TranscriptPayload []byte    `json:"transcript_payload,omitempty"`
	ArtifactsBundle   []byte    `json:"artifacts_bundle,omitempty"`
	SourceMachineID   string    `json:"source_machine_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// FileOpListOpts filters ListFileOperations.
type FileOpListOpts struct {
	OpType   string
	FilePath string
	Limit    int
}

// ProvenanceStore is the CRUD surface for migration 028 tables.
type ProvenanceStore interface {
	InsertFileOperation(ctx context.Context, op FileOperation) error
	InsertToolExecution(ctx context.Context, exec ToolExecution) error
	InsertBatchFileOperations(ctx context.Context, ops []FileOperation) error
	ListFileOperations(ctx context.Context, sessionID string, opts FileOpListOpts) ([]FileOperation, error)
	ListToolExecutions(ctx context.Context, sessionID string) ([]ToolExecution, error)
	UpsertSessionSnapshot(ctx context.Context, snap *SessionSnapshot) error
	GetLatestSnapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error)
	ListSnapshotsForProject(ctx context.Context, projectID string, limit int) ([]SessionSnapshot, error)
	PruneOldSnapshots(ctx context.Context, sessionID string, keepN int) error
}

func (s *PostgresStore) InsertFileOperation(ctx context.Context, op FileOperation) error {
	if strings.TrimSpace(op.ProjectID) == "" || strings.TrimSpace(op.SessionID) == "" {
		return fmt.Errorf("file operation requires project_id and session_id")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO session_file_operations (
			project_id, session_id, turn_index, harness, tool_name, file_path,
			op_type, line_start, line_end, diff_hunk, content_hash
		) VALUES (
			$1::uuid, $2::uuid, NULLIF($3,0), $4, $5, $6,
			$7::file_op_type, NULLIF($8,0), NULLIF($9,0), NULLIF($10,''), NULLIF($11,'')
		)`,
		op.ProjectID, op.SessionID, op.TurnIndex, op.Harness, op.ToolName, op.FilePath,
		op.OpType, op.LineStart, op.LineEnd, op.DiffHunk, op.ContentHash,
	)
	return err
}

func (s *PostgresStore) InsertToolExecution(ctx context.Context, exec ToolExecution) error {
	if strings.TrimSpace(exec.ProjectID) == "" || strings.TrimSpace(exec.SessionID) == "" {
		return fmt.Errorf("tool execution requires project_id and session_id")
	}
	var exit any
	if exec.ExitCode != nil {
		exit = *exec.ExitCode
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO session_tool_executions (
			project_id, session_id, harness, tool_name, command_line,
			working_directory, exit_code, output_snippet, truncated
		) VALUES (
			$1::uuid, $2::uuid, $3, $4, $5, NULLIF($6,''), $7, NULLIF($8,''), $9
		)`,
		exec.ProjectID, exec.SessionID, exec.Harness, exec.ToolName, exec.CommandLine,
		exec.WorkingDirectory, exit, exec.OutputSnippet, exec.Truncated,
	)
	return err
}

func (s *PostgresStore) InsertBatchFileOperations(ctx context.Context, ops []FileOperation) error {
	for _, op := range ops {
		if err := s.InsertFileOperation(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ListFileOperations(ctx context.Context, sessionID string, opts FileOpListOpts) ([]FileOperation, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	q := `
		SELECT id::text, project_id::text, session_id::text, COALESCE(turn_index,0), harness, tool_name,
		       file_path, op_type::text, COALESCE(line_start,0), COALESCE(line_end,0),
		       COALESCE(diff_hunk,''), COALESCE(content_hash,''), created_at
		FROM session_file_operations
		WHERE session_id = $1::uuid`
	args := []any{sessionID}
	n := 2
	if opts.OpType != "" {
		q += fmt.Sprintf(` AND op_type = $%d::file_op_type`, n)
		args = append(args, opts.OpType)
		n++
	}
	if opts.FilePath != "" {
		q += fmt.Sprintf(` AND file_path = $%d`, n)
		args = append(args, opts.FilePath)
		n++
	}
	q += fmt.Sprintf(` ORDER BY created_at ASC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileOperation
	for rows.Next() {
		var op FileOperation
		if err := rows.Scan(&op.ID, &op.ProjectID, &op.SessionID, &op.TurnIndex, &op.Harness, &op.ToolName,
			&op.FilePath, &op.OpType, &op.LineStart, &op.LineEnd, &op.DiffHunk, &op.ContentHash, &op.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListToolExecutions(ctx context.Context, sessionID string) ([]ToolExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, project_id::text, session_id::text, harness, tool_name, command_line,
		       COALESCE(working_directory,''), exit_code, COALESCE(output_snippet,''), truncated, created_at
		FROM session_tool_executions
		WHERE session_id = $1::uuid
		ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolExecution
	for rows.Next() {
		var te ToolExecution
		var exit *int
		if err := rows.Scan(&te.ID, &te.ProjectID, &te.SessionID, &te.Harness, &te.ToolName, &te.CommandLine,
			&te.WorkingDirectory, &exit, &te.OutputSnippet, &te.Truncated, &te.CreatedAt); err != nil {
			return nil, err
		}
		te.ExitCode = exit
		out = append(out, te)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertSessionSnapshot(ctx context.Context, snap *SessionSnapshot) error {
	if snap == nil {
		return fmt.Errorf("snapshot is nil")
	}
	if strings.TrimSpace(snap.SessionID) == "" || strings.TrimSpace(snap.ProjectID) == "" {
		return fmt.Errorf("snapshot requires session_id and project_id")
	}
	var ver int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(snapshot_version), 0) + 1
		FROM session_snapshots WHERE session_id = $1::uuid`, snap.SessionID).Scan(&ver)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO session_snapshots (
			session_id, snapshot_version, project_id, harness, conversation_id, turn_count,
			git_branch, git_commit, git_dirty, uncommitted_diff, diff_size_bytes, diff_truncated,
			transcript_payload, artifacts_bundle, source_machine_id
		) VALUES (
			$1::uuid, $2, $3::uuid, $4, $5, $6,
			NULLIF($7,''), NULLIF($8,''), $9, $10, $11, $12,
			$13, $14, NULLIF($15,'')
		)`,
		snap.SessionID, ver, snap.ProjectID, snap.Harness, snap.ConversationID, snap.TurnCount,
		snap.GitBranch, snap.GitCommit, snap.GitDirty, snap.UncommittedDiff, snap.DiffSizeBytes, snap.DiffTruncated,
		snap.TranscriptPayload, snap.ArtifactsBundle, snap.SourceMachineID,
	)
	if err == nil {
		snap.SnapshotVersion = ver
	}
	return err
}

func (s *PostgresStore) GetLatestSnapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, session_id::text, snapshot_version, project_id::text, harness, conversation_id, turn_count,
		       COALESCE(git_branch,''), COALESCE(git_commit,''), COALESCE(git_dirty,false),
		       uncommitted_diff, COALESCE(diff_size_bytes,0), diff_truncated,
		       transcript_payload, artifacts_bundle, COALESCE(source_machine_id,''),
		       created_at, updated_at
		FROM session_snapshots
		WHERE session_id = $1::uuid
		ORDER BY snapshot_version DESC
		LIMIT 1`, sessionID)
	return scanSnapshot(row)
}

func (s *PostgresStore) ListSnapshotsForProject(ctx context.Context, projectID string, limit int) ([]SessionSnapshot, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, session_id::text, snapshot_version, project_id::text, harness, conversation_id, turn_count,
		       COALESCE(git_branch,''), COALESCE(git_commit,''), COALESCE(git_dirty,false),
		       NULL::bytea, COALESCE(diff_size_bytes,0), diff_truncated,
		       NULL::bytea, NULL::bytea, COALESCE(source_machine_id,''),
		       created_at, updated_at
		FROM (
		  SELECT DISTINCT ON (session_id) *
		  FROM session_snapshots
		  WHERE project_id = $1::uuid
		  ORDER BY session_id, snapshot_version DESC
		) latest
		ORDER BY updated_at DESC
		LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSnapshot
	for rows.Next() {
		snap, err := scanSnapshotRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *snap)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PruneOldSnapshots(ctx context.Context, sessionID string, keepN int) error {
	if keepN <= 0 {
		keepN = 5
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM session_snapshots
		WHERE session_id = $1::uuid
		  AND snapshot_version NOT IN (
		    SELECT snapshot_version FROM session_snapshots
		    WHERE session_id = $1::uuid
		    ORDER BY snapshot_version DESC
		    LIMIT $2
		  )`, sessionID, keepN)
	return err
}

// PruneAllSnapshotsKeepN deletes old versions across all sessions (keep latest keepN each).
func (s *PostgresStore) PruneAllSnapshotsKeepN(ctx context.Context, keepN int) error {
	if keepN <= 0 {
		keepN = 5
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM session_snapshots s
		WHERE ctid IN (
		  SELECT ctid FROM (
		    SELECT ctid,
		           ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY snapshot_version DESC) AS rn
		    FROM session_snapshots
		  ) ranked
		  WHERE rn > $1
		)`, keepN)
	return err
}

func scanSnapshot(row pgx.Row) (*SessionSnapshot, error) {
	var snap SessionSnapshot
	err := row.Scan(
		&snap.ID, &snap.SessionID, &snap.SnapshotVersion, &snap.ProjectID, &snap.Harness, &snap.ConversationID, &snap.TurnCount,
		&snap.GitBranch, &snap.GitCommit, &snap.GitDirty,
		&snap.UncommittedDiff, &snap.DiffSizeBytes, &snap.DiffTruncated,
		&snap.TranscriptPayload, &snap.ArtifactsBundle, &snap.SourceMachineID,
		&snap.CreatedAt, &snap.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func scanSnapshotRows(rows pgx.Rows) (*SessionSnapshot, error) {
	var snap SessionSnapshot
	err := rows.Scan(
		&snap.ID, &snap.SessionID, &snap.SnapshotVersion, &snap.ProjectID, &snap.Harness, &snap.ConversationID, &snap.TurnCount,
		&snap.GitBranch, &snap.GitCommit, &snap.GitDirty,
		&snap.UncommittedDiff, &snap.DiffSizeBytes, &snap.DiffTruncated,
		&snap.TranscriptPayload, &snap.ArtifactsBundle, &snap.SourceMachineID,
		&snap.CreatedAt, &snap.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &snap, nil
}
