// Allowlisted command execution for the workspace daemon.
//
// v1 allowlist (per Phase 1.3): git, go test, npm test, pytest, cargo test.
// Everything runs with a 60s timeout via os/exec, without a shell, so no
// shell metacharacters can ever be interpreted. Output is capped to keep
// the daemon from buffering unbounded test logs.
package daemon

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandTimeout bounds every allowlisted command (60s, per Phase 1.3).
const CommandTimeout = 60 * time.Second

// MaxCommandOutput caps captured command output (32KB, truncated).
const MaxCommandOutput = 32 << 10

// ErrNotAllowed is returned for commands outside the allowlist.
var ErrNotAllowed = errors.New("daemon: command not allowlisted")

// CommandResult carries the outcome of an allowlisted command run.
type CommandResult struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	ExitCode  int      `json:"exit_code"`
	Output    string   `json:"output"`
	Truncated bool     `json:"truncated"`
}

// baseName normalizes a binary name: lowercased, ".exe" trimmed, no dirs.
func baseName(argv0 string) string {
	b := strings.ToLower(filepath.Base(argv0))
	return strings.TrimSuffix(b, ".exe")
}

// IsAllowed reports whether argv matches the v1 allowlist:
//
//	git [...]              (any git subcommand; no shell is involved)
//	go test [...]          (exactly "test" as first go arg)
//	npm test [...]         (exactly "test" as first npm arg)
//	pytest [...]           (bare pytest binary)
//	python -m pytest [...] (module invocation)
//	cargo test [...]       (exactly "test" as first cargo arg)
func IsAllowed(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return false
		}
	}
	// Force bare binary names: no path separators, so callers cannot
	// smuggle in an arbitrary binary via "C:\tools\evil.exe".
	if strings.ContainsAny(argv[0], `/\`) {
		return false
	}
	base := baseName(argv[0])
	rest := argv[1:]
	switch base {
	case "git":
		return true
	case "go", "cargo":
		return len(rest) >= 1 && rest[0] == "test"
	case "npm":
		return len(rest) >= 1 && rest[0] == "test"
	case "pytest":
		return true
	case "python", "python3":
		return len(rest) >= 2 && rest[0] == "-m" && rest[1] == "pytest"
	}
	return false
}

// RunCommand executes an allowlisted command in root with a 60s timeout.
func RunCommand(root string, argv []string) (CommandResult, error) {
	res := CommandResult{}
	if len(argv) == 0 {
		return res, ErrNotAllowed
	}
	if !IsAllowed(argv) {
		return res, ErrNotAllowed
	}
	res.Command = argv[0]
	res.Args = append([]string(nil), argv[1:]...)

	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return res, ctx.Err()
	}
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else {
			// Binary missing etc: surface the error, no exit code.
			return res, err
		}
	}
	res.ExitCode = exit
	if len(out) > MaxCommandOutput {
		res.Output = string(out[:MaxCommandOutput]) + "\n...[truncated]..."
		res.Truncated = true
	} else {
		res.Output = string(out)
	}
	return res, nil
}
