// Workspace daemon core: HTTP server, registration, heartbeat.
//
// Endpoints (all require Authorization: Bearer <daemon token>):
//
//	POST /register     daemon identity (machine, path, fingerprint, git state)
//	POST /heartbeat    liveness + branch/commit/dirty (also accepts GET)
//	POST /file/read    {path} -> {path, size, content}
//	POST /file/write   {path, content} -> {ok, bytes}
//	GET  /git/status   -> {branch, commit, dirty, porcelain}
//	GET  /git/diff     ?ref= (or POST {ref}) -> {ref, diff}
//	GET  /git/log      ?n= -> {log}
//	POST /command/run  {cmd, args} (or {argv}) -> CommandResult
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HeartbeatInterval is the daemon -> server heartbeat period (30s).
const HeartbeatInterval = 30 * time.Second

// maxHeartbeatBackoff caps the exponential backoff applied after consecutive
// heartbeat failures (see heartbeatBackoff). Issue #31.
const maxHeartbeatBackoff = 5 * time.Minute

// workspaceFileName persists the server-assigned workspace ID next to the
// daemon token (<root>/.central-memory/daemon.workspace, mode 0600) so
// heartbeats survive daemon restarts. Issue #31.
const workspaceFileName = "daemon.workspace"

// maxBodyBytes bounds JSON request bodies (2MB: 1MB file + envelope).
const maxBodyBytes = 2 << 20

// Daemon is a sandboxed workspace agent bound to a single root directory.
type Daemon struct {
	// Root is the absolute workspace root all operations are sandboxed to.
	Root string
	// Token is the bearer token required by every endpoint.
	Token string
	// ServerURL is the central server base URL (optional; used by
	// Register/StartHeartbeat when set).
	ServerURL string
	// MachineID identifies this machine (os.Hostname, "unknown" fallback).
	MachineID string
	// UserID identifies the owning user for registration. Empty means
	// resolve from the environment at Register time (CENTRAL_USER_ID,
	// NEXUS_USER_ID, USER_ID). Issue #31.
	UserID string
	// WorkspaceID is the server-assigned workspace ID from registration.
	// Sent on every heartbeat and persisted to disk (see workspaceFileName)
	// so heartbeats survive restarts. Guarded by mu. Issue #31.
	WorkspaceID string

	mux *http.ServeMux
	srv *http.Server

	client *http.Client
	mu     sync.Mutex
}

// NewDaemon builds a daemon bound to root, authenticated by token.
// It fails when root is blank/missing or token is blank.
func NewDaemon(root, token string) (*Daemon, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("daemon: empty root")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("daemon: empty token")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("daemon: root: %w", err)
	}
	if !st.IsDir() {
		return nil, errors.New("daemon: root is not a directory")
	}
	// Canonicalize (8.3 short names, junctions) so sandbox comparisons
	// in SecureJoin are canonical-vs-canonical.
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = filepath.Clean(resolved)
	}
	machine, err := os.Hostname()
	if err != nil || machine == "" {
		machine = "unknown"
	}
	d := &Daemon{
		Root:      abs,
		Token:     token,
		MachineID: machine,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
	// Best-effort: pick up a workspace ID persisted by a previous run so a
	// restarted daemon can heartbeat without re-registering. Issue #31.
	if wsID, err := LoadWorkspaceID(abs); err == nil {
		d.WorkspaceID = strings.TrimSpace(wsID)
	}
	d.mux = http.NewServeMux()
	d.mux.HandleFunc("/register", d.requireAuth(d.handleRegister))
	d.mux.HandleFunc("/heartbeat", d.requireAuth(d.handleHeartbeat))
	d.mux.HandleFunc("/file/read", d.requireAuth(d.handleFileRead))
	d.mux.HandleFunc("/file/write", d.requireAuth(d.handleFileWrite))
	d.mux.HandleFunc("/git/status", d.requireAuth(d.handleGitStatus))
	d.mux.HandleFunc("/git/diff", d.requireAuth(d.handleGitDiff))
	d.mux.HandleFunc("/git/log", d.requireAuth(d.handleGitLog))
	d.mux.HandleFunc("/command/run", d.requireAuth(d.handleCommandRun))
	return d, nil
}

// Handler returns the daemon HTTP handler (for tests and embedding).
func (d *Daemon) Handler() http.Handler { return d.mux }

// Start serves the daemon on addr (e.g. "127.0.0.1:0" is rejected — pass an
// explicit port). It blocks until the server stops.
func (d *Daemon) Start(addr string) error {
	d.srv = &http.Server{Addr: addr, Handler: d.mux}
	return d.srv.ListenAndServe()
}

// Close gracefully stops a started daemon.
func (d *Daemon) Close() error {
	if d.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.srv.Shutdown(ctx)
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Registration + heartbeat (local status surface + central-server client)
// ---------------------------------------------------------------------------

// registrationPayload is the daemon identity snapshot.
type registrationPayload struct {
	MachineID  string `json:"machine_id"`
	Path       string `json:"path"`
	Origin     string `json:"origin,omitempty"`
	RootCommit string `json:"root_commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	DaemonURL  string `json:"daemon_url,omitempty"`
}

func (d *Daemon) registration() registrationPayload {
	origin, rootCommit := FingerprintOf(d.Root)
	branch, commit, _, _, _ := GitStatus(d.Root)
	return registrationPayload{
		MachineID:  d.MachineID,
		Path:       d.Root,
		Origin:     origin,
		RootCommit: rootCommit,
		Branch:     branch,
		Commit:     commit,
	}
}

func (d *Daemon) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, d.registration())
}

// heartbeatPayload mirrors the workspaces heartbeat row.
type heartbeatPayload struct {
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	IsDirty   bool   `json:"is_dirty"`
	IsOnline  bool   `json:"is_online"`
	Time      string `json:"time"`
}

func (d *Daemon) heartbeat() heartbeatPayload {
	branch, commit, dirty, _, _ := GitStatus(d.Root)
	return heartbeatPayload{
		MachineID: d.MachineID,
		Path:      d.Root,
		Branch:    branch,
		CommitSHA: commit,
		IsDirty:   dirty,
		IsOnline:  true,
		Time:      time.Now().UTC().Format(time.RFC3339),
	}
}

func (d *Daemon) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, d.heartbeat())
}

// ---------------------------------------------------------------------------
// Central-server contract client (issue #31)
//
// The server (internal/server/routes.go, DisallowUnknownFields) requires:
//   - POST /projects/resolve {canonical_url?, root_commit?, folder_name}
//     -> project {id}
//   - POST /workspaces/register {project_id, user_id, machine_id, path, ...}
//     -> workspace {id} (201)
//   - POST /workspaces/heartbeat {workspace_id, branch?, commit_sha?,
//     is_dirty?} (workspace_id required)
//
// Register resolves the project, registers the workspace, and persists the
// returned workspace ID in memory and on disk; HeartbeatOnce sends a single
// workspace_id heartbeat; StartHeartbeat loops HeartbeatOnce with
// exponential backoff on failures.
// ---------------------------------------------------------------------------

// WorkspacePath returns <root>/.central-memory/daemon.workspace.
func WorkspacePath(root string) string {
	return filepath.Join(root, tokenDirName, workspaceFileName)
}

// LoadWorkspaceID reads the persisted workspace ID for root ("" when absent).
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

// SaveWorkspaceID persists id for root (dir 0700, file 0600).
func SaveWorkspaceID(root, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("daemon: empty workspace id")
	}
	if err := os.MkdirAll(filepath.Join(root, tokenDirName), 0o700); err != nil {
		return fmt.Errorf("daemon: create token dir: %w", err)
	}
	if err := os.WriteFile(WorkspacePath(root), []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("daemon: write workspace file: %w", err)
	}
	return nil
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

// setWorkspaceID caches the workspace ID in memory and persists it to disk.
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

// effectiveUserID resolves the owning user: the explicit UserID field first,
// then CENTRAL_USER_ID / NEXUS_USER_ID / USER_ID from the environment.
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

// DaemonURL advertises the reachable daemon address, derived from the bound
// listener address when started ("" before Start). Previously the raw listen
// spec (e.g. "127.0.0.1:0") was advertised, which is never dialable.
func (d *Daemon) DaemonURL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.srv == nil || strings.TrimSpace(d.srv.Addr) == "" {
		return ""
	}
	return "http://" + strings.TrimSpace(d.srv.Addr)
}

// registerRequest mirrors store.Workspace JSON for registration. Field names
// must stay in sync with the server decoder (unknown fields are rejected).
type registerRequest struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	IsDirty   bool   `json:"is_dirty,omitempty"`
	DaemonURL string `json:"daemon_url,omitempty"`
}

// heartbeatRequest mirrors the server heartbeatRequest: workspace_id is
// required; machine_id/path must NOT be sent (rejected as unknown).
type heartbeatRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Branch      string `json:"branch,omitempty"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	IsDirty     bool   `json:"is_dirty,omitempty"`
}

// resolveRequest mirrors projectResolveRequest: only canonical_url,
// root_commit and folder_name are accepted (unknown fields rejected).
type resolveRequest struct {
	CanonicalURL string `json:"canonical_url,omitempty"`
	RootCommit   string `json:"root_commit,omitempty"`
	FolderName   string `json:"folder_name"`
}

// postJSON POSTs payload as JSON to base+path and optionally decodes a 2xx
// response into out.
func (d *Daemon) postJSON(ctx context.Context, base, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("daemon: encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("daemon: %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := d.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: %s post: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("daemon: %s: server status %s", strings.TrimPrefix(path, "/"), resp.Status)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("daemon: %s decode: %w", strings.TrimPrefix(path, "/"), err)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	}
	return nil
}

// resolveProjectID POSTs /projects/resolve so Register can send the
// server-required project_id. Identity uses the same inputs as
// project fingerprinting (origin URL as canonical_url + root commit),
// falling back to folder_name matching server-side.
func (d *Daemon) resolveProjectID(ctx context.Context, base string) (string, error) {
	origin, rootCommit := FingerprintOf(d.Root)
	folder := filepath.Base(filepath.Clean(d.Root))
	var out struct {
		ID string `json:"id"`
	}
	if err := d.postJSON(ctx, base, "/projects/resolve", resolveRequest{
		CanonicalURL: origin,
		RootCommit:   rootCommit,
		FolderName:   folder,
	}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", fmt.Errorf("daemon: projects/resolve returned empty id")
	}
	return strings.TrimSpace(out.ID), nil
}

// Register resolves the project, POSTs the workspace to
// <serverURL>/workspaces/register, and persists the returned workspace ID
// in memory and on disk. An empty serverURL (and unset d.ServerURL) is a
// no-op so tests and offline use never dial the network.
func (d *Daemon) Register(ctx context.Context, serverURL string) error {
	base := strings.TrimSpace(serverURL)
	if base == "" {
		base = strings.TrimSpace(d.ServerURL)
	}
	if base == "" {
		return nil
	}
	userID := d.effectiveUserID()
	if userID == "" {
		return fmt.Errorf("daemon: user id is required (set Daemon.UserID or CENTRAL_USER_ID)")
	}
	if strings.TrimSpace(d.MachineID) == "" {
		return fmt.Errorf("daemon: machine id is required")
	}
	projectID, err := d.resolveProjectID(ctx, base)
	if err != nil {
		return fmt.Errorf("daemon: resolve project: %w", err)
	}
	branch, commit, dirty, _, _ := GitStatus(d.Root)
	var out struct {
		ID string `json:"id"`
	}
	if err := d.postJSON(ctx, base, "/workspaces/register", registerRequest{
		ProjectID: strings.TrimSpace(projectID),
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
// heartbeat to <serverURL>/workspaces/heartbeat. It errors when the
// workspace ID is unknown (Register first) and is a no-op without a server
// URL.
func (d *Daemon) HeartbeatOnce(ctx context.Context, serverURL string) error {
	base := strings.TrimSpace(serverURL)
	if base == "" {
		base = strings.TrimSpace(d.ServerURL)
	}
	if base == "" {
		return nil
	}
	wsID := d.getWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("daemon: heartbeat without workspace id (register first)")
	}
	branch, commit, dirty, _, _ := GitStatus(d.Root)
	return d.postJSON(ctx, base, "/workspaces/heartbeat", heartbeatRequest{
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
	backoff := HeartbeatInterval << (failures - 1)
	if backoff <= 0 || backoff > maxHeartbeatBackoff {
		return maxHeartbeatBackoff
	}
	return backoff
}

// StartHeartbeat POSTs heartbeat snapshots every interval until ctx ends.
// The first beat fires immediately; failures are logged (previously
// swallowed) and retried with exponential backoff while they persist.
func (d *Daemon) StartHeartbeat(ctx context.Context, serverURL string, interval time.Duration) {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	base := strings.TrimSpace(serverURL)
	if base == "" {
		base = strings.TrimSpace(d.ServerURL)
	}
	if base != "" {
		d.mu.Lock()
		d.ServerURL = base
		d.mu.Unlock()
	}
	failures := 0
	beat := func() {
		if err := d.HeartbeatOnce(ctx, base); err != nil {
			failures++
			log.Printf("daemon: heartbeat: %v (retry in %s)", err, heartbeatBackoff(failures))
		} else {
			failures = 0
		}
	}
	beat()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}

// ---------------------------------------------------------------------------
// File handlers
// ---------------------------------------------------------------------------

type fileReadReq struct {
	Path string `json:"path"`
}

func (d *Daemon) handleFileRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req fileReadReq
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	data, err := ReadFile(d.Root, req.Path)
	if err != nil {
		writeFileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    req.Path,
		"size":    len(data),
		"content": string(data),
	})
}

type fileWriteReq struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (d *Daemon) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req fileWriteReq
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := WriteFile(d.Root, req.Path, []byte(req.Content)); err != nil {
		writeFileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(req.Content)})
}

// writeFileErr maps sandbox errors to HTTP statuses: traversal/secret -> 403,
// too large -> 413, missing -> 404, dirs -> 400, else 500.
func writeFileErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTraversal):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrSecretBlocked):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, ErrNotFile):
		writeErr(w, http.StatusBadRequest, err.Error())
	case os.IsNotExist(err):
		writeErr(w, http.StatusNotFound, "not found")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// ---------------------------------------------------------------------------
// Git handlers
// ---------------------------------------------------------------------------

func (d *Daemon) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	branch, commit, dirty, porcelain, err := GitStatus(d.Root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"branch": branch, "commit": commit, "is_dirty": dirty, "porcelain": porcelain,
	})
}

func (d *Daemon) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ref := r.URL.Query().Get("ref")
	if r.Method == http.MethodPost {
		var req struct {
			Ref string `json:"ref"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Ref != "" {
			ref = req.Ref
		}
	}
	diff, err := GitDiff(d.Root, ref)
	if err != nil {
		var bref *BadRefError
		if errors.As(err, &bref) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref, "diff": diff})
}

func (d *Daemon) handleGitLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	n := 20
	if raw := r.URL.Query().Get("n"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil {
			n = parsed
		}
	}
	out, err := GitLog(d.Root, n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": out})
}

// ---------------------------------------------------------------------------
// Command handler
// ---------------------------------------------------------------------------

func (d *Daemon) handleCommandRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Cmd  string   `json:"cmd"`
		Args []string `json:"args"`
		Argv []string `json:"argv"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	argv := req.Argv
	if len(argv) == 0 {
		if strings.TrimSpace(req.Cmd) == "" {
			writeErr(w, http.StatusBadRequest, "cmd is required")
			return
		}
		argv = append([]string{req.Cmd}, req.Args...)
	}
	if !IsAllowed(argv) {
		writeErr(w, http.StatusBadRequest, ErrNotAllowed.Error())
		return
	}
	res, err := RunCommand(d.Root, argv)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeErr(w, http.StatusRequestTimeout, "command timed out")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
