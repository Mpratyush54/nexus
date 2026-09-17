package daemon

import (
	"encoding/json"
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
