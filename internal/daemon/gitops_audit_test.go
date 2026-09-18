package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Audit tests for gitops.go: ref validation, arg clamping, and git-backed
// integration (skipped when git is absent).

func writeFileDirect(root, name, content string) error {
	return os.WriteFile(filepath.Join(root, name), []byte(content), 0o644)
}

func TestAuditValidRefTable(t *testing.T) {
	valid := []string{
		"",
		"main",
		"HEAD",
		"HEAD~1",
		"HEAD^",
		"HEAD~2",
		"v1.0.0",
		"abc123def",
		"feature/foo",
		"main..feature",
		"main...feature",
		"release-1.2",
		"a_b.c-d/e",
	}
	for _, r := range valid {
		if !validRef(r) {
			t.Errorf("validRef(%q) = false, want true", r)
		}
	}
	invalid := []string{
		"-badopt",
		"--upload-pack=evil",
		".dotstart",
		"../escape",
		"has space",
		"tab\there",
		"new\nline",
		"semi;colon",
		"pipe|sym",
		"amp&ersand",
		"semi;colon2",
		"lt<gt",
		"paren(x)",
		"brace{x}",
		"bang!x",
		"star*x",
		"quest?x",
		"colon:x",
		"back\\slash",
		"single'quote",
		`double"quote`,
		"back`tick",
		"dollar$x",
		"subst$(x)",
		"tilde~only",
		"at@{1}",
	}
	for _, r := range invalid {
		if validRef(r) {
			t.Errorf("validRef(%q) = true, want false", r)
		}
	}
}

func TestAuditBadRefErrorShape(t *testing.T) {
	var err error = &BadRefError{Ref: "a;b"}
	if !strings.Contains(err.Error(), "a;b") {
		t.Errorf("BadRefError %q does not mention the ref", err.Error())
	}
	var bref *BadRefError
	if !errors.As(err, &bref) {
		t.Error("BadRefError not unwrappable via errors.As")
	}
}

func TestAuditItoaTable(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 7: "7", 20: "20", 100: "100", -3: "-3"}
	for n, want := range cases {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestAuditGitTimeoutValue(t *testing.T) {
	if gitTimeout != 30*time.Second {
		t.Errorf("gitTimeout = %v, want 30s", gitTimeout)
	}
}

func TestAuditGitDiffRejectsBadRefWithoutGit(t *testing.T) {
	// Ref validation happens before any exec: no git needed.
	for _, bad := range []string{"-badopt", "--upload-pack=./evil", "a b", "a;b", "-h", ".hidden", "a:b"} {
		_, err := GitDiff(t.TempDir(), bad)
		var bref *BadRefError
		if !errors.As(err, &bref) {
			t.Errorf("GitDiff(%q) err = %v, want *BadRefError", bad, err)
		}
	}
}

func auditInitRepo(t *testing.T, root string) bool {
	t.Helper()
	run := func(args ...string) bool {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("git %v: %v (%s)", args, err, strings.TrimSpace(string(out)))
			return false
		}
		return true
	}
	if !run("init") {
		return false
	}
	if !run("config", "user.email", "audit@example.com") {
		return false
	}
	if !run("config", "user.name", "audit") {
		return false
	}
	return true
}

func auditCommitFile(t *testing.T, root, name, content, msg string) bool {
	t.Helper()
	if err := writeFileDirect(root, name, content); err != nil {
		t.Logf("write: %v", err)
		return false
	}
	run := func(args ...string) bool {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("git %v: %v (%s)", args, err, strings.TrimSpace(string(out)))
			return false
		}
		return true
	}
	return run("add", name) && run("commit", "-m", msg)
}

func TestAuditGitStatusTempRepoLifecycle(t *testing.T) {
	if !auditHaveGit() {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if !auditInitRepo(t, root) {
		t.Skip("git init failed")
	}
	if !auditCommitFile(t, root, "f.txt", "commit one content", "first commit") {
		t.Skip("git commit failed")
	}
	branch, commit, dirty, porcelain, err := GitStatus(root)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}
	if strings.TrimSpace(branch) == "" {
		t.Error("empty branch in fresh repo")
	}
	if strings.TrimSpace(commit) == "" {
		t.Error("empty HEAD commit in fresh repo")
	}
	if dirty {
		t.Error("fresh commit reported dirty")
	}
	if strings.TrimSpace(porcelain) != "" {
		t.Errorf("fresh porcelain = %q, want empty", porcelain)
	}
	// Dirty the tree: status must flip dirty with non-empty porcelain.
	if err := writeFileDirect(root, "f.txt", "modified content here"); err != nil {
		t.Fatal(err)
	}
	_, _, dirty, porcelain, err = GitStatus(root)
	if err != nil {
		t.Fatalf("GitStatus dirty: %v", err)
	}
	if !dirty {
		t.Error("modified tree reported clean")
	}
	if strings.TrimSpace(porcelain) == "" {
		t.Error("dirty tree has empty porcelain")
	}
}

func TestAuditGitDiffTempRepo(t *testing.T) {
	if !auditHaveGit() {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if !auditInitRepo(t, root) {
		t.Skip("git init failed")
	}
	if !auditCommitFile(t, root, "f.txt", "v1 content", "first commit") {
		t.Skip("git commit failed")
	}
	if err := writeFileDirect(root, "f.txt", "v2 content"); err != nil {
		t.Fatal(err)
	}
	diff, err := GitDiff(root, "")
	if err != nil {
		t.Fatalf("GitDiff(empty ref): %v", err)
	}
	if !strings.Contains(diff, "f.txt") {
		t.Errorf("diff missing filename: %q", diff)
	}
	head, err := GitDiff(root, "HEAD")
	if err != nil {
		t.Fatalf("GitDiff(HEAD): %v", err)
	}
	if !strings.Contains(head, "f.txt") {
		t.Errorf("HEAD diff missing filename: %q", head)
	}
}

func TestAuditGitLogClampInTempRepo(t *testing.T) {
	if !auditHaveGit() {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if !auditInitRepo(t, root) {
		t.Skip("git init failed")
	}
	if !auditCommitFile(t, root, "f.txt", "log content", "audit log message") {
		t.Skip("git commit failed")
	}
	// n<=0 defaults to 20, n>100 clamps to 100: both must succeed.
	for _, n := range []int{0, -5, 1, 20, 500} {
		out, err := GitLog(root, n)
		if err != nil {
			t.Fatalf("GitLog(%d): %v", n, err)
		}
		if n == 1 && !strings.Contains(out, "audit log message") {
			t.Errorf("GitLog(1) missing commit message: %q", out)
		}
	}
}

func TestAuditGitOutsideRepoErrors(t *testing.T) {
	// Outside a repo git fails (or the binary is missing): either way an
	// error must surface, never silent success with empty output.
	root := t.TempDir()
	if _, _, _, _, err := GitStatus(root); err == nil {
		t.Error("GitStatus outside repo succeeded, want error")
	}
	if _, err := GitLog(root, 5); err == nil {
		t.Error("GitLog outside repo succeeded, want error")
	}
	if _, err := GitDiff(root, ""); err == nil {
		t.Error("GitDiff outside repo succeeded, want error")
	}
}
