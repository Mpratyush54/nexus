// Command server — local-dev entrypoint for the central API server
// (issue #39, plan target tree).
//
// Env-configured, thin wiring only. Canonical env contract (single source of
// truth: deploy/secrets-notes.md + docs/decisions/ADR-045-deploy-env-contract):
//
//	DATABASE_URL                        primary Postgres DSN (migrate.sh + ECS prod)
//	DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD + DB_SSLMODE
//	                                    fallback: assembled into a DSN when
//	                                    DATABASE_URL is unset (ECS discrete secrets;
//	                                    DB_SSLMODE=require in prod, disable only for local dev)
//	JWT_SECRET                          canonical signing key (CENTRAL_MEMORY_JWT_KEY
//	                                    accepted as a legacy fallback)
//	PORT (default 8080), MIGRATIONS_DIR (default /migrations)
//	CENTRAL_MEMORY_LOCAL_DEV=1          local bypass for the /readyz sslmode=require
//	                                    gate (compose sets this with sslmode=disable)
//
// Boot-migration bootstrap is deploy/migrate.sh (the container ENTRYPOINT),
// not a Go bootstrap binary: it assembles the same DSN from the same env and
// applies migrations/*.up.sql before exec'ing this binary. /healthz is
// liveness (no auth, no DB); /readyz is readiness (config + sslmode gate).
//
// Data-route persistence: server.New requires a server.Store and the
// Postgres-backed adapter is a follow-up owned by the store/server issues,
// so this entrypoint ships a stubStore that fails closed (HTTP 500) on data
// routes. /auth/login fails closed too (no users table yet); /healthz and
// /readyz are live. Nothing silently pretends to persist.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"central-memory/internal/server"
	"central-memory/internal/store"
)

// errStorePending fails closed on every data route until the Postgres-backed
// server.Store adapter lands.
var errStorePending = errors.New("central-memory: postgres store adapter pending (local cmd/server ships boot+serve; data routes land with the adapter follow-up)")

// stubStore satisfies server.Store with explicit failures. It exists only so
// the local server boots and serves health probes today.
type stubStore struct{}

func (stubStore) ResolveProject(ctx context.Context, canonicalURL, rootCommit, folderName string) (*store.Project, error) {
	return nil, errStorePending
}
func (stubStore) GetProject(ctx context.Context, id string) (*store.Project, error) {
	return nil, errStorePending
}
func (stubStore) RegisterWorkspace(ctx context.Context, ws *store.Workspace) error {
	return errStorePending
}
func (stubStore) Heartbeat(ctx context.Context, workspaceID, branch, commitSHA string, isDirty bool) error {
	return errStorePending
}
func (stubStore) GetActiveWorkspace(ctx context.Context, projectID string) (*store.Workspace, error) {
	return nil, errStorePending
}
func (stubStore) CreateMemoryItem(ctx context.Context, item *store.MemoryItem) error {
	return errStorePending
}
func (stubStore) GetMemoryItem(ctx context.Context, id string) (*store.MemoryItem, error) {
	return nil, errStorePending
}
func (stubStore) SearchMemory(ctx context.Context, projectID, query string, tags []string, limit int) ([]*store.MemoryItem, error) {
	return nil, errStorePending
}
func (stubStore) ConfirmMemory(ctx context.Context, id, confirmedBy string) error {
	return errStorePending
}
func (stubStore) CreateEpisode(ctx context.Context, ep *store.Episode) error {
	return errStorePending
}
func (stubStore) GetEpisode(ctx context.Context, id string) (*store.Episode, error) {
	return nil, errStorePending
}
func (stubStore) SearchEpisodes(ctx context.Context, projectID, errorPattern, query string, limit int) ([]*store.Episode, error) {
	return nil, errStorePending
}
func (stubStore) ResolveEpisode(ctx context.Context, id, resolution, verification, resolvedBy string) error {
	return errStorePending
}
func (stubStore) AppendEvent(ctx context.Context, ev *store.Event) error {
	return errStorePending
}
func (stubStore) ListEvents(ctx context.Context, projectID string, sinceID int64, limit int) ([]*store.Event, error) {
	return nil, errStorePending
}
func (stubStore) Subscribe(ctx context.Context, projectID string) (<-chan *store.Event, func(), error) {
	return nil, nil, errStorePending
}

// Compile-time proof the stub satisfies the server seam.
var _ store.Store = stubStore{}

// serverConfig is the resolved boot configuration. resolveConfig prefers
// DATABASE_URL and assembles a DSN from discrete DB_* parts otherwise, so
// the Go entrypoint and deploy/migrate.sh agree by construction.
type serverConfig struct {
	port           string
	databaseURL    string
	databaseSource string // "DATABASE_URL" | "DB_* (assembled)" | ""
	sslMode        string
	jwtSecret      string
	jwtSource      string // "JWT_SECRET" | "CENTRAL_MEMORY_JWT_KEY" | "dev-default"
	migrationsDir  string
	localDev       bool
}

// resolveJWTSecret returns the signing key. JWT_SECRET is canonical;
// CENTRAL_MEMORY_JWT_KEY is a legacy fallback (internal/server/auth.go reads
// it); empty means the insecure dev default (local use only).
func resolveJWTSecret(get func(string) string) (secret, source string) {
	if v := strings.TrimSpace(get("JWT_SECRET")); v != "" {
		return v, "JWT_SECRET"
	}
	if v := strings.TrimSpace(get("CENTRAL_MEMORY_JWT_KEY")); v != "" {
		return v, "CENTRAL_MEMORY_JWT_KEY"
	}
	return "central-memory-dev-secret-do-not-use-in-prod", "dev-default"
}

// buildDatabaseURLFromParts assembles a Postgres DSN from discrete parts.
// sslMode defaults to "require" (prod); pass "disable" only for local dev.
// Returns "" when the required parts are absent.
func buildDatabaseURLFromParts(host, port, name, user, password, sslMode string) string {
	host = strings.TrimSpace(host)
	name = strings.TrimSpace(name)
	user = strings.TrimSpace(user)
	if host == "" || name == "" || user == "" {
		return ""
	}
	if strings.TrimSpace(port) == "" {
		port = "5432"
	}
	if strings.TrimSpace(sslMode) == "" {
		sslMode = "require"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   host + ":" + strings.TrimSpace(port),
		Path:   "/" + name,
	}
	q := u.Query()
	q.Set("sslmode", strings.TrimSpace(sslMode))
	u.RawQuery = q.Encode()
	return u.String()
}

// resolveDatabaseURL returns the effective DSN: DATABASE_URL verbatim, else
// assembled from DB_* parts. The second return is the source label.
func resolveDatabaseURL(get func(string) string) (dsn, source string) {
	if v := strings.TrimSpace(get("DATABASE_URL")); v != "" {
		return v, "DATABASE_URL"
	}
	dsn = buildDatabaseURLFromParts(
		get("DB_HOST"), get("DB_PORT"), get("DB_NAME"),
		get("DB_USER"), get("DB_PASSWORD"), get("DB_SSLMODE"),
	)
	if dsn != "" {
		return dsn, "DB_* (assembled)"
	}
	return "", ""
}

// sslModeOf extracts ?sslmode= from a DSN ("" when absent/unparseable).
func sslModeOf(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("sslmode"))
}

// resolveConfig reads the process environment into a serverConfig.
func resolveConfig() serverConfig {
	get := os.Getenv
	dsn, src := resolveDatabaseURL(get)
	secret, jwtSrc := resolveJWTSecret(get)
	migrationsDir := strings.TrimSpace(get("MIGRATIONS_DIR"))
	if migrationsDir == "" {
		migrationsDir = "/migrations"
	}
	return serverConfig{
		port:           getenv("PORT", "8080"),
		databaseURL:    dsn,
		databaseSource: src,
		sslMode:        sslModeOf(dsn),
		jwtSecret:      secret,
		jwtSource:      jwtSrc,
		migrationsDir:  migrationsDir,
		localDev:       strings.TrimSpace(get("CENTRAL_MEMORY_LOCAL_DEV")) == "1",
	}
}

// readyStatus evaluates readiness: a DSN must be configured and, unless this
// is an explicit local-dev boot, it must pin sslmode=require (prod Aurora
// runs with rds.force_ssl=1). Pure over cfg (+ a migrations-dir existence
// probe by the caller) so tests cover the gate without I/O.
func readyStatus(cfg serverConfig, migrationsDirOK bool) (ok bool, reason string) {
	if cfg.databaseURL == "" {
		return false, "DATABASE_URL (or DB_* parts) is not configured"
	}
	if !cfg.localDev && !strings.EqualFold(cfg.sslMode, "require") {
		return false, "DSN sslmode must be require in non-local boots (set CENTRAL_MEMORY_LOCAL_DEV=1 for local dev with sslmode=disable)"
	}
	if !migrationsDirOK {
		return false, "MIGRATIONS_DIR not found: " + cfg.migrationsDir
	}
	return true, "ready"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newHandler(secret string, cfg serverConfig) http.Handler {
	srv := server.NewServer(stubStore{})
	srv.Auth = server.NewAuthenticator([]byte(secret))
	srv.AttachHub(server.NewHub())
	// NOTE (#40): the store→hub bridge (Server.StartBridge) starts once the
	// Postgres adapter lands (#37). stubStore has no projects to bridge, so
	// launching it here would only error-loop.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503,"message":"authentication unavailable: user store adapter pending"}}`))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		dirOK := true
		if fi, err := os.Stat(cfg.migrationsDir); err != nil || !fi.IsDir() {
			// Local `go test` runs have no /migrations dir; only enforce when
			// a DSN is configured (container boots) or the dir was explicitly set.
			if cfg.databaseURL != "" || strings.TrimSpace(os.Getenv("MIGRATIONS_DIR")) != "" {
				dirOK = false
			}
		}
		ok, reason := readyStatus(cfg, dirOK)
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{
			"ok":                ok,
			"reason":            reason,
			"database_source":   cfg.databaseSource,
			"sslmode":           cfg.sslMode,
			"migrations_dir":    cfg.migrationsDir,
			"migrations_dir_ok": dirOK,
			"store":             "stub (fail-closed data routes pending adapter)",
		})
	})
	mux.Handle("/", srv.Handler())
	return mux
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := resolveConfig()
	if cfg.jwtSource == "dev-default" {
		log.Print("server: JWT_SECRET unset — using insecure dev default (local use only)")
	} else {
		log.Printf("server: JWT key from %s", cfg.jwtSource)
	}
	if cfg.databaseSource == "" {
		log.Print("server: no DATABASE_URL/DB_* configured — /readyz will report not-ready; data routes fail closed")
	} else {
		log.Printf("server: database from %s (sslmode=%q)", cfg.databaseSource, cfg.sslMode)
	}

	httpSrv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           newHandler(cfg.jwtSecret, cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shCtx)
	}()
	log.Printf("server: listening on :%s (healthz open; readyz gated; data routes fail closed pending store adapter)", cfg.port)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
