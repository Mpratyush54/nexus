// Git inspection helpers for the workspace daemon (stdlib os/exec only).
package daemon

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"central-memory/internal/project"
)

// gitTimeout bounds git inspection calls so a hung git never stalls the daemon.
const gitTimeout = 30 * time.Second

// refPattern allows plain refs/tags/SHAs plus "~", "^", and "/" range
// syntax (e.g. HEAD~1, main...feature). Anything else — flags, shell
// metacharacters, whitespace — is rejected to prevent argument injection.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+(?:[~^]+[0-9]*)?(?:\.\.\.?[A-Za-z0-9_.\-/~^]*)?$`)

// validRef reports whether ref is safe to pass to git as a positional arg.
func validRef(ref string) bool {
	if ref == "" {
		return true
	}
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, ".") {
		return false
	}
	if strings.ContainsAny(ref, " \t\n\r\"'`$|&;<>(){}!*?\\:") {
		return false
	}
	return refPattern.MatchString(ref)
}

func runGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), ctx.Err()
	}
	return string(out), err
}

// GitStatus returns branch, HEAD commit, dirty flag, and porcelain output.
func GitStatus(root string) (branch, commit string, dirty bool, porcelain string, err error) {
	porcelain, err = runGit(root, "status", "--porcelain")
	if err != nil {
		return "", "", false, porcelain, err
	}
	dirty = strings.TrimSpace(porcelain) != ""

	branch, berr := runGit(root, "rev-parse", "--abbrev-ref", "HEAD")
	branch = strings.TrimSpace(branch)
	if berr != nil {
		branch = ""
	}
	commit, cerr := runGit(root, "rev-parse", "HEAD")
	commit = strings.TrimSpace(commit)
	if cerr != nil {
		commit = ""
	}
	return branch, commit, dirty, porcelain, nil
}

// GitDiff returns `git diff` or `git diff <ref>` output.
func GitDiff(root, ref string) (string, error) {
	if !validRef(ref) {
		return "", &BadRefError{Ref: ref}
	}
	if ref == "" {
		return runGit(root, "diff")
	}
	return runGit(root, "diff", ref)
}

// BadRefError is returned for unsafe git ref arguments.
type BadRefError struct{ Ref string }

func (e *BadRefError) Error() string { return "daemon: invalid git ref: " + e.Ref }

// GitLog returns the last n one-line log entries (clamped to 1..100).
func GitLog(root string, n int) (string, error) {
	if n <= 0 {
		n = 20
	}
	if n > 100 {
		n = 100
	}
	return runGit(root, "log", "--oneline", "-n", itoa(n))
}

// FingerprintOf reuses internal/project identity resolution.
func FingerprintOf(root string) (origin, rootCommit string) {
	return project.Fingerprint(root)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
