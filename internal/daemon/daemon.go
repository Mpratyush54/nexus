// Package daemon implements the local workspace daemon core for Phase 1.
//
// The daemon serves a small authenticated HTTP API sandboxed to a single
// workspace root: file read/write, git status/diff, and allowlisted test
// commands. On startup it generates a bearer token, registers with the
// central server, and heartbeats every 30 seconds.
//
// File layout (directory owned by issue #3; other agents own the rest):
//
//	daemon.go    — HTTP server, token auth, register + 30s heartbeat loop
//	fileops.go   — POST /file/read + /file/write (sandboxed, 1MB read cap)
//	gitops.go    — GET /git/status, GET /git/diff
//	commands.go  — POST /command/run (allowlisted, 60s timeout)
//
// interceptor.go, watcher.go, harvester.go and processor.go are owned by
// other issues and MUST NOT be touched here.
package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HeartbeatInterval is the daemon → server heartbeat period from the plan
// (§1.3: "Every 30s. Updates last_seen, branch, commit_sha, is_dirty").
const HeartbeatInterval = 30 * time.Second

// TokenDirName and TokenFileName locate the bearer token on disk:
// <workspaceRoot>/.central-memory/daemon.token (file mode 0600).
const (
	TokenDirName  = ".central-memory"
	TokenFileName = "daemon.token"
	// WorkspaceFileName persists the server-assigned workspace ID next to
	// the token (file mode 0600) so heartbeats survive restarts.
	WorkspaceFileName = "daemon.workspace"
)

// maxHeartbeatBackoff caps the exponential backoff applied after
// consecutive heartbeat failures (see heartbeatBackoff).
const maxHeartbeatBackoff = 5 * time.Minute

// Daemon is the workspace daemon core: an HTTP server sandboxed to Root,
// authenticated by Token, optionally reporting to ServerURL.
type Daemon struct {
	// Root is the canonical absolute workspace root. All file access is
	// confined beneath it (see ResolveInSandbox in fileops.go).
	Root string
	// ServerURL is the central server base URL (e.g. https://api.example.com).
	// Empty means local-only mode: register/heartbeat are no-ops.
	ServerURL string
	// Addr is the listen address (e.g. "127.0.0.1:0" for tests).
	Addr string
	// MachineID identifies this host in the register payload.
	MachineID string
	// UserID identifies the owning user for registration (server requires
	// user_id). Empty means resolve from the environment at Register time
	// (CENTRAL_USER_ID, NEXUS_USER_ID, USER_ID).
	UserID string
	// WorkspaceID is the server-assigned workspace ID from registration.
	// It is sent on every heartbeat and persisted to disk (see
	// WorkspaceFileName). Guarded by mu.
	WorkspaceID string
	// Token is the bearer token required on all API routes.
	Token string

	mux    *http.ServeMux
	srv    *http.Server
	client *http.Client

	mu     sync.Mutex
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// TokenDir returns <root>/.central-memory.
func TokenDir(root string) string {
	return filepath.Join(root, TokenDirName)
}

// TokenPath returns <root>/.central-memory/daemon.token.
func TokenPath(root string) string {
	return filepath.Join(TokenDir(root), TokenFileName)
}

// WorkspacePath returns <root>/.central-memory/daemon.workspace.
func WorkspacePath(root string) string {
	return filepath.Join(TokenDir(root), WorkspaceFileName)
}

// LoadWorkspaceID reads the persisted workspace ID for root (""
// when absent).
func LoadWorkspaceID(root string) (string, error) {
	data, err := os.ReadFile(WorkspacePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("daemon: read workspace file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// SaveWorkspaceID persists the workspace ID for root (file mode 0600).
func SaveWorkspaceID(root, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("daemon: empty workspace id")
	}
	if err := os.MkdirAll(TokenDir(root), 0o755); err != nil {
		return fmt.Errorf("daemon: create token dir: %w", err)
	}
	if err := os.WriteFile(WorkspacePath(root), []byte(strings.TrimSpace(id)+"\n"), 0o600); err != nil {
		return fmt.Errorf("daemon: write workspace file: %w", err)
	}
	return nil
}

// GenerateToken returns a hex-encoded 32-byte crypto/rand token (64 chars).
func GenerateToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("daemon: generate token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// LoadOrCreateToken loads the bearer token for root, generating and storing
// a fresh one (file mode 0600) when absent or blank.
func LoadOrCreateToken(root string) (string, error) {
	path := TokenPath(root)
	if data, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("daemon: read token file: %w", err)
	}
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(TokenDir(root), 0o755); err != nil {
		return "", fmt.Errorf("daemon: create token dir: %w", err)
	}
	// 0600: the token is a credential; group/other must not read it.
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("daemon: write token file: %w", err)
	}
	return tok, nil
}

// CanonicalRoot resolves root to an absolute, cleaned path.
func CanonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("daemon: empty workspace root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("daemon: resolve workspace root: %w", err)
	}
	return filepath.Clean(abs), nil
}

// New builds a Daemon for root. It ensures the bearer token exists on disk
// and mounts all routes; it does not start listening (see Serve/Start).
func New(root, serverURL, addr string) (*Daemon, error) {
	canon, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canon)
	if err != nil {
		return nil, fmt.Errorf("daemon: workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("daemon: workspace root %q is not a directory", canon)
	}
	tok, err := LoadOrCreateToken(canon)
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	wsID, _ := LoadWorkspaceID(canon)
	d := &Daemon{
		Root:        canon,
		ServerURL:   strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		Addr:        addr,
		MachineID:   host,
		Token:       tok,
		WorkspaceID: strings.TrimSpace(wsID),
		client:      &http.Client{Timeout: 15 * time.Second},
		stopCh:      make(chan struct{}),
	}
	d.mux = http.NewServeMux()
	d.mount()
	d.srv = &http.Server{
		Addr:              addr,
		Handler:           d.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return d, nil
}

// mount registers routes. /healthz is intentionally unauthenticated so
// process managers and liveness probes work without the secret; every other
// route requires Authorization: Bearer <token> and returns 401 otherwise.
func (d *Daemon) mount() {
	d.mux.HandleFunc("/healthz", d.handleHealth)
	d.mux.Handle("/file/read", d.requireAuth(http.HandlerFunc(d.handleFileRead)))
	d.mux.Handle("/file/write", d.requireAuth(http.HandlerFunc(d.handleFileWrite)))
	d.mux.Handle("/git/status", d.requireAuth(http.HandlerFunc(d.handleGitStatus)))
	d.mux.Handle("/git/diff", d.requireAuth(http.HandlerFunc(d.handleGitDiff)))
	d.mux.Handle("/command/run", d.requireAuth(http.HandlerFunc(d.handleCommandRun)))
}

// Handler exposes the daemon mux (useful for httptest in unit tests).
func (d *Daemon) Handler() http.Handler { return d.mux }

// CheckAuth reports whether r carries the daemon bearer token.
func (d *Daemon) CheckAuth(r *http.Request) bool {
	const prefix = "Bearer "
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, prefix) {
		return false
	}
	candidate := strings.TrimSpace(strings.TrimPrefix(got, prefix))
	if candidate == "" || d.Token == "" {
		return false
	}
	// Constant-time compare to avoid leaking token bytes via timing.
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(d.Token)) == 1
}

// requireAuth is the Bearer-check middleware: 401 on failure.
func (d *Daemon) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.CheckAuth(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Serve runs the daemon on ln until ctx is cancelled.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.srv.Shutdown(shCtx)
	}()
	err := d.srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Start listens on d.Addr (":0" allowed) and serves until ctx is cancelled.
// It returns the bound address for logging/registration.
func (d *Daemon) Start(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", d.Addr)
	if err != nil {
		return "", fmt.Errorf("daemon: listen: %w", err)
	}
	d.mu.Lock()
	d.srv.Addr = ln.Addr().String()
	d.mu.Unlock()
	go func() { _ = d.Serve(ctx, ln) }()
	return ln.Addr().String(), nil
}

// RegisterRequest is the daemon → server registration payload. Field names
// must match internal/server/routes.go registerRequest exactly: the server
// rejects unknown fields and requires project_id, user_id, machine_id and
// path (400 otherwise).
type RegisterRequest struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	IsDirty   bool   `json:"is_dirty,omitempty"`
	DaemonURL string `json:"daemon_url,omitempty"`
}

// HeartbeatRequest refreshes liveness and git state (§1.3: last_seen,
// branch, commit_sha, is_dirty; server marks offline after 90s silence).
// The server requires workspace_id and rejects unknown fields, so no
// machine_id/path may be sent here.
type HeartbeatRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Branch      string `json:"branch,omitempty"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	IsDirty     bool   `json:"is_dirty,omitempty"`
}

// resolveRequest mirrors POST /projects/resolve (folder_name required).
type resolveRequest struct {
	Origin      string `json:"origin,omitempty"`
	RootCommit  string `json:"root_commit,omitempty"`
	FolderName  string `json:"folder_name"`
	DisplayName string `json:"display_name,omitempty"`
}

// DaemonURL advertises the reachable daemon address. It derives from the
// bound listener address (set by Start) and falls back to the configured
// listen spec when the daemon has not started yet. The old code advertised
// the raw listen spec (e.g. "127.0.0.1:0"), which is never dialable.
func (d *Daemon) DaemonURL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	addr := d.Addr
	if d.srv != nil && strings.TrimSpace(d.srv.Addr) != "" {
		addr = d.srv.Addr
	}
	if strings.TrimSpace(addr) == "" {
		return ""
	}
	return "http://" + strings.TrimSpace(addr)
}

// effectiveUserID resolves the owning user: the explicit UserID field
// first, then CENTRAL_USER_ID / NEXUS_USER_ID / USER_ID from the
// environment.
func (d *Daemon) effectiveUserID() string {
	if strings.TrimSpace(d.UserID) != "" {
		return strings.TrimSpace(d.UserID)
	}
	for _, key := range []string{"CENTRAL_USER_ID", "NEXUS_USER_ID", "USER_ID"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// getWorkspaceID returns the cached workspace ID, falling back to the
// persisted file so heartbeats survive restarts.
func (d *Daemon) getWorkspaceID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.TrimSpace(d.WorkspaceID) != "" {
		return strings.TrimSpace(d.WorkspaceID)
	}
	if id, err := LoadWorkspaceID(d.Root); err == nil && strings.TrimSpace(id) != "" {
		d.WorkspaceID = strings.TrimSpace(id)
		return d.WorkspaceID
	}
	return ""
}

// setWorkspaceID caches the workspace ID in memory and persists it to
// disk (mode 0600) next to the token.
func (d *Daemon) setWorkspaceID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("daemon: empty workspace id")
	}
	if err := SaveWorkspaceID(d.Root, id); err != nil {
		return err
	}
	d.mu.Lock()
	d.WorkspaceID = id
	d.mu.Unlock()
	return nil
}

// fingerprint derives move-proof repo identity daemon-side using the same
// concepts as project.Fingerprint (git origin URL + root-commit hash) but
// without importing internal/store or internal/project: the daemon package
// must stay stdlib-only. Failures yield "" and never fail registration —
// the server falls back to folder_name matching.
func (d *Daemon) fingerprint() (origin, rootCommit string) {
	if out, err := runGit(context.Background(), d.Root, "remote", "get-url", "origin"); err == nil {
		origin = strings.TrimSpace(out)
	}
	if out, err := runGit(context.Background(), d.Root, "rev-list", "--max-parents=0", "HEAD"); err == nil {
		rootCommit = firstLine(out)
	}
	return origin, rootCommit
}

// postJSON POSTs payload as JSON and optionally decodes a 2xx response
// into out.
func (d *Daemon) postJSON(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("daemon: encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.ServerURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("daemon: %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: %s post: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon: %s: server status %s", strings.TrimPrefix(path, "/"), resp.Status)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("daemon: %s decode: %w", strings.TrimPrefix(path, "/"), err)
		}
	}
	return nil
}

// resolveProjectID POSTs /projects/resolve first so Register can send the
// server-required project_id.
func (d *Daemon) resolveProjectID(ctx context.Context) (string, error) {
	origin, rootCommit := d.fingerprint()
	folder := filepath.Base(filepath.Clean(d.Root))
	var out struct {
		ID string `json:"id"`
	}
	if err := d.postJSON(ctx, "/projects/resolve", resolveRequest{
		Origin:      origin,
		RootCommit:  rootCommit,
		FolderName:  folder,
		DisplayName: folder,
	}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", fmt.Errorf("daemon: projects/resolve returned empty id")
	}
	return strings.TrimSpace(out.ID), nil
}

// Register resolves the project, POSTs the workspace to
// <server>/workspaces/register, and persists the returned workspace ID in
// memory and on disk. It is a no-op in local-only mode (empty ServerURL)
// so `go test` and offline use never dial the network.
func (d *Daemon) Register(ctx context.Context) error {
	if d.ServerURL == "" {
		return nil
	}
	userID := d.effectiveUserID()
	if userID == "" {
		return fmt.Errorf("daemon: user id is required (set Daemon.UserID or CENTRAL_USER_ID)")
	}
	if strings.TrimSpace(d.MachineID) == "" {
		return fmt.Errorf("daemon: machine id is required")
	}
	projectID, err := d.resolveProjectID(ctx)
	if err != nil {
		return fmt.Errorf("daemon: resolve project: %w", err)
	}
	branch, _ := GitBranch(d.Root)
	commit, _ := GitCommit(d.Root)
	dirty, _ := GitDirty(d.Root)
	var out struct {
		ID string `json:"id"`
	}
	if err := d.postJSON(ctx, "/workspaces/register", RegisterRequest{
		ProjectID: projectID,
		UserID:    userID,
		MachineID: strings.TrimSpace(d.MachineID),
		Path:      d.Root,
		Branch:    branch,
		CommitSHA: commit,
		IsDirty:   dirty,
		DaemonURL: d.DaemonURL(),
	}, &out); err != nil {
		return err
	}
	if strings.TrimSpace(out.ID) == "" {
		return fmt.Errorf("daemon: workspaces/register returned empty id")
	}
	return d.setWorkspaceID(out.ID)
}

// HeartbeatOnce POSTs a single {workspace_id, branch, commit_sha, is_dirty}
// heartbeat to <server>/workspaces/heartbeat. No-op when ServerURL is
// empty. It errors when the workspace ID is unknown (Register first).
func (d *Daemon) HeartbeatOnce(ctx context.Context) error {
	if d.ServerURL == "" {
		return nil
	}
	wsID := d.getWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("daemon: heartbeat without workspace id (register first)")
	}
	branch, _ := GitBranch(d.Root)
	commit, _ := GitCommit(d.Root)
	dirty, _ := GitDirty(d.Root)
	return d.postJSON(ctx, "/workspaces/heartbeat", HeartbeatRequest{
		WorkspaceID: wsID,
		Branch:      branch,
		CommitSHA:   commit,
		IsDirty:     dirty,
	}, nil)
}

// heartbeatBackoff returns the delay before the next heartbeat after
// failures consecutive errors: HeartbeatInterval doubling per failure,
// capped at maxHeartbeatBackoff. The first failure retries on the normal
// interval.
func heartbeatBackoff(failures int) time.Duration {
	if failures <= 1 {
		return HeartbeatInterval
	}
	d := HeartbeatInterval << (failures - 1)
	if d <= 0 || d > maxHeartbeatBackoff {
		return maxHeartbeatBackoff
	}
	return d
}

// StartHeartbeatLoop ticks every HeartbeatInterval (30s) until ctx is done,
// calling HeartbeatOnce. The first beat fires immediately so the server
// learns about the daemon without waiting a full interval. Errors are
// logged (previously swallowed) and the next tick backs off exponentially
// while failures persist.
func (d *Daemon) StartHeartbeatLoop(ctx context.Context) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		failures := 0
		beat := func() {
			if err := d.HeartbeatOnce(ctx); err != nil {
				failures++
				log.Printf("daemon: heartbeat: %v (retry in %s)", err, heartbeatBackoff(failures))
			} else {
				failures = 0
			}
		}
		beat()
		timer := time.NewTimer(HeartbeatInterval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-timer.C:
				beat()
				timer.Reset(heartbeatBackoff(failures))
			}
		}
	}()
}

// StopHeartbeat signals the heartbeat goroutine to exit (ctx cancellation
// also stops it); Wait blocks until background goroutines finish.
func (d *Daemon) StopHeartbeat() {
	select {
	case <-d.stopCh:
	default:
		close(d.stopCh)
	}
}

// Wait blocks until heartbeat goroutines exit.
func (d *Daemon) Wait() { d.wg.Wait() }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
