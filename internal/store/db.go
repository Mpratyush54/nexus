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

// Ping verifies the database is reachable.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// RunMigrations executes migrations/*.up.sql in lexical order (001, 002, …).
// Files must be idempotent (CREATE IF NOT EXISTS / guarded ALTERs) so a
// half-applied run can simply be retried.
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
	for _, name := range ups {
		sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := s.pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

// ---- small conversion helpers (shared by all PostgresStore methods) ----

// nullText maps "" to NULL so UNIQUE(canonical_url)/UNIQUE(root_commit)
// never collide on empty strings (Postgres treats NULLs as distinct).
func nullText(s string) any {
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
