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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HeartbeatInterval is the daemon -> server heartbeat period (30s).
const HeartbeatInterval = 30 * time.Second

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

	mux *http.ServeMux
	srv *http.Server
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
	d := &Daemon{Root: abs, Token: token, MachineID: machine}
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

// Register POSTs daemon identity to <serverURL>/workspaces/register.
func (d *Daemon) Register(ctx context.Context, serverURL string) error {
	body, _ := json.Marshal(d.registration())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(serverURL, "/")+"/workspaces/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("daemon: register: server status %s", resp.Status)
	}
	return nil
}

// StartHeartbeat POSTs heartbeat snapshots every interval until ctx ends.
func (d *Daemon) StartHeartbeat(ctx context.Context, serverURL string, interval time.Duration) {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	url := strings.TrimSuffix(serverURL, "/") + "/workspaces/heartbeat"
	post := func() {
		body, _ := json.Marshal(d.heartbeat())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
	post()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			post()
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
