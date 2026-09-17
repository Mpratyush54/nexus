// Command server — local-dev entrypoint for the central API server
// (issue #39, plan target tree).
//
// Env-configured, thin wiring only: reads PORT + JWT_SECRET, builds the API
// via server.New, and serves until SIGINT/SIGTERM. This is the local
// counterpart to deploy/server-bootstrap (the container entrypoint, which
// additionally dials Aurora with retry, runs migrations on boot, and serves
// /readyz) — that file is untouched.
//
// Data-route persistence: server.New requires a server.Store and the
// Postgres-backed adapter is a follow-up owned by the store/server issues,
// so this entrypoint ships a stubStore that fails closed (HTTP 500) on data
// routes — the same posture as the bootstrap. /auth/login fails closed too
// (no users table yet); /healthz is live. Nothing silently pretends to
// persist.
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

func newHandler(secret string) http.Handler {
	srv := server.NewServer(stubStore{})
	srv.Auth = server.NewAuthenticator([]byte(secret))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503,"message":"authentication unavailable: user store adapter pending"}}`))
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

	port := getenv("PORT", "8080")
	secret := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if secret == "" {
		secret = "central-memory-dev-secret-do-not-use-in-prod"
		log.Print("server: JWT_SECRET unset — using insecure dev default (local use only)")
	}

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(secret),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shCtx)
	}()
	log.Printf("server: listening on :%s (healthz open; data routes fail closed pending store adapter)", port)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
