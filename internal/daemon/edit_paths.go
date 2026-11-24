package daemon

// edit_paths.go maps a path an agent edited to the checkout that holds it.
//
// Agents often run in one folder and edit another checkout of the same
// repository: a second clone or a git worktree (a chat started in D:\a
// editing D:\b\internal\x.go, where D:\b and this workspace share a
// remote). The chat is attributed to this workspace, but its absolute paths
// are outside root, so they used to be dropped and the edits never reached
// code links or line history. A path outside root now counts when the git
// checkout that holds it is the same repository as root (same origin, or
// the same root commit when either has no origin); it is reported
// repo-relative and its hunks are located in that checkout's file, the one
// the agent actually changed. Relative paths resolve against the
// transcript's working directory when it is known.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// editLoc is an edited file: base is the checkout it is in, rel its
// repo-relative slash path.
type editLoc struct {
	base, rel string
}

const editPathCacheMax = 1024

var editPathCache = struct {
	sync.Mutex
	top map[string]string // dir -> git toplevel ("" when none)
	cwd map[string]string // transcript -> working directory ("" when none)
}{top: map[string]string{}, cwd: map[string]string{}}

// gitTopFor returns the nearest folder at or above dir holding .git (a
// directory, or a file for worktrees), or "" when there is none.
func gitTopFor(dir string) string {
	dir = filepath.Clean(dir)
	key := strings.ToLower(dir)
	editPathCache.Lock()
	top, ok := editPathCache.top[key]
	editPathCache.Unlock()
	if ok {
		return top
	}
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			top = d
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	editPathCache.Lock()
	if len(editPathCache.top) >= editPathCacheMax {
		editPathCache.top = map[string]string{}
	}
	editPathCache.top[key] = top
	editPathCache.Unlock()
	return top
}

// transcriptCwd returns the working directory recorded in a transcript
// (cached per transcript path).
func transcriptCwd(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	editPathCache.Lock()
	cwd, ok := editPathCache.cwd[path]
	editPathCache.Unlock()
	if ok {
		return cwd
	}
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
		cwd = strings.TrimSpace(PeekTranscriptCwd(path))
		if cwd != "" && !filepath.IsAbs(cwd) {
			cwd = ""
		}
	}
	editPathCache.Lock()
	if len(editPathCache.cwd) >= editPathCacheMax {
		editPathCache.cwd = map[string]string{}
	}
	editPathCache.cwd[path] = cwd
	editPathCache.Unlock()
	return cwd
}

// eventCwd is the working directory of the transcript an event came from.
func eventCwd(ev Event) string {
	if ev.Payload == nil {
		return ""
	}
	p, _ := ev.Payload["path"].(string)
	return transcriptCwd(p)
}

// normalizeOrigin reduces a git remote URL to host/owner/repo, lower case
// ("git@github.com:A/b.git" and "https://github.com/a/b" are equal).
func normalizeOrigin(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else if at := strings.Index(u, "@"); at >= 0 {
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	if at := strings.LastIndex(u, "@"); at >= 0 {
		u = u[at+1:]
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return strings.TrimSuffix(u, "/")
}

// sameRepository reports whether two checkouts are the same repository.
func sameRepository(a, b string) bool {
	if strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	oa, ra := FingerprintOf(a)
	ob, rb := FingerprintOf(b)
	if na, nb := normalizeOrigin(oa), normalizeOrigin(ob); na != "" && nb != "" {
		return na == nb
	}
	return ra != "" && ra == rb
}

// resolveEditPath maps a raw edited path to its checkout and repo-relative
// path (see the file comment). cwd ("" when unknown) is the working
// directory relative paths are taken from.
func resolveEditPath(root, cwd, p string) (editLoc, bool) {
	p = strings.TrimSpace(p)
	root = strings.TrimSpace(root)
	if p == "" || root == "" {
		return editLoc{}, false
	}
	if !filepath.IsAbs(p) {
		dir := root
		if cwd != "" && filepath.IsAbs(cwd) {
			dir = cwd
		}
		p = filepath.Join(dir, p)
	}
	if rel, ok := changedRelPath(root, p); ok {
		return editLoc{base: root, rel: rel}, true
	}
	if inside, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p)); err == nil {
		if inside = filepath.ToSlash(inside); inside != ".." && !strings.HasPrefix(inside, "../") {
			return editLoc{}, false // inside root but ignored
		}
	}
	// Another checkout counts only when root is itself a checkout's top,
	// so both paths are relative to the same repository root.
	top := gitTopFor(filepath.Dir(p))
	if top == "" || !strings.EqualFold(gitTopFor(root), filepath.Clean(root)) || !sameRepository(root, top) {
		return editLoc{}, false
	}
	rel, ok := changedRelPath(top, p)
	if !ok {
		return editLoc{}, false
	}
	return editLoc{base: top, rel: rel}, true
}
