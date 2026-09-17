// Command server-bootstrap is the production entrypoint for the central API
// server container (issue #20, plan "AWS Infrastructure" + §1.8).
//
// It performs migration-on-boot and then serves:
//
//  1. Read config from the environment (values are injected by ECS from AWS
//     Secrets Manager — see infra/terraform/ecs.tf; nothing secret is
//     hardcoded or baked into the image).
//  2. Connect to Aurora Serverless v2 (PostgreSQL 16 + pgvector) with retry,
//     because a scaled-to-zero cluster needs time to wake on first connect.
//  3. Run migrations/*.up.sql in filename order via store.RunMigrations
//     (forward-only; 001_initial creates the pgcrypto + vector extensions).
//  4. Serve the REST API from internal/server on $PORT with /healthz
//     (liveness, open) and /readyz (open; pings the DB pool).
//
// Data-route persistence: server.New requires a server.Store. The full
// Postgres-backed adapter (memory/episode vector search wiring) is a
// follow-up owned by the store/server issues, so this bootstrap ships a
// stubStore that fails closed (HTTP 500 with a "pending" message) on data
// routes. /auth/login, /healthz and /readyz work; nothing silently pretends
// to persist. See ADR-020 for rationale.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"central-memory/internal/server"
	"central-memory/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Data-route persistence (issue #40 wiring the issue #37 adapter): the
// Postgres-backed server.Store is server.NewPostgresStore(db) — *store.DB
// satisfies store.DBTX, so the migrated pool backs every data route. The
// stubStore that used to fail closed here is gone; see ADR-040.

// getenv returns env key or fallback when unset/blank.
func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// databaseURL resolves the Postgres DSN. DATABASE_URL wins when set;
// otherwise the discrete DB_* parts are composed (this is the shape ECS
// injects from Secrets Manager: username/password come from the DB secret,
// host/port/name from Terraform outputs — never hardcoded).
func databaseURL() (string, error) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn, nil
	}
	host := strings.TrimSpace(os.Getenv("DB_HOST"))
	user := strings.TrimSpace(os.Getenv("DB_USER"))
	pass := os.Getenv("DB_PASSWORD")
	name := getenv("DB_NAME", "central_memory")
	port := getenv("DB_PORT", "5432")
	sslmode := getenv("DB_SSLMODE", "require")
	missing := []string{}
	if host == "" {
		missing = append(missing, "DB_HOST")
	}
	if user == "" {
		missing = append(missing, "DB_USER")
	}
	if pass == "" {
		missing = append(missing, "DB_PASSWORD")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("server-bootstrap: set DATABASE_URL or %s", strings.Join(missing, ", "))
	}
	// Password is interpolated, not logged. Special chars in generated
	// passwords are URL-safe per Terraform's random_password override set.
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		user, pass, host, port, name, sslmode), nil
}

// connectWithRetry dials Postgres until it answers or the deadline passes.
// Aurora Serverless v2 resumes from 0.5 ACU on first connection, which can
// take tens of seconds — fail-open retry here, fail-closed after.
func connectWithRetry(ctx context.Context, dsn string, deadline time.Duration) (*store.DB, error) {
	cfg := store.DefaultConfig(dsn)
	deadlineCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	attempt := 0
	for {
		attempt++
		db, err := store.Connect(deadlineCtx, cfg)
		if err == nil {
			log.Printf("server-bootstrap: connected to postgres (attempt %d)", attempt)
			return db, nil
		}
		if deadlineCtx.Err() != nil {
			return nil, fmt.Errorf("server-bootstrap: connect deadline exceeded after %d attempts: %w", attempt, err)
		}
		log.Printf("server-bootstrap: connect attempt %d failed, retrying in 3s: %v", attempt, err)
		select {
		case <-deadlineCtx.Done():
			return nil, fmt.Errorf("server-bootstrap: connect deadline exceeded after %d attempts", attempt)
		case <-time.After(3 * time.Second):
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("server-bootstrap: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	port := getenv("PORT", "8080")
	migrationsDir := getenv("MIGRATIONS_DIR", "/app/migrations")
	jwtSecret := os.Getenv("JWT_SECRET")
	if len(jwtSecret) < 32 {
		return errors.New("JWT_SECRET must be set with at least 32 characters (injected from Secrets Manager; see infra/terraform/secrets.tf)")
	}

	dsn, err := databaseURL()
	if err != nil {
		return err
	}

	// Boot step 1: connect (retry covers Aurora scale-from-zero wake-up).
	db, err := connectWithRetry(ctx, dsn, 3*time.Minute)
	if err != nil {
		return err
	}
	defer db.Close()

	// Boot step 2: migrate before serving a single request.
	applied, err := store.RunMigrations(ctx, db, migrationsDir)
	if err != nil {
		return fmt.Errorf("apply migrations from %s: %w", migrationsDir, err)
	}
	log.Printf("server-bootstrap: migrations applied=%d dir=%s files=%v", len(applied), migrationsDir, applied)

	// Boot step 3: serve. Data routes are backed by the issue #37
	// PostgresStore over the migrated pool; /healthz + /readyz are fully
	// live. WebSocket (/ws) and the static dashboard are enabled here
	// (issue #40); the store → hub event bridge runs as a goroutine below.
	srv := server.New(server.NewPostgresStore(db), server.Options{JWTSecret: []byte(jwtSecret)})
	srv.EnableWS()
	mux := http.NewServeMux()
	srv.RegisterWebRoutes(mux)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Health(r.Context()); err != nil {
			http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"migrations_applied":%d}`, len(applied))
	})
	mux.Handle("/", srv.Handler())

	// Boot step 4: bridge store.Subscribe (LISTEN/NOTIFY) → hub fan-out
	// (issue #40). Subscribe needs a *pgxpool.Pool, but *store.DB keeps its
	// pool private with no accessor (ownership constraint — see ADR-040), so
	// the bridge opens a small dedicated pool from the same DSN: one
	// connection is held by LISTEN, the second covers reconnect overlap.
	bridgePoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse bridge pool DSN: %w", err)
	}
	bridgePoolCfg.MaxConns = 2
	bridgePoolCfg.MinConns = 1
	bridgePool, err := pgxpool.NewWithConfig(ctx, bridgePoolCfg)
	if err != nil {
		return fmt.Errorf("open bridge pool: %w", err)
	}
	defer bridgePool.Close()
	go func() {
		if berr := server.BridgeEvents(ctx, bridgePool, srv.Hub()); berr != nil {
			log.Printf("server-bootstrap: ws bridge ended: %v", berr)
		}
	}()

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shCtx)
	}()
	log.Printf("server-bootstrap: listening on :%s (healthz, readyz, ws, web, data routes live)", port)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
