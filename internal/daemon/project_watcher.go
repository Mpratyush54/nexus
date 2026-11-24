package daemon

// project_watcher.go watches an approved workspace's source tree for file
// changes and feeds them to a FilesChangedReporter. It is separate from the
// SharedScanner (which watches agent transcript folders) and from Watcher
// (which polls instruction files).
//
// Memory: on Windows fsnotify allocates one ReadDirectoryChangesW buffer per
// watched directory (64 KiB by default), so this watcher uses 4 KiB buffers
// and caps the number of watched directories. Ignored trees (.git,
// node_modules, build output, agent state) are never watched.
//
// File descriptors: on macOS (and the BSDs) fsnotify uses kqueue, which
// cannot watch a tree. It opens one descriptor per watched directory and
// one more for every entry inside it, so 1500 folders of a source tree can
// mean tens of thousands of open files, and the process (the whole app,
// when the core runs as a library) then fails to open sockets, databases
// and transcripts. There the watcher also keeps a descriptor budget; past
// it the tree counts as capped and git results are simply not cached.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/fsnotify/fsnotify"

	"central-memory/internal/platform"
)

const (
	projectWatchMaxDirs = 1500
	projectWatchDepth   = 16
	projectWatchBuffer  = 4096
	// kqueueWatchBudget bounds the descriptors one project watcher may hold
	// where each watched entry costs one (macOS, BSD).
	kqueueWatchBudget = 4096
)

// kqueueBudget keeps one project watcher to at most a quarter of the
// process's soft descriptor limit (and never above kqueueWatchBudget), so
// sockets, databases and transcript reads always have room.
func kqueueBudget(softLimit uint64) int {
	if softLimit == 0 {
		return kqueueWatchBudget
	}
	if q := softLimit / 4; q < kqueueWatchBudget {
		if q < 16 {
			q = 16
		}
		return int(q)
	}
	return kqueueWatchBudget
}

// watchCostsPerEntry reports whether fsnotify spends a file descriptor on
// every entry of a watched directory (the kqueue backend).
var watchCostsPerEntry = func() bool {
	switch runtime.GOOS {
	case "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		return true
	}
	return false
}()

// projectWatcher adds fsnotify watches for root's source directories.
type projectWatcher struct {
	root    string
	w       *fsnotify.Watcher
	watched map[string]bool
	capped  bool
	// handles approximates the descriptors held (kqueue platforms only).
	handles int
	budget  int
}

func (p *projectWatcher) capAt(reason string) {
	if !p.capped {
		p.capped = true
		log.Printf("daemon: project watcher: %s %s", p.root, reason)
	}
}

func (p *projectWatcher) addTree(dir string) {
	rel, err := filepath.Rel(p.root, dir)
	if err != nil || strings.HasPrefix(filepath.ToSlash(rel), "../") || rel == ".." {
		return
	}
	depth := 0
	if rel != "." {
		depth = strings.Count(filepath.ToSlash(rel), "/") + 1
	}
	var walk func(string, int)
	walk = func(d string, level int) {
		if len(p.watched) >= projectWatchMaxDirs {
			p.capAt(fmt.Sprintf("has more than %d folders; watching the first %d", projectWatchMaxDirs, projectWatchMaxDirs))
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				// Out of descriptors (EMFILE) or unreadable: this part of
				// the tree goes unwatched, so cached git state would be stale.
				p.capAt(fmt.Sprintf("cannot read %s: %v", d, err))
			}
			return
		}
		key := strings.ToLower(filepath.Clean(d))
		if !p.watched[key] {
			if p.budget > 0 {
				cost := 1 + len(entries)
				if p.handles+cost > p.budget {
					p.capAt(fmt.Sprintf("needs more than %d open files to watch; watching part of it", p.budget))
					return
				}
				p.handles += cost
			}
			if err := p.w.AddWith(d, fsnotify.WithBufferSize(projectWatchBuffer)); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					p.capAt(fmt.Sprintf("cannot watch %s: %v", d, err))
				}
				return
			}
			p.watched[key] = true
		}
		if level >= projectWatchDepth {
			return
		}
		for _, e := range entries {
			// DirEntry.IsDir is false for symlinks and junctions: never
			// follow them out of the workspace.
			if !e.IsDir() || changedSkipDirs[e.Name()] || changedSkipDirs[strings.ToLower(e.Name())] {
				continue
			}
			walk(filepath.Join(d, e.Name()), level+1)
		}
	}
	walk(dir, depth)
}

// runProjectWatcher watches root until ctx ends, passing changed files to
// rep. paused (optional) suppresses reporting while capture is paused. It
// never blocks on the reporter: Note only takes a short lock.
func runProjectWatcher(ctx context.Context, root string, rep *FilesChangedReporter, paused func() bool) {
	if rep == nil || strings.TrimSpace(root) == "" {
		return
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("daemon: project watcher: %v", err)
		return
	}
	defer w.Close()
	pw := &projectWatcher{root: filepath.Clean(root), w: w, watched: map[string]bool{}}
	if watchCostsPerEntry {
		pw.budget = kqueueBudget(platform.FileLimit())
	}
	pw.addTree(pw.root)
	// A capped tree misses changes: then git results are not cached
	// (git_state_cache.go) and every caller runs git as before.
	registered := !pw.capped
	if registered {
		setWorkspaceWatched(pw.root, true)
	}
	defer func() {
		if registered {
			setWorkspaceWatched(pw.root, false)
		}
	}()

	loggedErr := false
	for {
		if registered && pw.capped {
			registered = false
			setWorkspaceWatched(pw.root, false)
		}
		select {
		case <-ctx.Done():
			return
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			// Lost notifications (overflow): drop cached git results.
			noteWorkspaceChange(pw.root)
			if !loggedErr && !errors.Is(err, fsnotify.ErrEventOverflow) {
				loggedErr = true
				log.Printf("daemon: project watcher: %v", err)
			}
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				key := strings.ToLower(filepath.Clean(ev.Name))
				if pw.watched[key] {
					noteWorkspaceChange(pw.root)
					delete(pw.watched, key)
					_ = w.Remove(ev.Name)
					continue
				}
			}
			if _, keep := changedRelPath(pw.root, ev.Name); !keep {
				continue
			}
			// Invalidate cached git status and uncommitted diff.
			noteWorkspaceChange(pw.root)
			if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if st, err := os.Lstat(ev.Name); err == nil && st.IsDir() {
					if ev.Op&fsnotify.Create != 0 {
						pw.addTree(ev.Name)
					}
					continue // directory mtime churn is not a file change
				}
			}
			if paused != nil && paused() {
				continue
			}
			rep.Note(ev.Name)
		}
	}
}
