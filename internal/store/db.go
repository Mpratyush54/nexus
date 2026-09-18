package store

// db.go — Postgres connection pool + migration runner (issue #2).
//
// PostgresStore (methods split across projects.go, workspaces.go, memory.go,
// episodes.go, events.go) implements Store over a pgxpool.Pool. MemStore stays
// dependency-free in spirit: it never touches this pool, so unit tests and
// local dev work with no database at all — only code paths that call
// NewPostgresStore need a live Postgres.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
)

// PostgresStore is the production Store backed by Aurora/Postgres + pgvector.
type PostgresStore struct {
	pool *pgxpool.Pool
	dsn  string // retained so Subscribe can open a dedicated LISTEN connection
}

// Compile-time guarantee that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore opens a pooled connection to Postgres and verifies it.
// dsn example: "postgres://user:pass@localhost:5432/central_memory?sslmode=disable".
//
// Pool sizing rationale: the daemon fleet is small (one pool per daemon +
// server); 20 conns cap with 30m max lifetime avoids the failure mode in our
// own episode history where short-lived conns were reaped mid-WebSocket-use.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresStore{pool: pool, dsn: dsn}, nil
}

// Close drains the pool. MemStore needs no equivalent (GC handles it).
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// DB exposes the pool as a DBTX for seam stores (UserStore, MemoryStore)
// that compose over any connection source (issue #37: server wiring).
func (s *PostgresStore) DB() DBTX {
	return poolDBTX{pool: s.pool}
}

// pgxRowsAdapter bridges pgx.Rows to the narrow Rows interface: a struct
// value cannot satisfy an interface its pointer methods... here all methods
// are value-receiver compatible, so a thin wrapper suffices.
type pgxRowsAdapter struct {
	rows pgx.Rows
}

func (r pgxRowsAdapter) Next() bool             { return r.rows.Next() }
func (r pgxRowsAdapter) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r pgxRowsAdapter) Err() error             { return r.rows.Err() }
func (r pgxRowsAdapter) Close()                 { r.rows.Close() }

// poolDBTX adapts *pgxpool.Pool to DBTX (issue #37): the pool's Query
// returns concrete pgx.Rows, which cannot satisfy the Rows interface, so
// writes go straight through while reads wrap. Transactions (*pgx.Tx)
// satisfy DBTX natively and bypass this adapter.
type poolDBTX struct {
	pool *pgxpool.Pool
}

func (p poolDBTX) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return p.pool.Exec(ctx, sql, arguments...)
}

func (p poolDBTX) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgxRowsAdapter{rows: rows}, nil
}

func (p poolDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.pool.QueryRow(ctx, sql, args...)
}

// Ping verifies the database is reachable.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// schemaMigrationsDDL tracks which migration versions have been applied.
// version is the migration filename without the ".up.sql" suffix
// (e.g. "001_initial" for "001_initial.up.sql").
const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// migrationVersion maps a migration filename to its tracking version.
// "001_initial.up.sql" -> "001_initial". Non-.up.sql names are returned
// unchanged so callers can spot misuse in tests.
func migrationVersion(filename string) string {
	return strings.TrimSuffix(filename, ".up.sql")
}

// filterPendingMigrations returns the subset of sorted *.up.sql filenames
// whose version is not in applied. Pure (no I/O) so unit tests cover the
// skip/record logic without a live Postgres.
func filterPendingMigrations(sortedUps []string, applied map[string]bool) []string {
	var pending []string
	for _, name := range sortedUps {
		if !applied[migrationVersion(name)] {
			pending = append(pending, name)
		}
	}
	return pending
}

// RunMigrations executes pending migrations/*.up.sql in lexical order,
// skipping versions already recorded in schema_migrations.
//
// Backward compatibility: databases created before version tracking have
// 001-005 applied but no schema_migrations rows. On first run with this
// code the table is created empty, every file looks pending, and each file
// is re-executed — safe because all current migrations are idempotent
// (CREATE TABLE/INDEX IF NOT EXISTS, guarded DO-block ALTERs,
// INSERT ... ON CONFLICT DO NOTHING), then recorded. Subsequent launches
// skip them via the version check and only execute the file once.
//
// Each migration runs in its own transaction (body + version INSERT commit
// atomically), so a half-applied file is retried cleanly on next launch.
// Files must stay transaction-safe: no CREATE INDEX CONCURRENTLY or other
// non-transactional DDL (split such migrations out if ever needed).
//
// Concurrency (issue #107): replicas booting together serialize on a
// session-level PostgreSQL advisory lock for the whole run, so only one
// runner applies migrations at a time; the others block, then observe the
// recorded versions and skip.
func (s *PostgresStore) RunMigrations(ctx context.Context, migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	var ups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)

	// Serialize concurrent runners (issue #107, #131). pg_advisory_lock is
	// session-level, so the lock, the version table work, and the unlock
	// MUST all run on one dedicated connection: pool.Exec would route
	// lock/unlock to arbitrary connections, leaking the lock and failing
	// to serialize. Acquire a conn for the whole run instead.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('central-memory-migrations'))`); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('central-memory-migrations'))`)
	}()

	if _, err := conn.Exec(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := make(map[string]bool, len(ups))
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}

	for _, name := range filterPendingMigrations(ups, applied) {
		version := migrationVersion(name)
		sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
		applied[version] = true
	}
	return nil
}

// ---- small conversion helpers (shared by all PostgresStore methods) ----

// Rows is the minimal result-set surface stores need (pgx.Rows satisfies
// it; tests substitute scripted fakes). Issue #34.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// DBTX is the query surface required by the users/tasks/watched_files
// stores (issue #34). *pgxpool.Pool satisfies it, so production code passes
// the pool (or a transaction) while unit tests substitute fakes.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Compile-time proof that transactions also satisfy the store seam is left
// to call sites: *pgxpool.Pool's Query returns pgx.Rows (a struct value),
// which cannot satisfy the Rows interface, so pool wiring goes through a
// thin adapter or *pgx.Tx where needed.

// nullText maps "" to NULL so UNIQUE(canonical_url)/UNIQUE(root_commit)
// never collide on empty strings (Postgres treats NULLs as distinct).
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullUUID maps "" to NULL for nullable UUID columns (issue #34).
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// encodeEmbedding renders a vector for a `$N::vector` parameter.
// Empty means "no embedding yet" -> NULL (row still searchable by text).
func encodeEmbedding(vec []float32) any {
	if len(vec) == 0 {
		return nil
	}
	return pgvector.NewVector(vec).String()
}

// parseEmbedding decodes an `embedding::text` column back to []float32.
// NULL/empty/unparseable -> nil (caller treats as "no embedding").
func parseEmbedding(s *string) []float32 {
	if s == nil || *s == "" {
		return nil
	}
	var v pgvector.Vector
	if err := v.Parse(*s); err != nil {
		return nil
	}
	return v.Slice()
}

// marshalPayload encodes an event payload; nil -> '{}' (column is NOT NULL).
func marshalPayload(p map[string]any) []byte {
	if p == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// unmarshalPayload decodes a jsonb column; never returns nil.
func unmarshalPayload(b []byte) map[string]any {
	out := make(map[string]any)
	if len(b) == 0 {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return make(map[string]any)
	}
	return out
}
