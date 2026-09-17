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

func (stubStore) Authenticate(ctx context.Context, username, password string) (string, error) {
	return "", errStorePending
}
func (stubStore) ResolveProject(ctx context.Context, p store.ProjectParams) (*store.Project, error) {
	return nil, errStorePending
}
func (stubStore) RegisterWorkspace(ctx context.Context, p store.WorkspaceParams) (*store.Workspace, error) {
	return nil, errStorePending
}
func (stubStore) HeartbeatWorkspace(ctx context.Context, id string, hb store.HeartbeatParams) (*store.Workspace, error) {
	return nil, errStorePending
}
func (stubStore) ListWorkspaces(ctx context.Context, projectID string) ([]store.Workspace, error) {
	return nil, errStorePending
}
func (stubStore) CreateMemory(ctx context.Context, m server.Memory) (*server.Memory, error) {
	return nil, errStorePending
}
func (stubStore) SearchMemory(ctx context.Context, q server.MemoryFilter) ([]server.Memory, error) {
	return nil, errStorePending
}
func (stubStore) CreateEpisode(ctx context.Context, e server.Episode) (*server.Episode, error) {
	return nil, errStorePending
}
func (stubStore) SearchEpisodes(ctx context.Context, q server.EpisodeFilter) ([]server.Episode, error) {
	return nil, errStorePending
}

// Compile-time proof the stub satisfies the server seam.
var _ server.Store = stubStore{}

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

	srv := server.New(stubStore{}, server.Options{JWTSecret: []byte(secret)})
	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
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
