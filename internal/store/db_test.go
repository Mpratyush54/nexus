// Unit tests for db.go: pool config, migration runner, embedding parser.
// DB-free by design; live-Postgres coverage lives in integration_test.go
// (gated on TEST_POSTGRES_DSN).
package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeMigrationRows is a store.Rows over a list of applied migration base
// names (single TEXT column). It lets RunMigrations exercise its
// skip-applied path without a live Postgres.
type fakeMigrationRows struct {
	names []string
	pos   int
}

func (r *fakeMigrationRows) Next() bool { r.pos++; return r.pos <= len(r.names) }
func (r *fakeMigrationRows) Err() error { return nil }
func (r *fakeMigrationRows) Close()     {}
func (r *fakeMigrationRows) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("fakeMigrationRows: arity mismatch")
	}
	s, ok := dest[0].(*string)
	if !ok {
		return errors.New("fakeMigrationRows: dest must be *string")
	}
	*s = r.names[r.pos-1]
	return nil
}

// fakeStoreDBTX is a DBTX that records Exec/Query calls and simulates the
// schema_migrations tracking table in memory: Query returns the keys of
// applied, and Exec of an INSERT INTO schema_migrations records its $1 arg.
// Query/QueryRow against a real database are covered by integration tests.
type fakeStoreDBTX struct {
	execs      []string
	queries    []string
	applied    map[string]struct{}
	execErr    error
	queryErr   error
	failSubstr string // when set, only statements containing it fail
}

func (f *fakeStoreDBTX) appliedNames() []string {
	names := make([]string, 0, len(f.applied))
	for n := range f.applied {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (f *fakeStoreDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	if f.execErr != nil && (f.failSubstr == "" || strings.Contains(sql, f.failSubstr)) {
		return pgconn.CommandTag{}, f.execErr
	}
	// Simulate the tracking insert: record the migration base name so a
	// second RunMigrations with the same fake observes it via Query.
	if strings.Contains(sql, "INSERT INTO schema_migrations") && len(args) > 0 {
		if name, ok := args[0].(string); ok {
			if f.applied == nil {
				f.applied = make(map[string]struct{})
			}
			f.applied[name] = struct{}{}
		}
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeStoreDBTX) Query(_ context.Context, sql string, _ ...any) (store.Rows, error) {
	f.queries = append(f.queries, sql)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return &fakeMigrationRows{names: f.appliedNames()}, nil
}

func (f *fakeStoreDBTX) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return nil
}

// migrationBodies returns only the Execs that applied migration file bodies
// (excluding the schema_migrations bookkeeping statements).
func migrationBodies(exec []string) []string {
	var out []string
	for _, sql := range exec {
		if strings.Contains(sql, "schema_migrations") {
			continue
		}
		out = append(out, sql)
	}
	return out
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
	// The first Exec must ensure the tracking table; migration bodies then
	// apply in filename order (empty files record without a body Exec).
	if len(fake.execs) == 0 || !strings.Contains(fake.execs[0], "CREATE TABLE IF NOT EXISTS schema_migrations") {
		t.Fatalf("first exec must ensure schema_migrations, got %q", fake.execs)
	}
	if len(fake.queries) != 1 {
		t.Fatalf("expected 1 applied-list query, got %q", fake.queries)
	}
	bodies := migrationBodies(fake.execs)
	if len(bodies) != 2 || bodies[0] != "SELECT 1;" || bodies[1] != "SELECT 2;" {
		t.Fatalf("exec order/content wrong: %q", bodies)
	}
}

func TestStore_RunMigrationsMissingDirIsNoop(t *testing.T) {
	fake := &fakeStoreDBTX{}
	applied, err := store.RunMigrations(context.Background(), fake, filepath.Join(t.TempDir(), "nope"))
	if err != nil || applied != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", applied, err)
	}
	if len(fake.execs) != 0 || len(fake.queries) != 0 {
		t.Fatalf("expected no execs/queries, got execs=%q queries=%q", fake.execs, fake.queries)
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

// TestMigrationDoubleApplyIsNoop proves the boot-twice fix: running the same
// migration dir twice against the same tracking state applies each file once
// and Execs no migration body on the second run.
func TestMigrationDoubleApplyIsNoop(t *testing.T) {
	dir := t.TempDir()
	writeStoreFile(t, dir, "001_a.up.sql", "SELECT 1;")
	writeStoreFile(t, dir, "002_b.up.sql", "SELECT 2;")
	fake := &fakeStoreDBTX{}
	first, err := store.RunMigrations(context.Background(), fake, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0] != "001_a.up.sql" || first[1] != "002_b.up.sql" {
		t.Fatalf("first run applied = %v, want [001_a.up.sql 002_b.up.sql]", first)
	}
	bodiesAfterFirst := len(migrationBodies(fake.execs))
	second, err := store.RunMigrations(context.Background(), fake, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second run must be a no-op, applied = %v", second)
	}
	if got := len(migrationBodies(fake.execs)); got != bodiesAfterFirst {
		t.Fatalf("second run exec'd %d bodies, want %d (no re-apply)", got, bodiesAfterFirst)
	}
}

// TestMigrationSkipApplied proves already-recorded files are skipped while
// new files still apply in order.
func TestMigrationSkipApplied(t *testing.T) {
	dir := t.TempDir()
	writeStoreFile(t, dir, "001_a.up.sql", "SELECT 1;")
	writeStoreFile(t, dir, "002_b.up.sql", "SELECT 2;")
	fake := &fakeStoreDBTX{applied: map[string]struct{}{"001_a.up.sql": {}}}
	applied, err := store.RunMigrations(context.Background(), fake, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "002_b.up.sql" {
		t.Fatalf("applied = %v, want [002_b.up.sql]", applied)
	}
	bodies := migrationBodies(fake.execs)
	if len(bodies) != 1 || bodies[0] != "SELECT 2;" {
		t.Fatalf("only 002 body must exec, got %q", bodies)
	}
}

// TestMigrationSeedRerunIdempotent proves the 004 agents seed is safe to
// re-encounter: its INSERT carries ON CONFLICT DO NOTHING, and the runner
// skips the file on a second pass.
func TestMigrationSeedRerunIdempotent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "004_agents.up.sql"))
	if err != nil {
		t.Fatalf("read real 004 migration: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "ON CONFLICT (name) DO NOTHING") {
		t.Fatalf("004 agents seed must carry ON CONFLICT (name) DO NOTHING for idempotent re-runs")
	}
	if strings.Contains(body, "when present") {
		t.Fatalf("004 header must not claim 002/003 are optional (requires 001+002+003 in order)")
	}
	if !strings.Contains(body, "001 + 002 + 003 in order") {
		t.Fatalf("004 header must state the 001+002+003 ordering requirement")
	}

	dir := t.TempDir()
	writeStoreFile(t, dir, "004_agents.up.sql", body)
	fake := &fakeStoreDBTX{}
	first, err := store.RunMigrations(context.Background(), fake, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0] != "004_agents.up.sql" {
		t.Fatalf("first run applied = %v, want [004_agents.up.sql]", first)
	}
	second, err := store.RunMigrations(context.Background(), fake, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("seed re-run must be a no-op, applied = %v", second)
	}
}

// TestMigrationQueryErrorFailsClosed proves a tracking-read failure aborts
// the run instead of blindly re-applying every file.
func TestMigrationQueryErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeStoreFile(t, dir, "001_a.up.sql", "SELECT 1;")
	fake := &fakeStoreDBTX{queryErr: errors.New("tracking unavailable")}
	applied, err := store.RunMigrations(context.Background(), fake, dir)
	if err == nil || !strings.Contains(err.Error(), "list applied migrations") {
		t.Fatalf("expected tracking-read error, got applied=%v err=%v", applied, err)
	}
	if len(applied) != 0 {
		t.Fatalf("failed run must report no newly applied files, got %v", applied)
	}
	if bodies := migrationBodies(fake.execs); len(bodies) != 0 {
		t.Fatalf("no migration body may exec when tracking is unreadable, got %q", bodies)
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
