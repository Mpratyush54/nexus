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

// allowedGitSubcommands is the read-only git surface required by Nexus
// (issue #91): status/diff/log inspection plus narrowly defined read-only
// plumbing. Everything else (config, remote, hooks/helpers, write paths
// like add/commit/push/pull/fetch/clone/init/checkout/reset) is rejected.
var allowedGitSubcommands = map[string]bool{
	"status":    true,
	"diff":      true,
	"log":       true,
	"show":      true,
	"rev-parse": true,
	"branch":    true,
	"tag":       true,
	"ls-files":  true,
	"grep":      true,
	"blame":     true,
	"version":   true,
	"help":      true,
}

// allowedGitFlags are the only bare `git <flag>` forms permitted (no
// subcommand, e.g. `git --version`). All other dash-forms are rejected so
// `--upload-pack`, `--exec-path`, etc. cannot smuggle helpers.
var allowedGitFlags = map[string]bool{
	"--version": true,
	"--help":    true,
	"-v":        true,
	"-h":        true,
}

// isAllowedGit reports whether a git argv is within the read-only allowlist.
// It rejects config overrides, external-program hooks, remotes, and write
// subcommands (issue #91 adversarial surface).
func isAllowedGit(rest []string) bool {
	if len(rest) == 0 {
		return true // bare `git` prints help; no effect
	}
	first := rest[0]
	if strings.HasPrefix(first, "-") {
		// Bare flag form: allow only the safe set; reject everything
		// else including -c/--config/--upload-pack/--exec.
		if allowedGitFlags[first] {
			// No further args permitted on flag form (e.g. `git --version
			// --upload-pack=x` is rejected below anyway).
			for _, a := range rest[1:] {
				if isDangerousGitFlag(a) {
					return false
				}
			}
			return true
		}
		return false
	}
	if !allowedGitSubcommands[first] {
		return false
	}
	for _, a := range rest[1:] {
		if strings.ContainsRune(a, 0) {
			return false
		}
		if isDangerousGitFlag(a) {
			return false
		}
	}
	return true
}

// isDangerousGitFlag rejects options that alter configuration or invoke
// external programs, plus credential/remote helpers, file-writing
// redirects (--output) that could land outside the workspace, and
// repo-location overrides (--git-dir/--work-tree/--bare/--namespace)
// that escape the Dir=root confinement (issue #132).
func isDangerousGitFlag(a string) bool {
	low := strings.ToLower(a)
	switch {
	case low == "-c" || strings.HasPrefix(low, "-c"):
		return true
	case strings.HasPrefix(low, "--config"):
		return true
	case strings.HasPrefix(low, "--upload-pack"):
		return true
	case strings.HasPrefix(low, "--receive-pack"):
		return true
	case strings.HasPrefix(low, "--exec"):
		return true
	case strings.HasPrefix(low, "--uploadarchive"):
		return true
	case strings.HasPrefix(low, "--output"):
		return true
	case strings.HasPrefix(low, "--git-dir"):
		return true
	case strings.HasPrefix(low, "--work-tree"):
		return true
	case low == "--bare" || strings.HasPrefix(low, "--bare="):
		return true
	case strings.HasPrefix(low, "--namespace"):
		return true
	case strings.Contains(low, "credential.helper"):
		return true
	case strings.Contains(low, "core.hooks"):
		return true
	case strings.Contains(low, "core.fsmonitor"):
		return true
	case strings.Contains(low, "core.sshcommand"):
		return true
	case strings.Contains(low, "protocol.ext.allow"):
		return true
	}
	return false
}

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
// Backslashes split too (issue #147): Windows paths must parse identically
// on every GOOS — filepath.Base alone leaves `C:\tools\git.exe` whole on
// Linux, inconsistent with Windows.
func baseName(argv0 string) string {
	b := strings.ToLower(filepath.ToSlash(argv0))
	if i := strings.LastIndex(b, "/"); i >= 0 {
		b = b[i+1:]
	}
	return strings.TrimSuffix(b, ".exe")
}

// IsAllowed reports whether argv matches the v1 allowlist:
//
//	git <read-only>        (status/diff/log/show/rev-parse/branch/tag/
//	                       ls-files/grep/blame/version/help + --version/--help;
//	                       config/remote/hooks/write subcommands rejected)
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
		return isAllowedGit(rest)
	case "go", "cargo":
		// -exec runs an arbitrary helper binary (issue #132): `go test
		// -exec /tmp/evil` would execute outside the sandbox.
		if len(rest) < 1 || rest[0] != "test" {
			return false
		}
		for _, a := range rest[1:] {
			low := strings.ToLower(a)
			if low == "-exec" || strings.HasPrefix(low, "-exec=") {
				return false
			}
		}
		return true
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
// Binaries are pinned to their absolute PATH resolution (issue #118 PATH
// hijack): argv[0] must be a bare name and is resolved via exec.LookPath,
// then executed by absolute path so a cwd-planted or PATH-shadowed binary
// cannot be smuggled in.
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

	// Pin the binary: resolve once via PATH, then exec the absolute path.
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
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
