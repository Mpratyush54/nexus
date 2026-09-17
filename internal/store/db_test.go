// Unit tests for db.go: pool config, migration runner, embedding parser.
// DB-free by design; live-Postgres coverage lives in integration_test.go
// (gated on TEST_POSTGRES_DSN).
package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeStoreDBTX is a DBTX that records Exec calls. Query/QueryRow are
// unimplemented — tests exercising them need scripted rows (follow-up).
type fakeStoreDBTX struct {
	execs      []string
	execErr    error
	failSubstr string // when set, only statements containing it fail
}

func (f *fakeStoreDBTX) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	if f.execErr != nil && (f.failSubstr == "" || strings.Contains(sql, f.failSubstr)) {
		return pgconn.CommandTag{}, f.execErr
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeStoreDBTX) Query(_ context.Context, _ string, _ ...any) (store.Rows, error) {
	return nil, errors.New("fakeStoreDBTX: Query not implemented")
}

func (f *fakeStoreDBTX) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return nil
}

func TestStore_DefaultConfigValues(t *testing.T) {
	cfg := store.DefaultConfig("postgres://localhost:5432/x")
	if cfg.DSN == "" {
		t.Error("DSN must be preserved")
	}
	if cfg.MaxConns <= 0 || cfg.MinConns <= 0 {
		t.Errorf("pool bounds must be positive, got min=%d max=%d", cfg.MinConns, cfg.MaxConns)
	}
	if cfg.MinConns > cfg.MaxConns {
		t.Errorf("MinConns (%d) must not exceed MaxConns (%d)", cfg.MinConns, cfg.MaxConns)
	}
	if cfg.MaxConnLifetime <= 0 || cfg.MaxConnIdleTime <= 0 {
		t.Error("connection lifetimes must be positive")
	}
	if cfg.HealthCheckPeriod <= 0 {
		t.Error("HealthCheckPeriod must be positive")
	}
	if cfg.ConnectTimeout <= 0 {
		t.Error("ConnectTimeout must be positive")
	}
}

func TestStore_ConnectRejectsEmptyDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := store.Connect(ctx, store.Config{}); err == nil {
		t.Error("expected error for empty Config")
	}
	if _, err := store.Connect(ctx, store.DefaultConfig("")); err == nil {
		t.Error("expected error for empty DSN")
	}
	if _, err := store.Connect(ctx, store.DefaultConfig("://bad dsn %%")); err == nil {
		t.Error("expected error for unparsable DSN")
	}
}

func writeStoreFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStore_ListMigrationFiles(t *testing.T) {
	dir := t.TempDir()
	writeStoreFile(t, dir, "002_events.up.sql", "SELECT 2;")
	writeStoreFile(t, dir, "001_initial.up.sql", "SELECT 1;")
	writeStoreFile(t, dir, "001_initial.down.sql", "DROP;")
	writeStoreFile(t, dir, "notes.md", "not a migration")
	if err := os.Mkdir(filepath.Join(dir, "999_dir.up.sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := store.ListMigrationFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "001_initial.up.sql"),
		filepath.Join(dir, "002_events.up.sql"),
	}
	if len(files) != len(want) {
		t.Fatalf("got %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("got %v, want %v", files, want)
		}
	}
}

func TestStore_ListMigrationFilesMissingDir(t *testing.T) {
	if _, err := store.ListMigrationFiles(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error for missing directory")
	}
}

func TestStore_RunMigrationsAppliesInOrder(t *testing.T) {
	dir := t.TempDir()
	writeStoreFile(t, dir, "002_b.up.sql", "SELECT 2;")
	writeStoreFile(t, dir, "001_a.up.sql", "SELECT 1;")
	writeStoreFile(t, dir, "001_a.down.sql", "DROP; -- must be ignored")
	writeStoreFile(t, dir, "003_empty.up.sql", "  \n ")
	fake := &fakeStoreDBTX{}
	applied, err := store.RunMigrations(context.Background(), fake, dir)
	if err != nil {
		t.Fatal(err)
	}
	wantApplied := []string{"001_a.up.sql", "002_b.up.sql", "003_empty.up.sql"}
	if len(applied) != len(wantApplied) {
		t.Fatalf("applied = %v, want %v", applied, wantApplied)
	}
	for i := range wantApplied {
		if applied[i] != wantApplied[i] {
			t.Fatalf("applied = %v, want %v", applied, wantApplied)
		}
	}
	if len(fake.execs) != 2 || fake.execs[0] != "SELECT 1;" || fake.execs[1] != "SELECT 2;" {
		t.Fatalf("exec order/content wrong: %q", fake.execs)
	}
}

func TestStore_RunMigrationsMissingDirIsNoop(t *testing.T) {
	fake := &fakeStoreDBTX{}
	applied, err := store.RunMigrations(context.Background(), fake, filepath.Join(t.TempDir(), "nope"))
	if err != nil || applied != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", applied, err)
	}
	if len(fake.execs) != 0 {
		t.Fatalf("expected no execs, got %q", fake.execs)
	}
}

func TestStore_RunMigrationsPropagatesExecError(t *testing.T) {
	dir := t.TempDir()
	writeStoreFile(t, dir, "001_ok.up.sql", "SELECT 1;")
	writeStoreFile(t, dir, "002_bad.up.sql", "BAD SQL;")
	fake := &fakeStoreDBTX{execErr: errors.New("boom"), failSubstr: "BAD"}
	applied, err := store.RunMigrations(context.Background(), fake, dir)
	if err == nil || !strings.Contains(err.Error(), "002_bad.up.sql") {
		t.Fatalf("expected error naming 002_bad.up.sql, got %v", err)
	}
	if len(applied) != 1 || applied[0] != "001_ok.up.sql" {
		t.Fatalf("expected partial applied [001_ok.up.sql], got %v", applied)
	}
}

func TestStore_ParseEmbeddingRoundTrip(t *testing.T) {
	v, err := store.ParseEmbedding("[1,2.5,-3]")
	if err != nil {
		t.Fatal(err)
	}
	got := v.Slice()
	want := []float32{1, 2.5, -3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := store.ParseEmbedding("not-a-vector"); err == nil {
		t.Error("expected error for malformed literal")
	}
	if _, err := store.ParseEmbedding(""); err == nil {
		t.Error("expected error for empty literal")
	}
}
