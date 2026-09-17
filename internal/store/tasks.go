// Task tracking (issue #34, plan §1.1).
//
// The tasks table shipped in migration 001 but had zero store coverage.
// This file provides CRUD plus the status machine and the episode link
// (bug-fix tasks point at their episode) over the DBTX seam (db.go),
// following the projects.go/workspaces.go conventions: NULL-coalescing
// column lists, pgx.ErrNoRows mapped to wrapped ErrNotFound, validation
// before any statement.
//
// Testability: TaskStore depends only on DBTX, so unit tests run against a
// scripted fake (tasks_test.go); the status machine (IsValidTaskStatus,
// CanTransitionTaskStatus) is pure and covered DB-free.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Task lifecycle states (CHECK constraint in migration 001).
const (
	TaskOpen       = "OPEN"
	TaskInProgress = "IN_PROGRESS"
	TaskDone       = "DONE"
	TaskBlocked    = "BLOCKED"
)

// taskTransitions is the allowed status machine. DONE is terminal except
// for an explicit reopen to OPEN; BLOCKED always routes back through
// OPEN/IN_PROGRESS so a blocked task is never marked DONE without
// re-entering active work.
var taskTransitions = map[string][]string{
	TaskOpen:       {TaskInProgress, TaskBlocked, TaskDone},
	TaskInProgress: {TaskDone, TaskBlocked, TaskOpen},
	TaskBlocked:    {TaskOpen, TaskInProgress},
	TaskDone:       {TaskOpen},
}

// IsValidTaskStatus reports whether status is a known task state.
func IsValidTaskStatus(status string) bool {
	switch status {
	case TaskOpen, TaskInProgress, TaskDone, TaskBlocked:
		return true
	default:
		return false
	}
}

// NormalizeTaskStatus upper-cases and trims status, defaulting "" to OPEN.
// The second return is false when the status is unknown.
func NormalizeTaskStatus(status string) (string, bool) {
	s := strings.ToUpper(strings.TrimSpace(status))
	if s == "" {
		return TaskOpen, true
	}
	if !IsValidTaskStatus(s) {
		return s, false
	}
	return s, true
}

// CanTransitionTaskStatus reports whether the status machine allows moving
// from → to. A no-op (from == to) is always allowed so idempotent writers
// never fail. Unknown states never transition.
func CanTransitionTaskStatus(from, to string) bool {
	if !IsValidTaskStatus(from) || !IsValidTaskStatus(to) {
		return false
	}
	if from == to {
		return true
	}
	for _, next := range taskTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Task mirrors a tasks row (migration 001). Empty EpisodeID/SessionID/
// Description/AssignedTo mean SQL NULL.
// Task rows use the shared Task model (models.go).
// TaskParams carries task identity for Create.
type TaskParams struct {
	ProjectID   string
	EpisodeID   string // optional; "" = NULL (linked later via SetEpisode)
	SessionID   string // optional; "" = NULL
	Title       string
	Description string // optional; "" = NULL
	Status      string // optional; "" = OPEN
	AssignedTo  string // optional user/agent id; "" = NULL
	CreatedBy   string
}

// taskColumns selects tasks with NULLs coalesced and UUIDs as text.
const taskColumns = `id::TEXT AS id, ` +
	`project_id::TEXT AS project_id, ` +
	`COALESCE(episode_id::TEXT, '') AS episode_id, ` +
	`COALESCE(session_id::TEXT, '') AS session_id, ` +
	`title, ` +
	`COALESCE(description, '') AS description, ` +
	`status, ` +
	`COALESCE(assigned_to::TEXT, '') AS assigned_to, ` +
	`created_by::TEXT AS created_by, ` +
	`created_at, ` +
	`updated_at`

// scanTask scans a full taskColumns row.
func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	if err := row.Scan(
		&t.ID, &t.ProjectID, &t.EpisodeID, &t.SessionID, &t.Title,
		&t.Description, &t.Status, &t.AssignedTo, &t.CreatedBy,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &t, nil
}

// TaskStore is task CRUD plus status transitions and episode links.
type TaskStore struct {
	db DBTX
}

// NewTaskStore wires a TaskStore to any DBTX (pool, transaction, fake).
func NewTaskStore(db DBTX) *TaskStore {
	return &TaskStore{db: db}
}

// Create inserts a task. Project, title, and creator are required; the
// episode link may be attached at creation or later via SetEpisode.
func (s *TaskStore) Create(ctx context.Context, params TaskParams) (*Task, error) {
	if strings.TrimSpace(params.ProjectID) == "" {
		return nil, errors.New("store: task project id is required")
	}
	if strings.TrimSpace(params.Title) == "" {
		return nil, errors.New("store: task title is required")
	}
	if strings.TrimSpace(params.CreatedBy) == "" {
		return nil, errors.New("store: task creator is required")
	}
	status, ok := NormalizeTaskStatus(params.Status)
	if !ok {
		return nil, fmt.Errorf("store: unknown task status %q", params.Status)
	}
	t, err := scanTask(s.db.QueryRow(ctx,
		`INSERT INTO tasks (project_id, episode_id, session_id, title, description, status, assigned_to, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+taskColumns,
		params.ProjectID,
		nullUUID(strings.TrimSpace(params.EpisodeID)),
		nullUUID(strings.TrimSpace(params.SessionID)),
		strings.TrimSpace(params.Title),
		nullText(strings.TrimSpace(params.Description)),
		status,
		nullUUID(strings.TrimSpace(params.AssignedTo)),
		strings.TrimSpace(params.CreatedBy)))
	if err != nil {
		return nil, fmt.Errorf("store: create task: %w", err)
	}
	return t, nil
}

// GetByID fetches one task or a wrapped ErrNotFound.
func (s *TaskStore) GetByID(ctx context.Context, id string) (*Task, error) {
	t, err := scanTask(s.db.QueryRow(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: task %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get task: %w", err)
	}
	return t, nil
}

// ListByProject returns a project's tasks newest-first with a clamped
// limit. Empty status lists every state; otherwise the status must be
// known (validated before any query).
func (s *TaskStore) ListByProject(ctx context.Context, projectID, status string, limit, offset int) ([]Task, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("store: task project id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE project_id = $1`
	args := []any{projectID}
	if strings.TrimSpace(status) != "" {
		normalized, ok := NormalizeTaskStatus(status)
		if !ok {
			return nil, fmt.Errorf("store: unknown task status %q", status)
		}
		args = append(args, normalized)
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(
			&t.ID, &t.ProjectID, &t.EpisodeID, &t.SessionID, &t.Title,
			&t.Description, &t.Status, &t.AssignedTo, &t.CreatedBy,
			&t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: list tasks scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tasks rows: %w", err)
	}
	return out, nil
}

// SetStatus moves a task through the status machine: the current row is
// read first and the transition is validated with CanTransitionTaskStatus,
// so illegal jumps (e.g. DONE → IN_PROGRESS, BLOCKED → DONE) fail before
// any write. updated_at is bumped by the database.
func (s *TaskStore) SetStatus(ctx context.Context, id, status string) (*Task, error) {
	normalized, ok := NormalizeTaskStatus(status)
	if !ok {
		return nil, fmt.Errorf("store: unknown task status %q", status)
	}
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !CanTransitionTaskStatus(current.Status, normalized) {
		return nil, fmt.Errorf("store: illegal task transition %s -> %s", current.Status, normalized)
	}
	t, err := scanTask(s.db.QueryRow(ctx,
		`UPDATE tasks SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+taskColumns,
		id, normalized))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: task %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: set task status: %w", err)
	}
	return t, nil
}

// SetEpisode links (or unlinks, with "") a task to its episode — the
// bug-fix arc the task works. updated_at is bumped by the database.
func (s *TaskStore) SetEpisode(ctx context.Context, id, episodeID string) (*Task, error) {
	t, err := scanTask(s.db.QueryRow(ctx,
		`UPDATE tasks SET episode_id = $2, updated_at = now() WHERE id = $1 RETURNING `+taskColumns,
		id, nullUUID(strings.TrimSpace(episodeID))))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: task %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: set task episode: %w", err)
	}
	return t, nil
}

// Delete removes a task.
func (s *TaskStore) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: task %s: %w", id, ErrNotFound)
	}
	return nil
}
