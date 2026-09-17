package daemon

import (
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
)

func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content here"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(root, "test-token-123")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func doReq(d *Daemon, method, target, body, token string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rdr)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, r)
	return w
}

func TestAuthUnauthorized(t *testing.T) {
	d := testDaemon(t)
	// No token.
	w := doReq(d, "POST", "/file/read", `{"path":"a.txt"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing token: got %d want 401", w.Code)
	}
	// Wrong token.
	w = doReq(d, "POST", "/file/read", `{"path":"a.txt"}`, "wrong")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d want 401", w.Code)
	}
	// Correct token works.
	w = doReq(d, "POST", "/file/read", `{"path":"a.txt"}`, "test-token-123")
	if w.Code != http.StatusOK {
		t.Errorf("valid token: got %d want 200 (%s)", w.Code, w.Body.String())
	}
}

func TestFileReadTraversalReturns403(t *testing.T) {
	d := testDaemon(t)
	paths := []string{"../../etc/passwd", `..\..\Windows\System32\drivers\etc\hosts`, "a.txt:stream"}
	if runtime.GOOS != "windows" {
		paths = append(paths, "/etc/passwd")
	} else {
		paths = append(paths, `C:\Windows\System32\drivers\etc\hosts`)
	}
	for _, p := range paths {
		w := doReq(d, "POST", "/file/read", `{"path":`+jsonStr(p)+`}`, "test-token-123")
		if w.Code != http.StatusForbidden {
			t.Errorf("path %q: got %d want 403 (%s)", p, w.Code, w.Body.String())
		}
	}
}

func TestFileWriteAndReadHandlers(t *testing.T) {
	d := testDaemon(t)
	w := doReq(d, "POST", "/file/write", `{"path":"n.txt","content":"hello handler content"}`, "test-token-123")
	if w.Code != http.StatusOK {
		t.Fatalf("write: got %d (%s)", w.Code, w.Body.String())
	}
	w = doReq(d, "POST", "/file/read", `{"path":"n.txt"}`, "test-token-123")
	if w.Code != http.StatusOK {
		t.Fatalf("read: got %d (%s)", w.Code, w.Body.String())
	}
	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != "hello handler content" {
		t.Fatalf("got %q", out.Content)
	}
}

func TestCommandAllowlistHandler(t *testing.T) {
	d := testDaemon(t)
	// Unallowlisted binary -> 400.
	w := doReq(d, "POST", "/command/run", `{"cmd":"rm","args":["-rf","/"]}`, "test-token-123")
	if w.Code != http.StatusBadRequest {
		t.Errorf("rm: got %d want 400", w.Code)
	}
	// Bare "go" without "test" -> 400.
	w = doReq(d, "POST", "/command/run", `{"cmd":"go","args":["build","./..."]}`, "test-token-123")
	if w.Code != http.StatusBadRequest {
		t.Errorf("go build: got %d want 400", w.Code)
	}
	// Path-smuggled binary -> 400.
	w = doReq(d, "POST", "/command/run", `{"cmd":"./evil","args":[]}`, "test-token-123")
	if w.Code != http.StatusBadRequest {
		t.Errorf("./evil: got %d want 400", w.Code)
	}
}

func TestIsAllowedTable(t *testing.T) {
	allowed := [][]string{
		{"git", "status"},
		{"git", "--version"},
		{"go", "test", "./..."},
		{"npm", "test"},
		{"pytest", "-q"},
		{"cargo", "test"},
		{"python", "-m", "pytest", "-q"},
		{"git.exe", "diff"},
	}
	for _, a := range allowed {
		if !IsAllowed(a) {
			t.Errorf("IsAllowed(%v) = false, want true", a)
		}
	}
	denied := [][]string{
		{},
		{"rm", "-rf", "/"},
		{"go", "build", "./..."},
		{"npm", "install"},
		{"cargo", "run"},
		{"python", "-c", " evil()"},
		{"./evil"},
		{"C:\\tools\\evil.exe"},
		{"git;rm", "-rf"},
	}
	for _, a := range denied {
		if IsAllowed(a) {
			t.Errorf("IsAllowed(%v) = true, want false", a)
		}
	}
}

func TestTokenRoundTrip0600(t *testing.T) {
	root := t.TempDir()
	tok, err := EnsureToken(root)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	back, err := LoadToken(root)
	if err != nil {
		t.Fatal(err)
	}
	if back != tok {
		t.Fatal("token mismatch after reload")
	}
	st, err := os.Stat(TokenPath(root))
	if err != nil {
		t.Fatal(err)
	}
	// Windows only honors the read-only bit, so 0600 cannot be observed
	// via Stat there; enforcement is best-effort on that platform.
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o777 != 0o600 {
		t.Errorf("token file mode = %o, want 600", st.Mode().Perm())
	}
}

func TestNewDaemonValidation(t *testing.T) {
	if _, err := NewDaemon("", "t"); err == nil {
		t.Error("empty root accepted")
	}
	if _, err := NewDaemon(t.TempDir(), ""); err == nil {
		t.Error("empty token accepted")
	}
	if _, err := NewDaemon(filepath.Join(t.TempDir(), "nope"), "t"); err == nil {
		t.Error("missing root accepted")
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------------------------------------------------------------------------
// Central-server contract tests (issue #31).
//
// The strict fake server mirrors internal/server/routes.go validation
// (DisallowUnknownFields + required fields). Payload shapes must stay in
// sync with the server or these tests fail.
// ---------------------------------------------------------------------------

func newContractDaemon(t *testing.T, serverURL string) *Daemon {
	t.Helper()
	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.ServerURL = serverURL
	d.UserID = "user-1"
	return d
}

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
				CanonicalURL string `json:"canonical_url"`
				RootCommit   string `json:"root_commit"`
				FolderName   string `json:"folder_name"`
			}
			if !decodeStrict(t, r, &req) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(req.FolderName) == "" && strings.TrimSpace(req.CanonicalURL) == "" && strings.TrimSpace(req.RootCommit) == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cap.gotResolve = true
			cap.resolveBody = map[string]any{
				"canonical_url": req.CanonicalURL, "root_commit": req.RootCommit,
				"folder_name": req.FolderName,
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "proj-123"})
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
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "ws-456"})
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
			_ = json.NewEncoder(w).Encode(map[string]string{"id": req.WorkspaceID})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestRegisterHeartbeatLocalNoop(t *testing.T) {
	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	// No server URL anywhere -> must not dial network.
	if err := d.Register(context.Background(), ""); err != nil {
		t.Fatalf("Register local noop: %v", err)
	}
	if err := d.HeartbeatOnce(context.Background(), ""); err != nil {
		t.Fatalf("HeartbeatOnce local noop: %v", err)
	}
}

func TestRegisterHeartbeatAgainstServer(t *testing.T) {
	var cap strictCapture
	srv := newStrictServer(t, &cap)
	defer srv.Close()

	d := newContractDaemon(t, srv.URL)

	if err := d.Register(context.Background(), ""); err != nil {
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
	// Workspace ID persisted in memory + on disk (0600).
	if d.WorkspaceID != "ws-456" {
		t.Fatalf("memory workspace id = %q, want ws-456", d.WorkspaceID)
	}
	raw, err := os.ReadFile(WorkspacePath(d.Root))
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "ws-456" {
		t.Fatalf("workspace file = %q, want ws-456", strings.TrimSpace(string(raw)))
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(WorkspacePath(d.Root))
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("workspace file too permissive: %o", st.Mode().Perm())
		}
	}

	if err := d.HeartbeatOnce(context.Background(), ""); err != nil {
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

	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.ServerURL = srv.URL
	t.Setenv("CENTRAL_USER_ID", "")
	t.Setenv("NEXUS_USER_ID", "")
	t.Setenv("USER_ID", "")
	d.UserID = ""
	if err := d.Register(context.Background(), ""); err == nil {
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

	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.ServerURL = srv.URL
	t.Setenv("CENTRAL_USER_ID", "env-user-9")
	if err := d.Register(context.Background(), ""); err != nil {
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

	d := newContractDaemon(t, srv.URL)
	if err := d.HeartbeatOnce(context.Background(), ""); err == nil {
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
	resp, err := http.Post(srv.URL+"/workspaces/heartbeat", "application/json", strings.NewReader(string(legacy)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy heartbeat status = %d, want 400", resp.StatusCode)
	}

	typo, _ := json.Marshal(map[string]any{"workspace_id": "ws-1", "commmit_sha": "typo"})
	resp2, err := http.Post(srv.URL+"/workspaces/heartbeat", "application/json", strings.NewReader(string(typo)))
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
	d, err := NewDaemon(root, "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.ServerURL = srv.URL
	d.UserID = "user-1"
	if err := d.Register(context.Background(), ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Simulate a restart: fresh Daemon loads the workspace ID from disk.
	d2, err := NewDaemon(root, "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon after restart: %v", err)
	}
	d2.ServerURL = srv.URL
	d2.UserID = "user-1"
	if got := d2.getWorkspaceID(); got != "ws-456" {
		t.Fatalf("restarted workspace id = %q, want ws-456", got)
	}
	cap.gotBeat = false
	if err := d2.HeartbeatOnce(context.Background(), ""); err != nil {
		t.Fatalf("HeartbeatOnce after restart: %v", err)
	}
	if !cap.gotBeat || cap.beatBody["workspace_id"] != "ws-456" {
		t.Fatalf("heartbeat after restart = %v, want ws-456", cap.beatBody)
	}
}

func TestDaemonURLFromBoundAddr(t *testing.T) {
	d, err := NewDaemon(t.TempDir(), "test-token-123")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if got := d.DaemonURL(); got != "" {
		t.Fatalf("unstarted DaemonURL = %q, want empty", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	d.mu.Lock()
	// Mirror Start: record the bound address on the server value.
	if d.srv == nil {
		d.srv = &http.Server{}
	}
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
}
