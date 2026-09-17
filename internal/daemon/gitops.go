// Git operations for the workspace daemon (issue #3).
//
// Thin wrappers over the git CLI executed with Dir pinned to the workspace
// root: GET /git/status (git status --porcelain) and GET /git/diff
// (git diff [ref]). The optional ref query is strictly validated to block
// shell/flag injection (no subprocess shell is used at all — os/exec with
// argv only). Branch/commit/dirty helpers feed register + heartbeat.
package daemon

import (
	"context"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// gitTimeout bounds git subprocesses so a hung pager/lock cannot hang the
// daemon handler. 30s is generous for status/diff on local repos.
const gitTimeout = 30 * time.Second

// refPattern allows branch names, tags, SHAs and simple revision suffixes
// (HEAD~1, HEAD^) while rejecting whitespace, shell metacharacters and
// option injection (leading '-').
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_.\-~^]*$`)

// ValidRef reports whether ref is safe to pass as a git diff argument.
// Empty means "unstaged diff against the worktree" and is always valid.
func ValidRef(ref string) bool {
	if ref == "" {
		return true
	}
	if len(ref) > 128 {
		return false
	}
	return refPattern.MatchString(ref)
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// GitStatus returns `git status --porcelain` output for root.
func GitStatus(root string) (string, error) {
	return runGit(context.Background(), root, "status", "--porcelain")
}

// GitDiff returns `git diff [--no-color [ref]]` output for root.
func GitDiff(root, ref string) (string, error) {
	args := []string{"diff", "--no-color"}
	if ref != "" {
		args = append(args, ref)
	}
	return runGit(context.Background(), root, args...)
}

// GitBranch returns the current branch name ("HEAD" when detached, "" when
// unavailable — heartbeat/register treat "" as unknown, never fatal).
func GitBranch(root string) (string, error) {
	out, err := runGit(context.Background(), root, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}

// GitCommit returns the full HEAD SHA ("" when unavailable).
func GitCommit(root string) (string, error) {
	out, err := runGit(context.Background(), root, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// GitDirty reports whether the worktree has staged or unstaged changes.
func GitDirty(root string) (bool, error) {
	out, err := GitStatus(root)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (d *Daemon) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	out, err := GitStatus(d.Root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "git status failed: "+firstLine(out))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": out})
}

func (d *Daemon) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if !ValidRef(ref) {
		writeError(w, http.StatusBadRequest, "invalid ref")
		return
	}
	out, err := GitDiff(d.Root, ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "git diff failed: "+firstLine(out))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"diff": out, "ref": ref})
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
