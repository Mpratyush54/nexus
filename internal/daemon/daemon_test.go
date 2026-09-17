package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestRegisterHeartbeatAgainstServer(t *testing.T) {
	var gotRegister, gotBeat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workspaces/register":
			gotRegister = true
		case "/workspaces/heartbeat":
			gotBeat = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root := t.TempDir()
	d, err := New(root, srv.URL, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Register(t.Context()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := d.HeartbeatOnce(t.Context()); err != nil {
		t.Fatalf("HeartbeatOnce: %v", err)
	}
	if !gotRegister || !gotBeat {
		t.Fatalf("server saw register=%v heartbeat=%v, want both true", gotRegister, gotBeat)
	}
}

func TestNewRejectsBadRoot(t *testing.T) {
	if _, err := New("", "", "127.0.0.1:0"); err == nil {
		t.Fatal("empty root accepted")
	}
	if _, err := New(filepath.Join(t.TempDir(), "missing"), "", "127.0.0.1:0"); err == nil {
		t.Fatal("missing root accepted")
	}
}
