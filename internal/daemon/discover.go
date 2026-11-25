package daemon

// Project discovery for capture setup: which folders on this machine have
// agent sessions, so the user can approve them with one click instead of
// hunting for paths. Read-only and bounded: only the first 64 KiB of each
// transcript is peeked for its working directory, nothing is uploaded.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DiscoveredProject is one folder with captured agent sessions.
type DiscoveredProject struct {
	Path       string         `json:"path"`
	Name       string         `json:"name"`
	Sessions   int            `json:"sessions"`
	Agents     map[string]int `json:"agents"`
	LastActive time.Time      `json:"last_active"`
}

const (
	discoverMaxFiles = 6000
	discoverTTL      = 10 * time.Minute
)

var (
	discoverMu      sync.Mutex
	discoverCache   []DiscoveredProject
	discoverAt      time.Time
	discoverRunning bool
)

// CachedDiscoveredProjects returns the last discovery result and refreshes it
// in the background when it is older than the TTL. The first call returns
// nil while the initial scan runs.
func CachedDiscoveredProjects() []DiscoveredProject {
	discoverMu.Lock()
	defer discoverMu.Unlock()
	if !discoverRunning && time.Since(discoverAt) > discoverTTL {
		discoverRunning = true
		go func() {
			result := DiscoverProjects(ResolveSources())
			discoverMu.Lock()
			discoverCache, discoverAt, discoverRunning = result, time.Now(), false
			discoverMu.Unlock()
		}()
	}
	return append([]DiscoveredProject(nil), discoverCache...)
}

// DiscoverProjects walks transcript sources and groups sessions by the git
// root (or folder) they ran in. Sorted by most recent activity.
func DiscoverProjects(sources []TranscriptSource) []DiscoveredProject {
	home, _ := os.UserHomeDir()
	byRoot := map[string]*DiscoveredProject{}
	rootOf := map[string]string{} // cwd -> resolved root ("" = rejected)
	visited := 0
	for _, src := range sources {
		if src.Format == FormatSQLite {
			continue // databases hold many projects; not peekable cheaply
		}
		for _, dir := range src.Dirs {
			_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if harvesterSkipDirs[d.Name()] {
						return filepath.SkipDir
					}
					if rel, rerr := filepath.Rel(dir, p); rerr == nil && rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxWalkDepth {
						return filepath.SkipDir
					}
					return nil
				}
				visited++
				if visited > discoverMaxFiles {
					return filepath.SkipAll
				}
				ext := strings.ToLower(filepath.Ext(p))
				if ext != ".jsonl" && ext != ".json" {
					return nil
				}
				cwd := strings.TrimSpace(PeekTranscriptCwd(p))
				if cwd == "" {
					return nil
				}
				root, seen := rootOf[cwd]
				if !seen {
					root = projectRootFor(cwd, home)
					rootOf[cwd] = root
				}
				if root == "" {
					return nil
				}
				key := strings.ToLower(root)
				proj := byRoot[key]
				if proj == nil {
					proj = &DiscoveredProject{Path: root, Name: filepath.Base(root), Agents: map[string]int{}}
					byRoot[key] = proj
				}
				proj.Sessions++
				proj.Agents[src.Agent]++
				if info, err := d.Info(); err == nil && info.ModTime().After(proj.LastActive) {
					proj.LastActive = info.ModTime()
				}
				return nil
			})
		}
	}
	out := make([]DiscoveredProject, 0, len(byRoot))
	for _, p := range byRoot {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out
}

// projectRootFor maps a session's working directory to the folder a person
// would approve: the enclosing git repository, else the folder itself.
// Missing folders, drive roots and the home directory are rejected.
func projectRootFor(cwd, home string) string {
	dir := filepath.Clean(cwd)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	for cur := dir; ; {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			dir = cur
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur || (home != "" && strings.EqualFold(cur, home)) {
			break
		}
		cur = parent
	}
	if filepath.Dir(dir) == dir || (home != "" && strings.EqualFold(dir, filepath.Clean(home))) {
		return ""
	}
	// Scratch folders (agents' own temp runs, test sandboxes) are not projects.
	for _, tmp := range discoverSkipDirs() {
		if strings.TrimSpace(tmp) != "" && tmp != "Temp" && hasPathPrefix(dir, tmp) {
			return ""
		}
	}
	return dir
}

// discoverSkipDirs lists scratch roots ignored by discovery (tests override).
var discoverSkipDirs = func() []string {
	return []string{os.TempDir(), filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp")}
}

func hasPathPrefix(path, prefix string) bool {
	p, q := strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(prefix))
	if long, err := filepath.EvalSymlinks(prefix); err == nil {
		if l := strings.ToLower(filepath.Clean(long)); strings.HasPrefix(p, l+string(filepath.Separator)) || p == l {
			return true
		}
	}
	return p == q || strings.HasPrefix(p, q+string(filepath.Separator))
}
