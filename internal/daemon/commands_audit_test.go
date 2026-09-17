package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Audit tests for commands.go: allowlist matrix + RunCommand behavior.

func TestAuditIsAllowedMatrix(t *testing.T) {
	allowed := [][]string{
		{"git", "status"},
		{"git", "--version"},
		{"git", "log", "--oneline"},
		{"git", "rev-parse", "HEAD"},
		{"GIT", "status"},
		{"git.exe", "diff"},
		{"GIT.EXE", "diff"},
		{"go", "test", "./..."},
		{"go", "test"},
		{"go.EXE", "test", "./..."},
		{"npm", "test"},
		{"npm", "test", "--", "--watch"},
		{"pytest", "-q"},
		{"pytest"},
		{"PYTEST"},
		{"python", "-m", "pytest", "-q"},
		{"python3", "-m", "pytest"},
		{"cargo", "test"},
		{"cargo", "test", "--all"},
		// Flag passthrough: only argv[0]/first-arg shape is gated.
		{"git", "--upload-pack=evil"},
		{"go", "test", "-run", "TestX"},
	}
	for _, a := range allowed {
		if !IsAllowed(a) {
			t.Errorf("IsAllowed(%v) = false, want true", a)
		}
	}
	denied := [][]string{
		{},
		{"rm", "-rf", "/"},
		{"npx", "jest"},
		{"npx", "test"},
		{"node", "test"},
		{"go", "build", "./..."},
		{"go", "run", "main.go"},
		{"go"},
		{"npm", "install"},
		{"npm", "run", "test"},
		{"npm"},
		{"cargo", "run"},
		{"cargo", "build"},
		{"cargo"},
		{"python", "-c", "evil()"},
		{"python", "test"},
		{"python", "-m", "unittest"},
		{"python3", "-m", "unittest"},
		{"pytest-xdist"},
		// Bare-name enforcement: any path separator in argv[0] is rejected.
		{"./evil"},
		{`.\evil`},
		{"C:\\tools\\evil.exe"},
		{"/usr/bin/git"},
		{"sub/dir/git"},
		{"git/status"},
		// Shell metachars in argv[0] never match a bare allowlisted name.
		{"git;rm", "-rf"},
		{"git|cat"},
		{"git&&rm"},
		{"go;test"},
		// NUL bytes are always rejected.
		{"git\x00", "status"},
		{"go", "test\x00"},
	}
	for _, a := range denied {
		if IsAllowed(a) {
			t.Errorf("IsAllowed(%v) = true, want false", a)
		}
	}
}

func TestAuditIsAllowedNpxDenied(t *testing.T) {
	// The v1 allowlist has no npx entry: even "npx test" stays denied so a
	// fetched package can never be executed implicitly.
	for _, a := range [][]string{{"npx", "test"}, {"npx", "pytest"}, {"npx"}} {
		if IsAllowed(a) {
			t.Errorf("IsAllowed(%v) = true, want false (npx not allowlisted)", a)
		}
	}
}

func TestAuditBaseNameNormalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git", "git"},
		{"GIT", "git"},
		{"git.exe", "git"},
		{"GIT.EXE", "git"},
		{"Go", "go"},
		{`C:\tools\GIT.EXE`, "git"},
		{"/usr/bin/git", "git"},
	}
	for _, tc := range cases {
		if got := baseName(tc.in); got != tc.want {
			t.Errorf("baseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuditCommandLimitsConst(t *testing.T) {
	if CommandTimeout != 60*time.Second {
		t.Errorf("CommandTimeout = %v, want 60s", CommandTimeout)
	}
	if MaxCommandOutput != 32<<10 {
		t.Errorf("MaxCommandOutput = %d, want %d", MaxCommandOutput, 32<<10)
	}
}

func TestAuditRunCommandRejectsNonAllowlisted(t *testing.T) {
	root := t.TempDir()
	for _, argv := range [][]string{
		{},
		{"rm", "-rf", "/"},
		{"go", "build", "./..."},
		{"./evil"},
		{"npx", "jest"},
	} {
		res, err := RunCommand(root, argv)
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("RunCommand(%v) err = %v, want ErrNotAllowed", argv, err)
		}
		if res.Command != "" {
			t.Errorf("RunCommand(%v) echoed command %q on rejection", argv, res.Command)
		}
	}
}

func TestAuditRunCommandMissingBinary(t *testing.T) {
	// Point PATH at an empty dir so the allowlisted binary cannot resolve.
	// This exercises the "binary missing" branch without waiting on timeouts.
	t.Setenv("PATH", t.TempDir())
	_, err := RunCommand(t.TempDir(), []string{"git", "--version"})
	if err == nil {
		t.Fatal("expected missing-binary error, got nil")
	}
	if errors.Is(err, ErrNotAllowed) {
		t.Fatalf("got ErrNotAllowed, want a resolution error: %v", err)
	}
}

func auditHaveGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestAuditRunCommandGitVersion(t *testing.T) {
	if !auditHaveGit() {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	res, err := RunCommand(root, []string{"git", "--version"})
	if err != nil {
		t.Fatalf("RunCommand git --version: %v", err)
	}
	if res.Command != "git" {
		t.Errorf("Command = %q, want git", res.Command)
	}
	if len(res.Args) != 1 || res.Args[0] != "--version" {
		t.Errorf("Args = %v, want [--version]", res.Args)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Output, "git version") {
		t.Errorf("Output missing version string: %q", res.Output)
	}
	if res.Truncated {
		t.Error("small output flagged truncated")
	}
}

func TestAuditRunCommandNonZeroExit(t *testing.T) {
	if !auditHaveGit() {
		t.Skip("git not on PATH")
	}
	// A failing git invocation surfaces the exit code with a nil error
	// (only missing binaries/timeouts return err).
	res, err := RunCommand(t.TempDir(), []string{"git", "rev-parse", "--verify", "audit-ref-that-does-not-exist-xyz"})
	if err != nil {
		t.Fatalf("failing git should report via ExitCode, got err: %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0 for bogus ref, want nonzero (output %q)", res.Output)
	}
}

func TestAuditRunCommandTruncatesLargeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-executable PATH stub needs a shell script host; windows uses truncation constants only")
	}
	// Fake pytest that prints 64KB so RunCommand must truncate at 32KB.
	dir := t.TempDir()
	stub := filepath.Join(dir, "pytest")
	script := "#!/bin/sh\npython3 -c \"import sys; sys.stdout.write('x'*65536)\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	res, err := RunCommand(t.TempDir(), []string{"pytest", "-q"})
	if err != nil {
		t.Fatalf("RunCommand fake pytest: %v", err)
	}
	if !res.Truncated {
		t.Error("64KB output not flagged truncated")
	}
	if len(res.Output) > MaxCommandOutput+64 {
		t.Errorf("output len %d exceeds cap %d + marker", len(res.Output), MaxCommandOutput)
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Error("output missing truncation marker")
	}
}
