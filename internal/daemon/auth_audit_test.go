package daemon

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Audit tests for auth.go: token lifecycle + HTTP bearer gate.

func auditAuthDaemon(t *testing.T, token string) *Daemon {
	t.Helper()
	d, err := NewDaemon(t.TempDir(), token)
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	return d
}

func auditServe(d *Daemon, method, target, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
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

func TestAuditGenerateTokenLengthHex(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 (32 bytes hex)", len(tok))
	}
	raw, err := hex.DecodeString(tok)
	if err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded token = %d bytes, want 32", len(raw))
	}
}

func TestAuditGenerateTokenUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		tok, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if seen[tok] {
			t.Fatal("duplicate token generated")
		}
		seen[tok] = true
	}
}

func TestAuditTokenPathShape(t *testing.T) {
	p := TokenPath(filepath.Join("r", "oot"))
	if filepath.Base(p) != "daemon.token" {
		t.Errorf("base = %q, want daemon.token", filepath.Base(p))
	}
	if filepath.Base(filepath.Dir(p)) != ".central-memory" {
		t.Errorf("parent dir = %q, want .central-memory", filepath.Base(filepath.Dir(p)))
	}
}

func TestAuditSaveTokenRejectsBlank(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "   ", "\n\t "} {
		if err := SaveToken(root, bad); err == nil {
			t.Errorf("SaveToken(%q) accepted blank token", bad)
		}
	}
}

func TestAuditSaveTokenRoundTripAndMode(t *testing.T) {
	root := t.TempDir()
	tok, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(root, tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	back, err := LoadToken(root)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if back != tok {
		t.Fatal("token mismatch after round-trip")
	}
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are best-effort on windows")
	}
	st, err := os.Stat(TokenPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %o, want 600", st.Mode().Perm())
	}
	dirSt, err := os.Stat(filepath.Dir(TokenPath(root)))
	if err != nil {
		t.Fatal(err)
	}
	if dirSt.Mode().Perm() != 0o700 {
		t.Errorf("token dir mode = %o, want 700", dirSt.Mode().Perm())
	}
}

func TestAuditLoadTokenFailures(t *testing.T) {
	if _, err := LoadToken(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Error("LoadToken on missing file succeeded, want error")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(TokenPath(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenPath(root), []byte("  \n "), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(root); err == nil {
		t.Error("LoadToken on blank file succeeded, want error")
	}
}

func TestAuditLoadTokenTrimsWhitespace(t *testing.T) {
	root := t.TempDir()
	if err := SaveToken(root, "tok-abc"); err != nil {
		t.Fatal(err)
	}
	// Simulate editors/tools appending a trailing newline.
	raw, err := os.ReadFile(TokenPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenPath(root), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := LoadToken(root)
	if err != nil {
		t.Fatal(err)
	}
	if back != "tok-abc" {
		t.Errorf("got %q, want trimmed token", back)
	}
}

func TestAuditEnsureTokenLifecycle(t *testing.T) {
	root := t.TempDir()
	first, err := EnsureToken(root)
	if err != nil {
		t.Fatalf("EnsureToken create: %v", err)
	}
	if first == "" {
		t.Fatal("empty token created")
	}
	second, err := EnsureToken(root)
	if err != nil {
		t.Fatalf("EnsureToken reload: %v", err)
	}
	if second != first {
		t.Fatal("EnsureToken regenerated instead of reusing persisted token")
	}
	// A pre-seeded token is honored, not overwritten.
	root2 := t.TempDir()
	if err := SaveToken(root2, "seeded-token"); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureToken(root2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "seeded-token" {
		t.Errorf("got %q, want seeded token", got)
	}
}

func TestAuditBearerTokenCases(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"missing", "", ""},
		{"basic scheme", "Basic abc", ""},
		{"lowercase bearer", "bearer abc", ""},
		{"token only", "abc", ""},
		{"valid", "Bearer tok123", "tok123"},
		{"empty token", "Bearer ", ""},
		{"empty token spaces", "Bearer    ", ""},
		{"padded token trimmed", "Bearer   tok123   ", "tok123"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		if got := bearerToken(r); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAuditCheckAuthMatrix(t *testing.T) {
	d := auditAuthDaemon(t, "secret-token")
	withAuth := func(tok string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if tok != "" {
			// "NOMARK" sentinel: set a non-bearer header to test scheme rejection.
			if tok == "NOMARK" {
				r.Header.Set("Authorization", "Basic secret-token")
			} else {
				r.Header.Set("Authorization", "Bearer "+tok)
			}
		}
		return r
	}
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"correct", "secret-token", true},
		{"wrong", "wrong-token", false},
		{"prefix of token", "secret", false},
		{"token with suffix", "secret-token-x", false},
		{"empty header", "", false},
		{"wrong scheme", "NOMARK", false},
		{"different length", "short", false},
	}
	for _, tc := range cases {
		if got := d.checkAuth(withAuth(tc.token)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuditCheckAuthEmptyDaemonToken(t *testing.T) {
	d := auditAuthDaemon(t, "secret-token")
	d.Token = ""
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	if d.checkAuth(r) {
		t.Error("checkAuth passed with empty daemon token, want rejection")
	}
	// Empty bearer against empty daemon token must also fail.
	r2 := httptest.NewRequest("GET", "/", nil)
	if d.checkAuth(r2) {
		t.Error("checkAuth passed with both tokens empty, want rejection")
	}
}

func TestAuditRequireAuthGate(t *testing.T) {
	d := auditAuthDaemon(t, "audit-token")
	called := false
	h := d.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		writeJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
	})
	// Missing -> 401, handler not called.
	called = false
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing token: got %d, want 401", w.Code)
	}
	if called {
		t.Error("next handler called despite missing token")
	}
	// Wrong -> 401.
	called = false
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer nope")
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", w.Code)
	}
	if called {
		t.Error("next handler called despite wrong token")
	}
	// Correct -> 200, handler called.
	called = false
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer audit-token")
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("valid token: got %d, want 200", w.Code)
	}
	if !called {
		t.Error("next handler not called for valid token")
	}
}

// TestAuditAllEndpointsRequireAuth pins that every route — including the
// GET-allowed ones (/register, /heartbeat, /git/*) — still demands a token.
func TestAuditAllEndpointsRequireAuth(t *testing.T) {
	d := auditAuthDaemon(t, "audit-token")
	if err := os.WriteFile(filepath.Join(d.Root, "a.txt"), []byte("alpha content here"), 0o644); err != nil {
		t.Fatal(err)
	}
	routes := []struct {
		method string
		target string
		body   string
	}{
		{"POST", "/register", ""},
		{"GET", "/register", ""},
		{"POST", "/heartbeat", ""},
		{"GET", "/heartbeat", ""},
		{"POST", "/file/read", `{"path":"a.txt"}`},
		{"POST", "/file/write", `{"path":"n.txt","content":"x"}`},
		{"GET", "/git/status", ""},
		{"POST", "/git/status", ""},
		{"GET", "/git/diff", ""},
		{"POST", "/git/diff", `{"ref":""}`},
		{"GET", "/git/log", ""},
		{"POST", "/git/log", ""},
		{"POST", "/command/run", `{"cmd":"rm","args":[]}`},
	}
	for _, rt := range routes {
		if w := auditServe(d, rt.method, rt.target, rt.body, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: got %d, want 401", rt.method, rt.target, w.Code)
		}
		if w := auditServe(d, rt.method, rt.target, rt.body, "wrong"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s wrong token: got %d, want 401", rt.method, rt.target, w.Code)
		}
		// Empty "Bearer " header must also be rejected.
		r := httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body))
		r.Header.Set("Authorization", "Bearer ")
		w := httptest.NewRecorder()
		d.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s empty bearer: got %d, want 401", rt.method, rt.target, w.Code)
		}
		// A valid token must get PAST auth (any non-401 status proves it;
		// the handler itself may 400/500 when git is absent).
		if w := auditServe(d, rt.method, rt.target, rt.body, "audit-token"); w.Code == http.StatusUnauthorized {
			t.Errorf("%s %s valid token: got 401, want authenticated dispatch", rt.method, rt.target)
		}
	}
}
