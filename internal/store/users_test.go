// Unit tests for users (issue #34).
//
// DB-free by design: userFakeDB scripts the DBTX seam (row queue for
// QueryRow, canned result set for Query) and records every statement plus
// args for SQL assertions, following the sessions_test.go fake pattern.
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
// Fake DBTX (user-scoped names; sessions_test.go owns sessionFakeDB).
// ---------------------------------------------------------------------------

// userFakeRow replays one canned pgx.Row Scan.
type userFakeRow struct {
	values []any
	err    error
}

func (r userFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("userFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = r.values[i].(string)
		case *time.Time:
			*ptr = r.values[i].(time.Time)
		default:
			return errors.New("userFakeRow: unsupported dest")
		}
	}
	return nil
}

// userFakeRows replays a store.Rows result set.
type userFakeRows struct {
	rows [][]any
	pos  int
}

func (f *userFakeRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *userFakeRows) Err() error { return nil }
func (f *userFakeRows) Close()     {}
func (f *userFakeRows) Scan(dest ...any) error {
	return userFakeRow{values: f.rows[f.pos-1]}.Scan(dest...)
}

// userFakeDB scripts QueryRow as a queue and Query as one result set,
// recording statements and args; Exec reports execAffected rows.
type userFakeDB struct {
	rowQueue     []userFakeRow
	rows         *userFakeRows
	queryErr     error
	queries      []string
	args         [][]any
	execAffected int64
}

func (f *userFakeDB) record(sql string, args []any) {
	f.queries = append(f.queries, sql)
	f.args = append(f.args, args)
}

func (f *userFakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.record(sql, args)
	if f.execAffected == 0 {
		return pgconn.CommandTag{}, nil
	}
	return pgconn.NewCommandTag("DELETE 1"), nil
}

func (f *userFakeDB) Query(_ context.Context, sql string, args ...any) (store.Rows, error) {
	f.record(sql, args)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *userFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.record(sql, args)
	if len(f.rowQueue) == 0 {
		return userFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

func userRow(id, username, email, settings string) userFakeRow {
	return userFakeRow{values: []any{
		id, username, email, settings,
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}}
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

func TestUserCreateValidationBlocksDB(t *testing.T) {
	fake := &userFakeDB{}
	if _, err := store.NewUserStore(fake).Create(context.Background(), store.UserParams{}); err == nil {
		t.Error("create without username must fail")
	}
	if _, err := store.NewUserStore(fake).Create(context.Background(), store.UserParams{Username: "  "}); err == nil {
		t.Error("create with blank username must fail")
	}
	if len(fake.queries) != 0 {
		t.Errorf("invalid create must issue no statements, got %v", fake.queries)
	}
}

func TestUserCreateDefaultsSettingsToEmptyObject(t *testing.T) {
	fake := &userFakeDB{rowQueue: []userFakeRow{userRow("u1", "alice", "", "{}")}}
	u, err := store.NewUserStore(fake).Create(context.Background(), store.UserParams{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "u1" || u.Username != "alice" || len(u.Settings) != 0 {
		t.Errorf("unexpected user: %+v", u)
	}
	if !strings.Contains(fake.queries[0], "INSERT INTO users") {
		t.Errorf("create must insert:\n%s", fake.queries[0])
	}
	if got := fake.args[0][2].(string); got != "{}" {
		t.Errorf("empty settings must default to '{}', got %q", got)
	}
}

func TestUserGetByUsername(t *testing.T) {
	fake := &userFakeDB{rowQueue: []userFakeRow{userRow("u1", "alice", "a@x.io", `{"theme":"dark"}`)}}
	u, err := store.NewUserStore(fake).GetByUsername(context.Background(), "  alice ")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "a@x.io" || u.Settings["theme"] != "dark" {
		t.Errorf("unexpected user: %+v", u)
	}
	if !strings.Contains(fake.queries[0], "WHERE username = $1") {
		t.Errorf("lookup must filter by username:\n%s", fake.queries[0])
	}
}

func TestUserGetByUsernameValidationBlocksDB(t *testing.T) {
	fake := &userFakeDB{}
	if _, err := store.NewUserStore(fake).GetByUsername(context.Background(), " "); err == nil {
		t.Error("blank username lookup must fail")
	}
	if len(fake.queries) != 0 {
		t.Errorf("invalid lookup must issue no statements, got %v", fake.queries)
	}
}

func TestUserGetByIDNotFound(t *testing.T) {
	fake := &userFakeDB{rowQueue: []userFakeRow{{err: pgx.ErrNoRows}}}
	_, err := store.NewUserStore(fake).GetByID(context.Background(), "missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing user must wrap ErrNotFound, got %v", err)
	}
}

func TestUserUpdateSettings(t *testing.T) {
	fake := &userFakeDB{rowQueue: []userFakeRow{userRow("u1", "alice", "", `{"provider":"bedrock"}`)}}
	u, err := store.NewUserStore(fake).UpdateSettings(context.Background(), "u1", `{"provider":"bedrock"}`)
	if err != nil {
		t.Fatal(err)
	}
	if u.Settings["provider"] != "bedrock" {
		t.Errorf("settings not updated: %+v", u)
	}
	if !strings.Contains(fake.queries[0], "UPDATE users SET settings") {
		t.Errorf("update must set settings:\n%s", fake.queries[0])
	}
}

func TestUserUpdateSettingsNotFound(t *testing.T) {
	fake := &userFakeDB{rowQueue: []userFakeRow{{err: pgx.ErrNoRows}}}
	_, err := store.NewUserStore(fake).UpdateSettings(context.Background(), "ghost", "{}")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update of missing user must wrap ErrNotFound, got %v", err)
	}
}

func TestUserDeleteNotFound(t *testing.T) {
	fake := &userFakeDB{}
	if err := store.NewUserStore(fake).Delete(context.Background(), "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete of missing user must wrap ErrNotFound, got %v", err)
	}
}

func TestUserListSQL(t *testing.T) {
	created := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &userFakeDB{rows: &userFakeRows{rows: [][]any{
		{"u1", "alice", "", "{}", created},
		{"u2", "bob", "b@x.io", "{}", created},
	}}}
	got, err := store.NewUserStore(fake).List(context.Background(), 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Username != "alice" || got[1].Username != "bob" {
		t.Fatalf("List = %+v, want [alice bob]", got)
	}
	if !strings.Contains(fake.queries[0], "FROM users") || !strings.Contains(fake.queries[0], "ORDER BY created_at DESC") {
		t.Errorf("List must select newest-first:\n%s", fake.queries[0])
	}
}
