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
)

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
	// MachineID identifies this host in register/heartbeat payloads.
	MachineID string
	// Token is the bearer token required on all API routes.
	Token string

	// Interceptor is the Layer-1 passive event emitter (issue #32). It is
	// nil-sink safe: New builds it with a nil sink (no-op) until the core
	// injects the real server client via SetEventSink.
	Interceptor *Interceptor

	// lastHEAD is the last observed HEAD SHA for the GIT_COMMITTED
	// HEAD-change detector (see checkGitCommit). Guarded by mu.
	lastHEAD string

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
	d := &Daemon{
		Root:        canon,
		ServerURL:   strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		Addr:        addr,
		MachineID:   host,
		Token:       tok,
		Interceptor: NewInterceptor(nil),
		client:      &http.Client{Timeout: 15 * time.Second},
		stopCh:      make(chan struct{}),
	}
	if head, err := GitCommit(canon); err == nil {
		d.lastHEAD = strings.TrimSpace(head)
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
	if d.Interceptor == nil {
		d.Interceptor = NewInterceptor(nil)
	}
	d.mux.HandleFunc("/healthz", d.handleHealth)
	d.mux.Handle("/file/read", d.requireAuth(http.HandlerFunc(d.handleFileRead)))
	d.mux.Handle("/file/write", d.requireAuth(http.HandlerFunc(d.handleFileWrite)))
	d.mux.Handle("/git/status", d.requireAuth(http.HandlerFunc(d.handleGitStatus)))
	d.mux.Handle("/git/diff", d.requireAuth(http.HandlerFunc(d.handleGitDiff)))
	d.mux.Handle("/command/run", d.requireAuth(http.HandlerFunc(d.handleCommandRun)))
}

// Handler exposes the daemon mux (useful for httptest in unit tests).
func (d *Daemon) Handler() http.Handler { return d.mux }

// SetEventSink injects (or replaces) the Layer-1 event sink (issue #32).
// A nil sink means no-op emission (local-only mode, tests). It never fails.
func (d *Daemon) SetEventSink(sink EventSink) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Interceptor == nil {
		d.Interceptor = NewInterceptor(sink)
		return
	}
	d.Interceptor.Sink = sink
}

// checkGitCommit is the HEAD-change detector feeding GIT_COMMITTED
// (issue #32). It compares the current HEAD SHA against the last observed
// one and emits OnGitCommit on change (message/stat best-effort, capped).
// It never fails the caller: git errors and empty repos are silent no-ops,
// and emission itself is nil-sink/panic safe via Interceptor.Emit.
func (d *Daemon) checkGitCommit() {
	if d == nil {
		return
	}
	head, err := GitCommit(d.Root)
	head = strings.TrimSpace(head)
	if err != nil || head == "" {
		return
	}
	d.mu.Lock()
	last := d.lastHEAD
	if head == last {
		d.mu.Unlock()
		return
	}
	d.lastHEAD = head
	d.mu.Unlock()
	ctx := context.Background()
	msg, _ := runGit(ctx, d.Root, "log", "-1", "--format=%B", head)
	stat, _ := runGit(ctx, d.Root, "show", "--stat", "--oneline", head)
	msg, _ = CapString(strings.TrimSpace(msg), MaxEventDiffBytes)
	stat, _ = CapString(strings.TrimSpace(stat), MaxEventDiffBytes)
	d.Interceptor.OnGitCommit(head, msg, stat)
}

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

// RegisterRequest is the daemon → server registration payload (§1.3:
// project_id, machine_id, path, branch, commit, daemon_url).
type RegisterRequest struct {
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Branch    string `json:"branch,omitempty"`
	Commit    string `json:"commit,omitempty"`
	IsDirty   bool   `json:"is_dirty,omitempty"`
	DaemonURL string `json:"daemon_url,omitempty"`
}

// HeartbeatRequest refreshes liveness and git state (§1.3: last_seen,
// branch, commit_sha, is_dirty; server marks offline after 90s silence).
type HeartbeatRequest struct {
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Branch    string `json:"branch,omitempty"`
	Commit    string `json:"commit,omitempty"`
	IsDirty   bool   `json:"is_dirty,omitempty"`
}

// Register POSTs the workspace to <server>/workspaces/register. It is a
// no-op in local-only mode (empty ServerURL) so `go test` and offline use
// never dial the network.
func (d *Daemon) Register(ctx context.Context) error {
	if d.ServerURL == "" {
		return nil
	}
	branch, _ := GitBranch(d.Root)
	commit, _ := GitCommit(d.Root)
	dirty, _ := GitDirty(d.Root)
	body, _ := json.Marshal(RegisterRequest{
		MachineID: d.MachineID,
		Path:      d.Root,
		Branch:    branch,
		Commit:    commit,
		IsDirty:   dirty,
		DaemonURL: "http://" + d.Addr,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.ServerURL+"/workspaces/register", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("daemon: register request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: register post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon: register: server status %s", resp.Status)
	}
	return nil
}

// HeartbeatOnce POSTs a single heartbeat to <server>/workspaces/heartbeat.
// No-op when ServerURL is empty.
func (d *Daemon) HeartbeatOnce(ctx context.Context) error {
	if d.ServerURL == "" {
		return nil
	}
	branch, _ := GitBranch(d.Root)
	commit, _ := GitCommit(d.Root)
	dirty, _ := GitDirty(d.Root)
	body, _ := json.Marshal(HeartbeatRequest{
		MachineID: d.MachineID,
		Path:      d.Root,
		Branch:    branch,
		Commit:    commit,
		IsDirty:   dirty,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.ServerURL+"/workspaces/heartbeat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("daemon: heartbeat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: heartbeat post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon: heartbeat: server status %s", resp.Status)
	}
	return nil
}

// StartHeartbeatLoop ticks every HeartbeatInterval (30s) until ctx is done,
// calling HeartbeatOnce. The first beat fires immediately so the server
// learns about the daemon without waiting a full interval.
func (d *Daemon) StartHeartbeatLoop(ctx context.Context) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		_ = d.HeartbeatOnce(ctx)
		t := time.NewTicker(HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-t.C:
				_ = d.HeartbeatOnce(ctx)
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
