package main

// A disposable SQLite backing adapter for the real APITokenStore. It runs only
// in this loopback demo; token generation, hashing, binding and authentication
// remain the normal server handlers, not canned video responses.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"central-memory/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "modernc.org/sqlite"
)

type demoTokenDB struct{ db *sql.DB }

func newDemoTokenStore() (*store.APITokenStore, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE api_tokens (
	 id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))), user_id TEXT NOT NULL,
	 name TEXT NOT NULL, token_prefix TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL,
	 scopes TEXT NOT NULL, agent_id TEXT, created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
	 last_used_at TEXT, revoked_at TEXT)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return store.NewAPITokenStore(demoTokenDB{db}), nil
}

func tokenSQL(q string) string {
	return strings.NewReplacer("::TEXT", "", "::int", "", "now()", "strftime('%Y-%m-%dT%H:%M:%fZ','now')").Replace(q)
}
func tokenArgs(args []any) []any {
	out := append([]any(nil), args...)
	for i, v := range out {
		if s, ok := v.([]string); ok {
			b, _ := json.Marshal(s)
			out[i] = string(b)
		}
	}
	return out
}
func (d demoTokenDB) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	r, err := d.db.ExecContext(ctx, tokenSQL(q), tokenArgs(args)...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, err := r.RowsAffected()
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", n)), err
}
func (d demoTokenDB) Query(ctx context.Context, q string, args ...any) (store.Rows, error) {
	r, err := d.db.QueryContext(ctx, tokenSQL(q), tokenArgs(args)...)
	if err != nil {
		return nil, err
	}
	return &demoTokenRows{rows: r}, nil
}
func (d demoTokenDB) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	r, err := d.db.QueryContext(ctx, tokenSQL(q), tokenArgs(args)...)
	return &demoTokenRows{rows: r, err: err, single: true}
}

type demoTokenRows struct {
	rows   *sql.Rows
	err    error
	single bool
}

func (r *demoTokenRows) Next() bool { return r.rows != nil && r.rows.Next() }
func (r *demoTokenRows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.rows.Err()
}
func (r *demoTokenRows) Close() {
	if r.rows != nil {
		r.rows.Close()
	}
}
func (r *demoTokenRows) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.single {
		defer r.Close()
		if !r.Next() {
			if err := r.rows.Err(); err != nil {
				return err
			}
			return pgx.ErrNoRows
		}
	}
	values := make([]any, len(dest))
	cells := make([]any, len(dest))
	for i := range values {
		cells[i] = &values[i]
	}
	if err := r.rows.Scan(cells...); err != nil {
		return err
	}
	for i, v := range values {
		s := ""
		if v != nil {
			s = fmt.Sprint(v)
		}
		switch target := dest[i].(type) {
		case *string:
			*target = s
		case *[]string:
			if err := json.Unmarshal([]byte(s), target); err != nil {
				return err
			}
		case *int:
			n, ok := v.(int64)
			if !ok {
				return errors.New("invalid demo token count")
			}
			*target = int(n)
		case *time.Time:
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return err
			}
			*target = t
		case **time.Time:
			if v == nil {
				*target = nil
			} else {
				t, err := time.Parse(time.RFC3339Nano, s)
				if err != nil {
					return err
				}
				*target = &t
			}
		default:
			return fmt.Errorf("unsupported demo token field %T", dest[i])
		}
	}
	return nil
}
