// Unit tests for watched files (issue #34).
//
// DB-free by design: file-type validation and StaleWatchedFiles are pure,
// and WatchedFileStore SQL paths run against watchedFakeDB, which records
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
// Fake DBTX (watched-scoped names; sessions_test.go owns sessionFakeDB).
// ---------------------------------------------------------------------------

// watchedFakeRow replays one canned pgx.Row Scan.
type watchedFakeRow struct {
	values []any
	err    error
}

func (r watchedFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("watchedFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = r.values[i].(string)
		case *time.Time:
			*ptr = r.values[i].(time.Time)
		default:
			return errors.New("watchedFakeRow: unsupported dest")
		}
	}
	return nil
}

// watchedFakeRows replays a store.Rows result set.
type watchedFakeRows struct {
	rows [][]any
	pos  int
}

func (f *watchedFakeRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *watchedFakeRows) Err() error { return nil }
func (f *watchedFakeRows) Close()     {}
func (f *watchedFakeRows) Scan(dest ...any) error {
	return watchedFakeRow{values: f.rows[f.pos-1]}.Scan(dest...)
}

// watchedFakeDB scripts QueryRow as a queue and Query as one result set,
// recording every statement; Exec reports execAffected rows.
type watchedFakeDB struct {
	rowQueue     []watchedFakeRow
	rows         *watchedFakeRows
	queryErr     error
	queries      []string
	execAffected int64
}

func (f *watchedFakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.queries = append(f.queries, sql)
	if f.execAffected == 0 {
		return pgconn.CommandTag{}, nil
	}
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func (f *watchedFakeDB) Query(_ context.Context, sql string, _ ...any) (store.Rows, error) {
	f.queries = append(f.queries, sql)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *watchedFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	if len(f.rowQueue) == 0 {
		return watchedFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

func watchedRow(id, workspace, path, hash, fileType string) watchedFakeRow {
	return watchedFakeRow{values: []any{
		id, workspace, path, hash, fileType,
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}}
}

// ---------------------------------------------------------------------------
// File types (pure)
// ---------------------------------------------------------------------------

func TestWatchedFileTypeValidation(t *testing.T) {
	for _, ft := range []string{
		store.WatchedClaudeMD, store.WatchedCursorRules,
		store.WatchedCopilotInstructions, store.WatchedWindsurfRules,
		store.WatchedCustom,
	} {
		if !store.IsValidWatchedFileType(ft) {
			t.Errorf("IsValidWatchedFileType(%q) = false, want true", ft)
		}
	}
	if store.IsValidWatchedFileType("CLAUDE_MD") || store.IsValidWatchedFileType("") {
		t.Error("un-normalized/empty type must be invalid")
	}
	if got, ok := store.NormalizeWatchedFileType("  CLAUDE_MD "); !ok || got != store.WatchedClaudeMD {
		t.Errorf("normalize = %q,%v; want claude_md,true", got, ok)
	}
	if _, ok := store.NormalizeWatchedFileType("vimrc"); ok {
		t.Error("vimrc must be rejected")
	}
}

// ---------------------------------------------------------------------------
// Store SQL paths (scripted DBTX)
// ---------------------------------------------------------------------------

func TestWatchedFileUpsertValidationBlocksDB(t *testing.T) {
	fake := &watchedFakeDB{}
	valid := store.WatchedFileParams{WorkspaceID: "ws1", Path: "CLAUDE.md", LastHash: "abc", FileType: "claude_md"}
	for name, mutate := range map[string]func(*store.WatchedFileParams){
		"missing workspace": func(p *store.WatchedFileParams) { p.WorkspaceID = "" },
		"missing path":      func(p *store.WatchedFileParams) { p.Path = " " },
		"bad file type":     func(p *store.WatchedFileParams) { p.FileType = "vimrc" },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			mutate(&p)
			if _, err := store.NewWatchedFileStore(fake).Upsert(context.Background(), p); err == nil {
				t.Error("invalid upsert must fail")
			}
		})
	}
	if len(fake.queries) != 0 {
		t.Errorf("invalid upserts must issue no statements, got %v", fake.queries)
	}
}

func TestWatchedFileUpsertInsertsAndUpdatesOnConflict(t *testing.T) {
	fake := &watchedFakeDB{rowQueue: []watchedFakeRow{
		watchedRow("w1", "ws1", "CLAUDE.md", "abc", "claude_md"),
	}}
	got, err := store.NewWatchedFileStore(fake).Upsert(context.Background(), store.WatchedFileParams{
		WorkspaceID: "ws1", Path: "CLAUDE.md", LastHash: "abc", FileType: "claude_md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "w1" || got.LastHash != "abc" {
		t.Errorf("unexpected watched file: %+v", got)
	}
	q := fake.queries[0]
	if !strings.Contains(q, "INSERT INTO watched_files") {
		t.Errorf("upsert must insert:\n%s", q)
	}
	if !strings.Contains(q, "ON CONFLICT (workspace_id, path)") {
		t.Errorf("upsert must target the natural key:\n%s", q)
	}
	if !strings.Contains(q, "last_hash = EXCLUDED.last_hash") {
		t.Errorf("upsert must refresh the hash on conflict:\n%s", q)
	}
}

func TestWatchedFileGetByWorkspacePath(t *testing.T) {
	fake := &watchedFakeDB{rowQueue: []watchedFakeRow{
		watchedRow("w1", "ws1", "CLAUDE.md", "abc", "claude_md"),
	}}
	got, err := store.NewWatchedFileStore(fake).GetByWorkspacePath(context.Background(), "ws1", "CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "CLAUDE.md" || got.FileType != "claude_md" {
		t.Errorf("unexpected watched file: %+v", got)
	}
	if !strings.Contains(fake.queries[0], "workspace_id = $1 AND path = $2") {
		t.Errorf("get must filter workspace + path:\n%s", fake.queries[0])
	}
}

func TestWatchedFileGetByWorkspacePathNotFound(t *testing.T) {
	fake := &watchedFakeDB{rowQueue: []watchedFakeRow{{err: pgx.ErrNoRows}}}
	_, err := store.NewWatchedFileStore(fake).GetByWorkspacePath(context.Background(), "ws1", "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing watched file must wrap ErrNotFound, got %v", err)
	}
}

func TestWatchedFileListByWorkspace(t *testing.T) {
	created := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &watchedFakeDB{rows: &watchedFakeRows{rows: [][]any{
		{"w2", "ws1", ".cursorrules", "def", "cursorrules", created},
		{"w1", "ws1", "CLAUDE.md", "abc", "claude_md", created},
	}}}
	got, err := store.NewWatchedFileStore(fake).ListByWorkspace(context.Background(), "ws1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != ".cursorrules" {
		t.Fatalf("ListByWorkspace = %+v, want path-ordered rows", got)
	}
	if !strings.Contains(fake.queries[0], "WHERE workspace_id = $1") || !strings.Contains(fake.queries[0], "ORDER BY path ASC") {
		t.Errorf("list must scope workspace + order by path:\n%s", fake.queries[0])
	}
}

func TestWatchedFileDelete(t *testing.T) {
	missing := &watchedFakeDB{}
	if err := store.NewWatchedFileStore(missing).Delete(context.Background(), "ws1", "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete of missing row must wrap ErrNotFound, got %v", err)
	}
	present := &watchedFakeDB{execAffected: 1}
	if err := store.NewWatchedFileStore(present).Delete(context.Background(), "ws1", "CLAUDE.md"); err != nil {
		t.Errorf("delete of present row must succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Staleness (pure)
// ---------------------------------------------------------------------------

func TestStaleWatchedFiles(t *testing.T) {
	stored := []store.WatchedFile{
		{ID: "w1", WorkspaceID: "ws1", Path: "CLAUDE.md", LastHash: "aaa"},
		{ID: "w2", WorkspaceID: "ws1", Path: ".cursorrules", LastHash: "bbb"},
		{ID: "w3", WorkspaceID: "ws1", Path: ".windsurfrules", LastHash: ""}, // never scanned
	}
	current := map[string]string{
		"CLAUDE.md":                       "aaa", // unchanged: not stale
		".cursorrules":                    "ccc", // edited: stale
		".windsurfrules":                  "ddd", // first scan: stale
		".github/copilot-instructions.md": "eee", // untracked: stale (synthesized)
	}
	got := store.StaleWatchedFiles(stored, current)
	if len(got) != 3 {
		t.Fatalf("StaleWatchedFiles = %+v, want 3 stale entries", got)
	}
	// Sorted by path for determinism.
	if got[0].Path != ".cursorrules" || got[1].Path != ".github/copilot-instructions.md" || got[2].Path != ".windsurfrules" {
		t.Fatalf("stale entries not sorted by path: %+v", got)
	}
	if got[0].ID != "w2" {
		t.Errorf("changed file must keep its stored identity: %+v", got[0])
	}
	if got := store.StaleWatchedFiles(stored, map[string]string{"CLAUDE.md": "aaa"}); len(got) != 0 {
		t.Errorf("unchanged scan must yield no stale entries, got %+v", got)
	}
	if got := store.StaleWatchedFiles(nil, nil); len(got) != 0 {
		t.Errorf("empty inputs must yield no stale entries, got %+v", got)
	}
}
