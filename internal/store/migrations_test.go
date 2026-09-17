package store

// Tests for versioned RunMigrations (nexus issue #2 review fix):
//   - filename -> version parsing (pure, no DB)
//   - pending/skip/idempotency logic (pure, no DB)
//   - live Postgres integration guarded by DATABASE_URL (skipped in CI
//     without a database): record-once, skip-on-rerun, backward compat
//     with pre-existing tables applied without tracking.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationVersion(t *testing.T) {
	cases := map[string]string{
		"001_initial.up.sql":  "001_initial",
		"002_events.up.sql":   "002_events",
		"005_branches.up.sql": "005_branches",
		"010_anything.up.sql": "010_anything",
	}
	for file, want := range cases {
		if got := migrationVersion(file); got != want {
			t.Errorf("migrationVersion(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestFilterPendingMigrations(t *testing.T) {
	all := []string{"001_initial.up.sql", "002_events.up.sql", "003_sessions.up.sql"}

	// Nothing applied -> everything pending, order preserved.
	if got := filterPendingMigrations(all, map[string]bool{}); len(got) != 3 {
		t.Fatalf("empty applied: got %v, want all 3", got)
	}

	// Partial -> only unapplied remain.
	applied := map[string]bool{"001_initial": true, "002_events": true}
	pending := filterPendingMigrations(all, applied)
	if len(pending) != 1 || pending[0] != "003_sessions.up.sql" {
		t.Fatalf("partial applied: got %v, want [003_sessions.up.sql]", pending)
	}

	// Idempotency: recording the pending version makes the next run empty.
	applied[migrationVersion(pending[0])] = true
	if again := filterPendingMigrations(all, applied); len(again) != 0 {
		t.Fatalf("after record: got %v, want empty (idempotent skip)", again)
	}

	// Unknown versions in the applied set do not affect pending.
	weird := map[string]bool{"999_unknown": true}
	if got := filterPendingMigrations(all, weird); len(got) != 3 {
		t.Fatalf("unknown applied: got %v, want all 3", got)
	}
}

// TestRunMigrationsRecordsAndSkips is an integration test. It runs only
// when DATABASE_URL is set; otherwise it skips so `go test ./...` stays
// green without a database.
func TestRunMigrationsRecordsAndSkips(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping live migration test")
	}
	ctx := context.Background()

	dir := t.TempDir()
	mig001 := `CREATE TABLE IF NOT EXISTS migtest_widgets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
INSERT INTO migtest_widgets (id, name) VALUES ('w1', 'first')
ON CONFLICT (id) DO NOTHING;
`
	mig002 := `CREATE TABLE IF NOT EXISTS migtest_gadgets (
    id TEXT PRIMARY KEY
);
INSERT INTO migtest_gadgets (id) VALUES ('g1')
ON CONFLICT (id) DO NOTHING;
`
	if err := os.WriteFile(filepath.Join(dir, "001_widgets.up.sql"), []byte(mig001), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "002_gadgets.up.sql"), []byte(mig002), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to DATABASE_URL: %v", err)
	}
	defer s.Close()

	// Clean slate for repeatable runs.
	_, _ = s.pool.Exec(ctx, `DROP TABLE IF EXISTS migtest_gadgets`)
	_, _ = s.pool.Exec(ctx, `DROP TABLE IF EXISTS migtest_widgets`)
	_, _ = s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version IN ('001_widgets','002_gadgets')`)

	// First run applies both.
	if err := s.RunMigrations(ctx, dir); err != nil {
		t.Fatalf("first RunMigrations: %v", err)
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version IN ('001_widgets','002_gadgets')`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 2 {
		t.Fatalf("recorded versions = %d, want 2", count)
	}

	// Second run must skip both (idempotent, no error, no duplicate rows).
	if err := s.RunMigrations(ctx, dir); err != nil {
		t.Fatalf("second RunMigrations (skip path): %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version IN ('001_widgets','002_gadgets')`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations after rerun: %v", err)
	}
	if count != 2 {
		t.Fatalf("after rerun recorded versions = %d, want 2 (no duplicates)", count)
	}

	// Backward compat: simulate a DB where the tables exist but tracking
	// was wiped (pre-versioning state). Re-running must re-apply safely
	// (all statements idempotent) and re-record.
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version IN ('001_widgets','002_gadgets')`); err != nil {
		t.Fatal(err)
	}
	if err := s.RunMigrations(ctx, dir); err != nil {
		t.Fatalf("backward-compat rerun after tracking wipe: %v", err)
	}

	_, _ = s.pool.Exec(ctx, `DROP TABLE IF EXISTS migtest_gadgets`)
	_, _ = s.pool.Exec(ctx, `DROP TABLE IF EXISTS migtest_widgets`)
	_, _ = s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version IN ('001_widgets','002_gadgets')`)
}
