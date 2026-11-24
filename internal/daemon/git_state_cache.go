package daemon

// git_state_cache.go caches git inspection results per workspace so the
// heartbeat (every 30 s) and session snapshots do not spawn `git status`,
// `git rev-parse`, `git log`, `git diff` and `git ls-files` (and re-read every
// untracked file) when nothing changed.
//
// A cached result is reused while all of these hold:
//   - the workspace's project watcher (project_watcher.go) is running and
//     has seen no source change since the result was computed;
//   - .git/HEAD, the ref it names, packed-refs and the index have the same
//     size and modification time (commits, checkouts, staging);
//   - the result is younger than gitStateMaxAge, a backstop for changes the
//     watcher cannot see (untracked files in skipped folders, watch caps).
//
// Without a running watcher nothing is cached and every call runs git, as
// before.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gitStateMaxAge bounds how long a cached git result may be reused.
var gitStateMaxAge = 5 * time.Minute

// workspaceChanges counts source changes seen by project watchers, per root.
var workspaceChanges = struct {
	sync.Mutex
	gen     map[string]uint64
	watched map[string]int
}{gen: map[string]uint64{}, watched: map[string]int{}}

func workspaceKey(root string) string {
	return strings.ToLower(filepath.Clean(strings.TrimSpace(root)))
}

// noteWorkspaceChange records a source change under root.
func noteWorkspaceChange(root string) {
	k := workspaceKey(root)
	workspaceChanges.Lock()
	workspaceChanges.gen[k]++
	workspaceChanges.Unlock()
}

// setWorkspaceWatched marks root as watched (on) or not. Calls nest.
func setWorkspaceWatched(root string, on bool) {
	k := workspaceKey(root)
	workspaceChanges.Lock()
	defer workspaceChanges.Unlock()
	if on {
		workspaceChanges.watched[k]++
		workspaceChanges.gen[k]++
		return
	}
	if workspaceChanges.watched[k] <= 1 {
		delete(workspaceChanges.watched, k)
	} else {
		workspaceChanges.watched[k]--
	}
	workspaceChanges.gen[k]++
}

// workspaceChangeGen returns root's change counter and whether a watcher
// is keeping it current.
func workspaceChangeGen(root string) (uint64, bool) {
	k := workspaceKey(root)
	workspaceChanges.Lock()
	defer workspaceChanges.Unlock()
	return workspaceChanges.gen[k], workspaceChanges.watched[k] > 0
}

// gitDirs returns the git directory of root and its common directory
// (they differ for linked worktrees). ok=false when root is no checkout.
func gitDirs(root string) (gitDir, commonDir string, ok bool) {
	dotGit := filepath.Join(root, ".git")
	st, err := os.Stat(dotGit)
	if err != nil {
		return "", "", false
	}
	gitDir = dotGit
	if !st.IsDir() {
		raw, err := os.ReadFile(dotGit)
		if err != nil {
			return "", "", false
		}
		line := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(line, "gitdir:") {
			return "", "", false
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	commonDir = gitDir
	if raw, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(raw))
		if c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			commonDir = filepath.Clean(c)
		}
	}
	return gitDir, commonDir, true
}

func statStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
}

// gitStateKey fingerprints root's git state for the cache. ok=false means
// do not cache (no watcher, or not a checkout).
func gitStateKey(root string) (string, bool) {
	gen, watched := workspaceChangeGen(root)
	if !watched {
		return "", false
	}
	gitDir, commonDir, ok := gitDirs(root)
	if !ok {
		return "", false
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	headText := strings.TrimSpace(string(head))
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|%s", gen, headText, statStamp(filepath.Join(gitDir, "index")), statStamp(filepath.Join(commonDir, "packed-refs")))
	if ref, isRef := strings.CutPrefix(headText, "ref:"); isRef {
		ref = filepath.FromSlash(strings.TrimSpace(ref))
		b.WriteString("|" + statStamp(filepath.Join(commonDir, ref)))
		if gitDir != commonDir {
			b.WriteString("|" + statStamp(filepath.Join(gitDir, ref)))
		}
	}
	return b.String(), true
}

type gitCacheEntry struct {
	key   string
	at    time.Time
	value any
}

var gitResultCache = struct {
	sync.Mutex
	m map[string]gitCacheEntry
}{m: map[string]gitCacheEntry{}}

// cachedGit returns the cached value for (root, kind) when root's git state
// is unchanged, else computes it with fn. Errors are never cached.
func cachedGit[T any](root, kind string, fn func() (T, error)) (T, error) {
	key, cacheable := gitStateKey(root)
	slot := workspaceKey(root) + "\x00" + kind
	if cacheable {
		gitResultCache.Lock()
		e, ok := gitResultCache.m[slot]
		gitResultCache.Unlock()
		if ok && e.key == key && time.Since(e.at) < gitStateMaxAge {
			if v, ok := e.value.(T); ok {
				return v, nil
			}
		}
	}
	v, err := fn()
	if err != nil || !cacheable {
		return v, err
	}
	// Recompute the key: running git can refresh the index stat. A change
	// made while git ran bumps the watcher generation and misses next time.
	if after, ok := gitStateKey(root); ok {
		key = after
	}
	gitResultCache.Lock()
	gitResultCache.m[slot] = gitCacheEntry{key: key, at: time.Now(), value: v}
	gitResultCache.Unlock()
	return v, nil
}

type gitStatusResult struct {
	branch, commit string
	dirty          bool
	porcelain      string
}

// GitStatusCached is GitStatus with the workspace git-state cache.
func GitStatusCached(root string) (branch, commit string, dirty bool, porcelain string, err error) {
	res, err := cachedGit(root, "status", func() (gitStatusResult, error) {
		b, c, d, p, err := GitStatus(root)
		return gitStatusResult{branch: b, commit: c, dirty: d, porcelain: p}, err
	})
	if err != nil {
		// Same values GitStatus returns on failure.
		return "", "", false, res.porcelain, err
	}
	return res.branch, res.commit, res.dirty, res.porcelain, nil
}

// GitLogCached is GitLog with the workspace git-state cache.
func GitLogCached(root string, n int) (string, error) {
	return cachedGit(root, "log:"+itoa(n), func() (string, error) {
		return GitLog(root, n)
	})
}
