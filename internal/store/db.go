// Package store provides Postgres data access for central-memory.
//
// This file (issue #2, plan §§1.2, 1.8, 1.9) owns the connection pool: pool
// configuration with tunable limits, health checking, and the ordered
// migration runner over migrations/*.up.sql.
//
// Testability: Store types (ProjectStore, WorkspaceStore) depend on the DBTX
// interface, not on *DB or *pgxpool.Pool, so unit tests run without a live
// Postgres. DB-backed behaviour is covered by integration tests gated on
// TEST_POSTGRES_DSN.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// ErrNotFound is returned (wrapped) when a row addressed by ID does not exist.
var ErrNotFound = errors.New("store: not found")

// Rows is the minimal result-set surface used by this package. It is defined
// in memory.go; pgx rows satisfy it method-for-method, so no adapter is
// needed to pass pgx results around.

// DBTX is the query surface required by the store layer. *DB implements it
// by delegating to its pool; tests substitute fakes without a database.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config tunes the Postgres connection pool.
type Config struct {
	// DSN is the Postgres connection string (required).
	DSN string
	// MaxConns caps total connections (pool exhaustion surfaces as
	// acquire-timeout errors instead of DB overload).
	MaxConns int32
	// MinConns keeps idle connections warm to avoid connect latency on bursts.
	MinConns int32
	// MaxConnLifetime bounds connection age so load balancers / Aurora
	// failovers rotate connections out. Must exceed the longest expected
	// single-connection hold (WebSocket handlers hold a connection per
	// message round-trip, not for the socket lifetime — see ADR-002).
	MaxConnLifetime time.Duration
	// MaxConnIdleTime reaps connections idle longer than this.
	MaxConnIdleTime time.Duration
	// HealthCheckPeriod interval for background pool health checks.
	HealthCheckPeriod time.Duration
	// ConnectTimeout caps a single new-connection attempt.
	ConnectTimeout time.Duration
}

// DefaultConfig returns production-sane pool defaults for the Aurora
// Serverless v2 backend: a small warm pool that scales under burst without
// overwhelming a scaled-to-zero cluster on wake-up.
func DefaultConfig(dsn string) Config {
	return Config{
		DSN:               dsn,
		MaxConns:          16,
		MinConns:          2,
		MaxConnLifetime:   30 * time.Minute,
		MaxConnIdleTime:   5 * time.Minute,
		HealthCheckPeriod: 1 * time.Minute,
		ConnectTimeout:    10 * time.Second,
	}
}

// DB owns a pgxpool.Pool. Construct with Connect; it is safe for concurrent use.
type DB struct {
	pool *pgxpool.Pool
	cfg  Config
}

// Compile-time proofs: *DB satisfies the store seam and memory.go's Querier.
var (
	_ DBTX    = (*DB)(nil)
	_ Querier = (*DB)(nil)
)

// Connect opens the pool and fails fast when the database is unreachable.
func Connect(ctx context.Context, cfg Config) (*DB, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("store: connect requires a non-empty DSN")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("store: parse DSN: %w", err)
	}
	// Apply only explicitly set limits so a zero Config keeps pgx defaults.
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckPeriod > 0 {
		poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	}
	if cfg.ConnectTimeout > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	db := &DB{pool: pool, cfg: cfg}
	if err := db.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// Ping verifies a live connection can be acquired.
func (db *DB) Ping(ctx context.Context) error {
	if err := db.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Health proves the full query path (acquire + round-trip + release).
func (db *DB) Health(ctx context.Context) error {
	if err := db.Ping(ctx); err != nil {
		return err
	}
	var one int
	if err := db.pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		return fmt.Errorf("store: health query: %w", err)
	}
	return nil
}

// Close releases all pooled connections.
func (db *DB) Close() {
	db.pool.Close()
}

// Stat exposes pool telemetry (total/acquired/idle connections).
func (db *DB) Stat() *pgxpool.Stat {
	return db.pool.Stat()
}

// Exec delegates to the pool.
func (db *DB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return db.pool.Exec(ctx, sql, arguments...)
}

// Query delegates to the pool. The pgx rows returned satisfy the narrow Rows
// interface, which is also what makes *DB a valid memory.go Querier.
func (db *DB) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return db.pool.Query(ctx, sql, args...)
}

// QueryRow delegates to the pool.
func (db *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return db.pool.QueryRow(ctx, sql, args...)
}

// ParseEmbedding parses a pgvector text literal ("[0.1,0.2,...]") into a
// pgvector-go Vector. It claims the pgvector-go dependency for the store
// package: memory.go formats embeddings as text for the query path, and this
// is the symmetric read-path parser for future vector-column reads
// (episodes/memory detail views).
func ParseEmbedding(s string) (pgvector.Vector, error) {
	var v pgvector.Vector
	if err := v.Parse(s); err != nil {
		return pgvector.Vector{}, fmt.Errorf("store: parse embedding: %w", err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Migrations
// ---------------------------------------------------------------------------

// migrationSuffix marks forward migration files owned by the migrations/
// agent. Down migrations (*.down.sql) are never applied by this runner.
const migrationSuffix = ".up.sql"

// ListMigrationFiles returns the sorted forward-migration paths in dir.
// Sorting by filename gives deterministic apply order (001_*, 002_*, ...).
// It is pure filesystem logic — unit-testable without a database.
func ListMigrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("store: list migrations in %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), migrationSuffix) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// RunMigrations applies every migrations/*.up.sql file in filename order and
// returns the applied base names. A missing directory is a no-op (nil, nil):
// migrations are owned by another agent and may not exist yet. Each file may
// contain multiple statements; pgx Exec runs the file body as one unit.
// There is no down-migration support by design — rollback is forward-only via
// new migrations (Aurora Serverless DDL is transactional per file here).
func RunMigrations(ctx context.Context, db DBTX, dir string) ([]string, error) {
	files, err := ListMigrationFiles(dir)
	if err != nil {
		// errors.Is (not os.IsNotExist): the %w-wrapped *PathError needs
		// full chain traversal, which os.IsNotExist does not perform.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var applied []string
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			return applied, fmt.Errorf("store: read migration %s: %w", filepath.Base(f), err)
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			applied = append(applied, filepath.Base(f))
			continue
		}
		if _, err := db.Exec(ctx, string(body)); err != nil {
			return applied, fmt.Errorf("store: apply migration %s: %w", filepath.Base(f), err)
		}
		applied = append(applied, filepath.Base(f))
	}
	return applied, nil
}
