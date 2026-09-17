package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	root := t.TempDir()
	d, err := New(root, "", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func authReq(t *testing.T, d *Daemon, method, target string, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("Authorization", "Bearer "+d.Token)
	return r
}

func TestTokenRoundTrip0600(t *testing.T) {
	root := t.TempDir()
	tok1, err := LoadOrCreateToken(root)
	if err != nil {
		t.Fatalf("LoadOrCreateToken: %v", err)
	}
	if len(tok1) != 64 {
		t.Fatalf("expected 64-char hex token, got %d chars", len(tok1))
	}
	tok2, err := LoadOrCreateToken(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if tok1 != tok2 {
		t.Fatal("token not stable across loads")
	}
	st, err := os.Stat(TokenPath(root))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	// Windows Go ignores Unix permission bits (ACLs apply instead), so the
	// 0600 enforcement is only verifiable on Unix. The WriteFile call still
	// passes 0600 for correct behavior on macOS/Linux.
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token file too permissive: %o", st.Mode().Perm())
	}
	// Distinct roots get distinct tokens.
	other := t.TempDir()
	tok3, err := LoadOrCreateToken(other)
	if err != nil {
		t.Fatalf("other root: %v", err)
	}
	if tok3 == tok1 {
		t.Fatal("tokens collide across roots")
	}
}

func TestAuthNoToken401(t *testing.T) {
	d := newTestDaemon(t)
	for _, target := range []string{"/file/read", "/file/write", "/git/status", "/git/diff", "/command/run"} {
		req := httptest.NewRequest(http.MethodPost, target, nil)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: got %d, want 401", target, rec.Code)
		}
	}
	// GET variants too.
	for _, target := range []string{"/git/status", "/git/diff"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: got %d, want 401", target, rec.Code)
		}
	}
}

func TestAuthWrongToken401(t *testing.T) {
	d := newTestDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/git/status", nil)
	req.Header.Set("Authorization", "Bearer wrong-token-value")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", rec.Code)
	}

	// Missing Bearer prefix is also 401.
	req2 := httptest.NewRequest(http.MethodGet, "/git/status", nil)
	req2.Header.Set("Authorization", d.Token)
	rec2 := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("missing Bearer prefix: got %d, want 401", rec2.Code)
	}
}

func TestHealthzOpen(t *testing.T) {
	d := newTestDaemon(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", rec.Code)
	}
}

func TestRegisterHeartbeatLocalNoop(t *testing.T) {
	d := newTestDaemon(t) // ServerURL == "" → must not dial network
	if err := d.Register(t.Context()); err != nil {
		t.Fatalf("Register local noop: %v", err)
	}
	if err := d.HeartbeatOnce(t.Context()); err != nil {
		t.Fatalf("HeartbeatOnce local noop: %v", err)
	}
}

// strictFakeServer mirrors internal/server/routes.go validation: unknown
// fields rejected, required fields enforced. Payload shapes here must stay
// in sync with the server or the contract tests below fail.
type strictCapture struct {
	gotResolve   bool
	resolveBody  map[string]any
	gotRegister  bool
	registerBody map[string]any
	gotBeat      bool
	beatBody     map[string]any
}

func decodeStrict(t *testing.T, r *http.Request, dst any) bool {
	t.Helper()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return false
	}
	return true
}

func newStrictServer(t *testing.T, cap *strictCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects/resolve":
			var req struct {
				Origin      string `json:"origin"`
				RootCommit  string `json:"root_commit"`
				FolderName  string `json:"folder_name"`
				DisplayName string `json:"display_name"`
			}
			if !decodeStrict(t, r, &req) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(req.FolderName) == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cap.gotResolve = true
			cap.resolveBody = map[string]any{
				"origin": req.Origin, "root_commit": req.RootCommit,
				"folder_name": req.FolderName, "display_name": req.DisplayName,
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"ID": "proj-123"})
		case "/workspaces/register":
			var req struct {
				ProjectID string `json:"project_id"`
				UserID    string `json:"user_id"`
				MachineID string `json:"machine_id"`
				Path      string `json:"path"`
				Branch    string `json:"branch"`
				CommitSHA string `json:"commit_sha"`
				IsDirty   bool   `json:"is_dirty"`
				DaemonURL string `json:"daemon_url"`
			}
			if !decodeStrict(t, r, &req) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid JSON"}`))
				return
			}
			if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.UserID) == "" ||
				strings.TrimSpace(req.MachineID) == "" || strings.TrimSpace(req.Path) == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cap.gotRegister = true
			cap.registerBody = map[string]any{
				"project_id": req.ProjectID, "user_id": req.UserID,
				"machine_id": req.MachineID, "path": req.Path,
				"branch": req.Branch, "commit_sha": req.CommitSHA,
				"is_dirty": req.IsDirty, "daemon_url": req.DaemonURL,
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"ID": "ws-456"})
		case "/workspaces/heartbeat":
			var req struct {
				WorkspaceID string `json:"workspace_id"`
				Branch      string `json:"branch"`
				CommitSHA   string `json:"commit_sha"`
				IsDirty     bool   `json:"is_dirty"`
			}
			if !decodeStrict(t, r, &req) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(req.WorkspaceID) == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cap.gotBeat = true
			cap.beatBody = map[string]any{
				"workspace_id": req.WorkspaceID, "branch": req.Branch,
				"commit_sha": req.CommitSHA, "is_dirty": req.IsDirty,
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"ID": req.WorkspaceID})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestRegisterHeartbeatAgainstServer(t *testing.T) {
	var cap strictCapture
	srv := newStrictServer(t, &cap)
	defer srv.Close()

	root := t.TempDir()
	d, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.UserID = "user-1"
	// Bind a real listener so DaemonURL derives from the bound address,
	// not the "127.0.0.1:0" listen spec.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d.mu.Lock()
	d.srv.Addr = ln.Addr().String()
	d.mu.Unlock()
	defer ln.Close()

	if err := d.Register(t.Context()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !cap.gotResolve {
		t.Fatal("server never saw POST /projects/resolve")
	}
	if !cap.gotRegister {
		t.Fatal("server never saw a valid POST /workspaces/register")
	}
	if cap.registerBody["project_id"] != "proj-123" {
		t.Fatalf("register project_id = %v, want proj-123 (from /projects/resolve)", cap.registerBody["project_id"])
	}
	if cap.registerBody["user_id"] != "user-1" {
		t.Fatalf("register user_id = %v, want user-1", cap.registerBody["user_id"])
	}
	if cap.registerBody["machine_id"] == "" || cap.registerBody["path"] == "" {
		t.Fatalf("register missing machine_id/path: %v", cap.registerBody)
	}
	if _, ok := cap.registerBody["commit"]; ok {
		t.Fatal("register sent legacy 'commit' field; want 'commit_sha'")
	}
	wantURL := "http://" + ln.Addr().String()
	if cap.registerBody["daemon_url"] != wantURL {
		t.Fatalf("register daemon_url = %v, want bound addr %v", cap.registerBody["daemon_url"], wantURL)
	}
	// Workspace ID persisted in memory + on disk (0600).
	if d.WorkspaceID != "ws-456" {
		t.Fatalf("memory workspace id = %q, want ws-456", d.WorkspaceID)
	}
	raw, err := os.ReadFile(WorkspacePath(root))
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "ws-456" {
		t.Fatalf("workspace file = %q, want ws-456", strings.TrimSpace(string(raw)))
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(WorkspacePath(root))
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("workspace file too permissive: %o", st.Mode().Perm())
		}
	}

	if err := d.HeartbeatOnce(t.Context()); err != nil {
		t.Fatalf("HeartbeatOnce: %v", err)
	}
	if !cap.gotBeat {
		t.Fatal("server never saw a valid POST /workspaces/heartbeat")
	}
	if cap.beatBody["workspace_id"] != "ws-456" {
		t.Fatalf("heartbeat workspace_id = %v, want ws-456", cap.beatBody["workspace_id"])
	}
	if _, ok := cap.beatBody["machine_id"]; ok {
		t.Fatal("heartbeat sent legacy 'machine_id'; want only workspace_id")
	}
	if _, ok := cap.beatBody["path"]; ok {
		t.Fatal("heartbeat sent legacy 'path'; want only workspace_id")
	}
}

func TestRegisterRequiresUserID(t *testing.T) {
	var cap strictCapture
	srv := newStrictServer(t, &cap)
	defer srv.Close()

	root := t.TempDir()
	d, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Setenv("CENTRAL_USER_ID", "")
	t.Setenv("NEXUS_USER_ID", "")
	t.Setenv("USER_ID", "")
	d.UserID = ""
	if err := d.Register(t.Context()); err == nil {
		t.Fatal("Register without user_id succeeded, want error")
	} else if !strings.Contains(err.Error(), "user id") {
		t.Fatalf("Register error = %v, want user-id complaint", err)
	}
	if cap.gotRegister {
		t.Fatal("server saw register despite missing user_id")
	}
}

func TestRegisterResolvesUserIDFromEnv(t *testing.T) {
	var cap strictCapture
	srv := newStrictServer(t, &cap)
	defer srv.Close()

	root := t.TempDir()
	d, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Setenv("CENTRAL_USER_ID", "env-user-9")
	if err := d.Register(t.Context()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if cap.registerBody["user_id"] != "env-user-9" {
		t.Fatalf("register user_id = %v, want env-user-9", cap.registerBody["user_id"])
	}
}

func TestHeartbeatWithoutRegisterFails(t *testing.T) {
	var cap strictCapture
	srv := newStrictServer(t, &cap)
	defer srv.Close()

	root := t.TempDir()
	d, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.UserID = "user-1"
	if err := d.HeartbeatOnce(t.Context()); err == nil {
		t.Fatal("HeartbeatOnce without workspace id succeeded, want error")
	}
	if cap.gotBeat {
		t.Fatal("server saw heartbeat without workspace_id")
	}
}

func TestHeartbeatRejectsLegacyShape(t *testing.T) {
	// The old daemon heartbeat {machine_id,path,...} must NOT validate:
	// the server requires workspace_id and rejects unknown fields.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WorkspaceID string `json:"workspace_id"`
			Branch      string `json:"branch"`
			CommitSHA   string `json:"commit_sha"`
			IsDirty     bool   `json:"is_dirty"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.WorkspaceID) == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	legacy, _ := json.Marshal(map[string]any{"machine_id": "m", "path": "/tmp/x"})
	resp, err := http.Post(srv.URL+"/workspaces/heartbeat", "application/json", bytes.NewReader(legacy))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy heartbeat status = %d, want 400", resp.StatusCode)
	}

	typo, _ := json.Marshal(map[string]any{"workspace_id": "ws-1", "commmit_sha": "typo"})
	resp2, err := http.Post(srv.URL+"/workspaces/heartbeat", "application/json", bytes.NewReader(typo))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("typo heartbeat status = %d, want 400 (unknown field)", resp2.StatusCode)
	}
}

func TestHeartbeatSendsPersistedIDAfterRestart(t *testing.T) {
	var cap strictCapture
	srv := newStrictServer(t, &cap)
	defer srv.Close()

	root := t.TempDir()
	d, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.UserID = "user-1"
	if err := d.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Simulate a restart: fresh Daemon loads the workspace ID from disk.
	d2, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	d2.UserID = "user-1"
	if got := d2.getWorkspaceID(); got != "ws-456" {
		t.Fatalf("restarted workspace id = %q, want ws-456", got)
	}
	cap.gotBeat = false
	if err := d2.HeartbeatOnce(context.Background()); err != nil {
		t.Fatalf("HeartbeatOnce after restart: %v", err)
	}
	if !cap.gotBeat || cap.beatBody["workspace_id"] != "ws-456" {
		t.Fatalf("heartbeat after restart = %v, want ws-456", cap.beatBody)
	}
}

func TestDaemonURLFromBoundAddr(t *testing.T) {
	d := newTestDaemon(t)
	if got := d.DaemonURL(); got != "http://127.0.0.1:0" {
		t.Fatalf("unstarted DaemonURL = %q, want http://127.0.0.1:0", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	d.mu.Lock()
	d.srv.Addr = ln.Addr().String()
	d.mu.Unlock()
	if got := d.DaemonURL(); got != "http://"+ln.Addr().String() {
		t.Fatalf("bound DaemonURL = %q, want http://%s", got, ln.Addr().String())
	}
}

func TestHeartbeatBackoff(t *testing.T) {
	if got := heartbeatBackoff(0); got != HeartbeatInterval {
		t.Fatalf("backoff(0) = %v, want %v", got, HeartbeatInterval)
	}
	if got := heartbeatBackoff(1); got != HeartbeatInterval {
		t.Fatalf("backoff(1) = %v, want %v", got, HeartbeatInterval)
	}
	if got := heartbeatBackoff(2); got != 2*HeartbeatInterval {
		t.Fatalf("backoff(2) = %v, want %v", got, 2*HeartbeatInterval)
	}
	prev := heartbeatBackoff(2)
	for i := 3; i < 10; i++ {
		got := heartbeatBackoff(i)
		if got < prev {
			t.Fatalf("backoff(%d) = %v < backoff(%d) = %v", i, got, i-1, prev)
		}
		prev = got
		if got > maxHeartbeatBackoff {
			t.Fatalf("backoff(%d) = %v exceeds cap %v", i, got, maxHeartbeatBackoff)
		}
	}
	if got := heartbeatBackoff(100); got != maxHeartbeatBackoff {
		t.Fatalf("backoff(100) = %v, want cap %v", got, maxHeartbeatBackoff)
	}
}

func TestWorkspaceIDRoundTrip0600(t *testing.T) {
	root := t.TempDir()
	if err := SaveWorkspaceID(root, "ws-abc"); err != nil {
		t.Fatalf("SaveWorkspaceID: %v", err)
	}
	got, err := LoadWorkspaceID(root)
	if err != nil {
		t.Fatalf("LoadWorkspaceID: %v", err)
	}
	if got != "ws-abc" {
		t.Fatalf("round trip = %q, want ws-abc", got)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(WorkspacePath(root))
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("workspace file too permissive: %o", st.Mode().Perm())
		}
	}
	_ = time.Now // keep time import if unused in some builds
}

func TestNewRejectsBadRoot(t *testing.T) {
	if _, err := New("", "", "127.0.0.1:0"); err == nil {
		t.Fatal("empty root accepted")
	}
	if _, err := New(filepath.Join(t.TempDir(), "missing"), "", "127.0.0.1:0"); err == nil {
		t.Fatal("missing root accepted")
	}
}
