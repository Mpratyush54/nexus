package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAllowedCommand(t *testing.T) {
	allowed := []struct {
		cmd  string
		args []string
	}{
		{"git", nil},
		{"git", []string{"status", "--porcelain"}},
		{"go", []string{"test", "./..."}},
		{"npm", []string{"test"}},
		{"pytest", nil},
		{"pytest", []string{"-q"}},
		{"cargo", []string{"test"}},
	}
	for _, a := range allowed {
		if !IsAllowedCommand(a.cmd, a.args) {
			t.Errorf("IsAllowedCommand(%q, %v) = false, want true", a.cmd, a.args)
		}
	}
	denied := []struct {
		cmd  string
		args []string
	}{
		{"rm", []string{"-rf", "/"}},
		{"curl", []string{"http://evil.example"}},
		{"sh", []string{"-c", "echo hi"}},
		{"cmd", []string{"/c", "dir"}},
		{"powershell", []string{"-Command", "ls"}},
		{"go", []string{"run", "."}},
		{"go", []string{"build", "./..."}},
		{"go", nil},
		{"npm", []string{"install"}},
		{"npm", []string{"exec", "x"}},
		{"cargo", []string{"run"}},
		{"cargo", []string{"build"}},
		{"/bin/git", nil},
		{"git.exe", nil},
		{"pytest;rm", nil},
		{"", nil},
	}
	for _, a := range denied {
		if IsAllowedCommand(a.cmd, a.args) {
			t.Errorf("IsAllowedCommand(%q, %v) = true, want false", a.cmd, a.args)
		}
	}
}

func TestCommandRunHTTPAllowlist(t *testing.T) {
	d := newTestDaemon(t)
	denied := []string{
		`{"cmd":"rm","args":["-rf","/"]}`,
		`{"cmd":"go","args":["run","."]}`,
		`{"cmd":"curl","args":["http://example.com"]}`,
		`{"cmd":"sh","args":["-c","id"]}`,
	}
	for _, body := range denied {
		req := authReq(t, d, http.MethodPost, "/command/run", body)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: got %d, want 400", body, rec.Code)
		}
	}
}

func TestRunCommandGitVersion(t *testing.T) {
	d := newTestDaemon(t)
	code, out, err := RunCommand(t.Context(), d.Root, "git", []string{"version"})
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	if code != 0 || out == "" {
		t.Fatalf("git version: code=%d out=%q", code, out)
	}
	if _, _, err := RunCommand(t.Context(), d.Root, "rm", []string{"-rf"}); err == nil {
		t.Fatal("non-allowlisted command executed")
	} else if _, ok := err.(*AllowlistError); !ok {
		t.Fatalf("wrong error type: %T", err)
	}
}
