// Unit tests for tasks (issue #34).
//
// DB-free by design: the status machine (Normalize/IsValid/CanTransition)
// is pure, and TaskStore SQL paths run against taskFakeDB, which records
// statements and replays canned rows (sessions_test.go fake pattern).
package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Fake DBTX (task-scoped names; sessions_test.go owns sessionFakeDB).
// ---------------------------------------------------------------------------

// taskFakeRow replays one canned pgx.Row Scan.
type taskFakeRow struct {
	values []any
	err    error
}

func (r taskFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("taskFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = r.values[i].(string)
		case *time.Time:
			*ptr = r.values[i].(time.Time)
		default:
			return errors.New("taskFakeRow: unsupported dest")
		}
	}
	return nil
}

// taskFakeRows replays a store.Rows result set.
type taskFakeRows struct {
	rows [][]any
	pos  int
}

func (f *taskFakeRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *taskFakeRows) Err() error { return nil }
func (f *taskFakeRows) Close()     {}
func (f *taskFakeRows) Scan(dest ...any) error {
	return taskFakeRow{values: f.rows[f.pos-1]}.Scan(dest...)
}

// taskFakeDB scripts QueryRow as a queue and Query as one result set,
// recording every statement.
type taskFakeDB struct {
	rowQueue []taskFakeRow
	rows     *taskFakeRows
	queryErr error
	queries  []string
	execs    int
}

func (f *taskFakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs++
	f.queries = append(f.queries, sql)
	return pgconn.CommandTag{}, nil
}

func (f *taskFakeDB) Query(_ context.Context, sql string, _ ...any) (store.Rows, error) {
	f.queries = append(f.queries, sql)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *taskFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	if len(f.rowQueue) == 0 {
		return taskFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

func taskRow(id, project, episode, session, title, desc, status, assignee, creator string) taskFakeRow {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	return taskFakeRow{values: []any{
		id, project, episode, session, title, desc, status, assignee, creator, now, now,
	}}
}

// ---------------------------------------------------------------------------
// Status machine (pure)
// ---------------------------------------------------------------------------

func TestTaskStatusValidation(t *testing.T) {
	for _, s := range []string{store.TaskOpen, store.TaskInProgress, store.TaskDone, store.TaskBlocked} {
		if !store.IsValidTaskStatus(s) {
			t.Errorf("IsValidTaskStatus(%q) = false, want true", s)
		}
	}
	if store.IsValidTaskStatus("TODO") || store.IsValidTaskStatus("") {
		t.Error("unknown/empty status must be invalid")
	}
	if got, ok := store.NormalizeTaskStatus(""); !ok || got != store.TaskOpen {
		t.Errorf("empty status = %q,%v; want OPEN,true", got, ok)
	}
	if got, ok := store.NormalizeTaskStatus("  in_progress "); !ok || got != store.TaskInProgress {
		t.Errorf("in_progress = %q,%v; want IN_PROGRESS,true", got, ok)
	}
	if _, ok := store.NormalizeTaskStatus("TODO"); ok {
		t.Error("TODO must be rejected")
	}
}

func TestTaskCanTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{store.TaskOpen, store.TaskInProgress, true},
		{store.TaskOpen, store.TaskBlocked, true},
		{store.TaskOpen, store.TaskDone, true},
		{store.TaskInProgress, store.TaskDone, true},
		{store.TaskInProgress, store.TaskBlocked, true},
		{store.TaskInProgress, store.TaskOpen, true},
		{store.TaskBlocked, store.TaskOpen, true},
		{store.TaskBlocked, store.TaskInProgress, true},
		{store.TaskDone, store.TaskOpen, true}, // explicit reopen
		// Forbidden jumps.
		{store.TaskBlocked, store.TaskDone, false},
		{store.TaskDone, store.TaskInProgress, false},
		{store.TaskDone, store.TaskBlocked, false},
		{store.TaskDone, store.TaskDone, true}, // idempotent rewrite
		{store.TaskOpen, store.TaskOpen, true}, // no-op always allowed
		{store.TaskOpen, "TODO", false},
		{"TODO", store.TaskOpen, false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := store.CanTransitionTaskStatus(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransition(%q -> %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Store SQL paths (scripted DBTX)
// ---------------------------------------------------------------------------

func TestTaskCreateValidationBlocksDB(t *testing.T) {
	fake := &taskFakeDB{}
	valid := store.TaskParams{ProjectID: "p1", Title: "Fix auth", CreatedBy: "u1"}
	for name, mutate := range map[string]func(*store.TaskParams){
		"missing project": func(p *store.TaskParams) { p.ProjectID = "" },
		"missing title":   func(p *store.TaskParams) { p.Title = "  " },
		"missing creator": func(p *store.TaskParams) { p.CreatedBy = "" },
		"bad status":      func(p *store.TaskParams) { p.Status = "TODO" },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			mutate(&p)
			if _, err := store.NewTaskStore(fake).Create(context.Background(), p); err == nil {
				t.Error("invalid create must fail")
			}
		})
	}
	if len(fake.queries) != 0 {
		t.Errorf("invalid creates must issue no statements, got %v", fake.queries)
	}
}

func TestTaskCreateDefaultsOpen(t *testing.T) {
	fake := &taskFakeDB{rowQueue: []taskFakeRow{
		taskRow("t1", "p1", "", "", "Fix auth", "", store.TaskOpen, "", "u1"),
	}}
	got, err := store.NewTaskStore(fake).Create(context.Background(), store.TaskParams{
		ProjectID: "p1", Title: "Fix auth", CreatedBy: "u1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t1" || got.Status != store.TaskOpen {
		t.Errorf("unexpected task: %+v", got)
	}
	if !strings.Contains(fake.queries[0], "INSERT INTO tasks") {
		t.Errorf("create must insert:\n%s", fake.queries[0])
	}
}

func TestTaskGetByIDNotFound(t *testing.T) {
	fake := &taskFakeDB{rowQueue: []taskFakeRow{{err: pgx.ErrNoRows}}}
	_, err := store.NewTaskStore(fake).GetByID(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing task must wrap ErrNotFound, got %v", err)
	}
}

func TestTaskSetStatusHappyPath(t *testing.T) {
	fake := &taskFakeDB{rowQueue: []taskFakeRow{
		taskRow("t1", "p1", "", "", "Fix auth", "", store.TaskOpen, "", "u1"),
		taskRow("t1", "p1", "", "", "Fix auth", "", store.TaskInProgress, "", "u1"),
	}}
	got, err := store.NewTaskStore(fake).SetStatus(context.Background(), "t1", "in_progress")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.TaskInProgress {
		t.Errorf("status = %q, want IN_PROGRESS", got.Status)
	}
	if len(fake.queries) != 2 {
		t.Fatalf("SetStatus must read-then-write (queries=%d)", len(fake.queries))
	}
	if !strings.Contains(fake.queries[1], "UPDATE tasks SET status") {
		t.Errorf("second statement must update status:\n%s", fake.queries[1])
	}
}

func TestTaskSetStatusIllegalTransitionWritesNothing(t *testing.T) {
	fake := &taskFakeDB{rowQueue: []taskFakeRow{
		taskRow("t1", "p1", "", "", "Fix auth", "", store.TaskDone, "", "u1"),
	}}
	_, err := store.NewTaskStore(fake).SetStatus(context.Background(), "t1", store.TaskInProgress)
	if err == nil {
		t.Fatal("DONE -> IN_PROGRESS must fail")
	}
	if len(fake.queries) != 1 {
		t.Errorf("illegal transition must stop after the read (queries=%d: %v)", len(fake.queries), fake.queries)
	}
}

func TestTaskSetStatusUnknownStatusBlocksDB(t *testing.T) {
	fake := &taskFakeDB{}
	if _, err := store.NewTaskStore(fake).SetStatus(context.Background(), "t1", "TODO"); err == nil {
		t.Error("unknown status must fail")
	}
	if len(fake.queries) != 0 {
		t.Errorf("unknown status must issue no statements, got %v", fake.queries)
	}
}

func TestTaskSetEpisodeLinksAndUnlinks(t *testing.T) {
	fake := &taskFakeDB{rowQueue: []taskFakeRow{
		taskRow("t1", "p1", "e9", "", "Fix auth", "", store.TaskOpen, "", "u1"),
		taskRow("t1", "p1", "", "", "Fix auth", "", store.TaskOpen, "", "u1"),
	}}
	s := store.NewTaskStore(fake)
	linked, err := s.SetEpisode(context.Background(), "t1", "e9")
	if err != nil {
		t.Fatal(err)
	}
	if linked.EpisodeID != "e9" {
		t.Errorf("episode link = %q, want e9", linked.EpisodeID)
	}
	unlinked, err := s.SetEpisode(context.Background(), "t1", "")
	if err != nil {
		t.Fatal(err)
	}
	if unlinked.EpisodeID != "" {
		t.Errorf("episode unlink = %q, want empty", unlinked.EpisodeID)
	}
	for _, q := range fake.queries {
		if !strings.Contains(q, "UPDATE tasks SET episode_id") {
			t.Errorf("SetEpisode must update episode_id:\n%s", q)
		}
	}
}

func TestTaskListByProjectFiltersStatus(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	mkRows := func() *taskFakeRows {
		return &taskFakeRows{rows: [][]any{
			{"t1", "p1", "", "", "A", "", store.TaskOpen, "", "u1", now, now},
			{"t2", "p1", "", "", "B", "", store.TaskOpen, "", "u1", now, now},
		}}
	}
	fake := &taskFakeDB{rows: mkRows()}
	got, err := store.NewTaskStore(fake).ListByProject(context.Background(), "p1", "open", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByProject = %+v, want 2 tasks", got)
	}
	if !strings.Contains(fake.queries[0], "project_id = $1") || !strings.Contains(fake.queries[0], "status = $2") {
		t.Errorf("filtered list must constrain project + status:\n%s", fake.queries[0])
	}

	unfiltered := &taskFakeDB{rows: mkRows()}
	if _, err := store.NewTaskStore(unfiltered).ListByProject(context.Background(), "p1", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unfiltered.queries[0], "status =") {
		t.Errorf("unfiltered list must not constrain status:\n%s", unfiltered.queries[0])
	}

	bad := &taskFakeDB{}
	if _, err := store.NewTaskStore(bad).ListByProject(context.Background(), "p1", "TODO", 0, 0); err == nil {
		t.Error("unknown status filter must fail")
	}
	if len(bad.queries) != 0 {
		t.Errorf("bad status must issue no statements, got %v", bad.queries)
	}
}

func TestTaskDeleteNotFound(t *testing.T) {
	fake := &taskFakeDB{}
	if err := store.NewTaskStore(fake).Delete(context.Background(), "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete of missing task must wrap ErrNotFound, got %v", err)
	}
}
