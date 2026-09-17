package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(dir+"/a.txt", []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestGitStatusCleanAndDirty(t *testing.T) {
	dir := initGitRepo(t)
	out, err := GitStatus(dir)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}
	if out != "" {
		t.Fatalf("clean repo status = %q, want empty", out)
	}
	if dirty, _ := GitDirty(dir); dirty {
		t.Fatal("clean repo reported dirty")
	}
	if err := os.WriteFile(dir+"/a.txt", []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = GitStatus(dir)
	if err != nil {
		t.Fatalf("GitStatus dirty: %v", err)
	}
	if out == "" {
		t.Fatal("dirty repo status empty")
	}
	if dirty, _ := GitDirty(dir); !dirty {
		t.Fatal("dirty repo reported clean")
	}
	if b, err := GitBranch(dir); err != nil || b == "" {
		t.Fatalf("branch=%q err=%v", b, err)
	}
	if c, err := GitCommit(dir); err != nil || len(c) < 7 {
		t.Fatalf("commit=%q err=%v", c, err)
	}
}

func TestGitDiffEndpoint(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.WriteFile(dir+"/a.txt", []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := New(dir, "", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := authReq(t, d, http.MethodGet, "/git/diff", "")
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff: got %d", rec.Code)
	}

	// Malicious ref → 400, never reaches the shell (no shell is used at all).
	for _, bad := range []string{
		"; rm -rf /",
		"| cat /etc/passwd",
		"$(whoami)",
		"-uploader",
		"HEAD;cat",
		"a b",
	} {
		// Build the request via escaped query values (raw targets with
		// spaces/metachars would not parse as URLs).
		rr := httptest.NewRequest(http.MethodGet, "/git/diff", nil)
		rr.Header.Set("Authorization", "Bearer "+d.Token)
		q := rr.URL.Query()
		q.Set("ref", bad)
		rr.URL.RawQuery = q.Encode()
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, rr)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("ref %q: got %d, want 400", bad, rec.Code)
		}
	}
}

func TestValidRef(t *testing.T) {
	for _, ok := range []string{"", "HEAD", "main", "feature/x-1.2", "abc123", "HEAD~1", "v1.0.0"} {
		if !ValidRef(ok) {
			t.Errorf("ValidRef(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"-x", ";rm", "a b", "a|b", "a&b", "$x", "`x`", "a:b"} {
		if ValidRef(bad) {
			t.Errorf("ValidRef(%q) = true, want false", bad)
		}
	}
}
