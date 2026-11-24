package store

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

var (
	embeddedPostgresOnce sync.Once
	embeddedPostgresDB   *embeddedpostgres.EmbeddedPostgres
	embeddedPostgresDSN  string
	embeddedPostgresErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if embeddedPostgresDB != nil {
		_ = embeddedPostgresDB.Stop()
	}
	os.Exit(code)
}

func testPostgresDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	if testing.Short() {
		t.Skip("short mode: skipping embedded postgres")
	}
	embeddedPostgresOnce.Do(startEmbeddedPostgres)
	if embeddedPostgresErr != nil {
		t.Skipf("embedded postgres unavailable: %v", embeddedPostgresErr)
	}
	return embeddedPostgresDSN
}

func startEmbeddedPostgres() {
	port, err := freePort()
	if err != nil {
		embeddedPostgresErr = err
		return
	}
	runtimeDir, err := os.MkdirTemp("", "nexus-pg-runtime-*")
	if err != nil {
		embeddedPostgresErr = err
		return
	}
	dataDir, err := os.MkdirTemp("", "nexus-pg-data-*")
	if err != nil {
		embeddedPostgresErr = err
		return
	}
	config := embeddedpostgres.DefaultConfig().
		Username("postgres").
		Password("postgres").
		Database("postgres").
		Port(uint32(port)).
		RuntimePath(runtimeDir).
		DataPath(dataDir).
		CachePath(filepath.Join(os.TempDir(), "nexus-embedded-postgres-cache")).
		StartTimeout(120 * time.Second).
		Logger(io.Discard)
	embeddedPostgresDB = embeddedpostgres.NewDatabase(config)
	if err := embeddedPostgresDB.Start(); err != nil {
		embeddedPostgresErr = err
		return
	}
	embeddedPostgresDSN = fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	if err := migrateAndSeedEmbeddedPostgres(embeddedPostgresDSN); err != nil {
		embeddedPostgresErr = err
	}
}

func migrateAndSeedEmbeddedPostgres(dsn string) error {
	ctx := context.Background()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer s.Close()
	migrationsDir, err := embeddedMigrationsDir()
	if err != nil {
		return err
	}
	if err := s.RunMigrations(ctx, migrationsDir); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO users (username) VALUES ('pg-test-owner'), ('pg-test-other')
		ON CONFLICT (username) DO NOTHING`); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO projects (folder_name, display_name, created_by)
		SELECT 'pg-test-project', 'PG Test Project', id FROM users
		WHERE username = 'pg-test-owner'
		  AND NOT EXISTS (SELECT 1 FROM projects WHERE folder_name = 'pg-test-project')`)
	return err
}

func embeddedMigrationsDir() (string, error) {
	src, err := filepath.Abs("../../migrations")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "nexus-migrations-*")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return "", err
	}
	dropVectorIndex := regexp.MustCompile(`(?m)^.*USING ivfflat.*\n`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			return "", err
		}
		if entry.Name() == "001_initial.up.sql" {
			text := string(body)
			text = strings.ReplaceAll(text, `CREATE EXTENSION IF NOT EXISTS "vector";`, "")
			text = strings.ReplaceAll(text, "vector(1536)", "TEXT")
			text = dropVectorIndex.ReplaceAllString(text, "")
			body = []byte(text)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name()), body, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
