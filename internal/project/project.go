// Package project detects leaf projects under the configured project roots
// (platform.ProjectRoots with a Windows-only D:\ default, Issue #111). A
// top-level folder like <root>/gitlab-test can hold several real repos —
// the folder itself is not the project, each repo underneath is. A
// directory is a leaf project when it carries a repo marker (.git,
// package.json, go.mod, ...). A top-level dir without markers but with
// marked children contributes each marked child as "parent/child". Scan
// depth is capped at 2 to stay fast. OS-level dirs ($RECYCLE.BIN, ...)
// are never projects.
package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"central-memory/internal/platform"
)

// Markers that make a directory a project root.
var Markers = []string{
	".git", "package.json", "go.mod", "pyproject.toml", "setup.py",
	"Cargo.toml", "pom.xml", "build.gradle", "composer.json",
	"*.sln", "Gemfile", "pubspec.yaml",
}

var systemDirs = map[string]bool{
	"$recycle.bin": true, "system volume information": true,
	"program files": true, "windowsapps": true, "recovery": true, "system.sav": true,
}

// SystemDir reports OS-level directories that are never projects.
func SystemDir(name string) bool { return systemDirs[strings.ToLower(name)] }

func hasMarker(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		for _, m := range Markers {
			if strings.HasPrefix(m, "*") {
				if matched, _ := filepath.Match(m, e.Name()); matched {
					return true
				}
				continue
			}
			if e.Name() == m {
				return true
			}
		}
	}
	return false
}

// skipDirs are never descended into and never projects (dependency,
// build, and tooling dirs — note node_modules carries package.json files
// that must not become "projects").
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	"out": true, ".next": true, "__pycache__": true, ".venv": true,
	"venv": true, "target": true, "bin": true, "obj": true,
	"coverage": true, "testresults": true, ".idea": true, ".vscode": true,
}

// defaultWindowsRoot is the legacy Windows-only project container. It is
// used exclusively as a Windows default (Issue #111): other platforms use
// platform.ProjectRoots() (env or home) and never this drive letter.
const defaultWindowsRoot = `D:\`

// projectRoots returns the directories Leaves scans. NEXUS_PROJECT_ROOTS
// (via platform.ProjectRoots) wins when explicitly set; otherwise on
// Windows the legacy D:\ root is used when present (backward compatible),
// falling back to the platform default (home). On non-Windows the platform
// default (env or home) is used verbatim — never a Windows drive letter.
func projectRoots() []string {
	if raw := strings.TrimSpace(os.Getenv("NEXUS_PROJECT_ROOTS")); raw != "" {
		return platform.ProjectRoots()
	}
	if runtime.GOOS == "windows" {
		if fi, err := os.Stat(defaultWindowsRoot); err == nil && fi.IsDir() {
			return []string{defaultWindowsRoot}
		}
	}
	return platform.ProjectRoots()
}

// Roots returns the configured project roots (Issue #111). It is the
// exported view of projectRoots for adapter callers that must resolve
// root-relative paths without hardcoding a drive letter.
func Roots() []string { return projectRoots() }

// LeafDir maps a project ID ("a/b" or "a") to its absolute directory under
// the first project root. Absolute inputs are cleaned and returned as-is.
// Empty or "global" map to "" (no directory).
func LeafDir(leaf string) string {
	if leaf == "" || leaf == "global" {
		return ""
	}
	if filepath.IsAbs(leaf) {
		return filepath.Clean(leaf)
	}
	roots := projectRoots()
	root := defaultWindowsRoot
	if len(roots) > 0 && strings.TrimSpace(roots[0]) != "" {
		root = roots[0]
	} else if runtime.GOOS != "windows" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			root = home
		}
	}
	return filepath.Join(root, filepath.FromSlash(leaf))
}

// LeafDirs maps leaf IDs to absolute directories, skipping globals.
func LeafDirs(leaves []string) []string {
	out := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		if dir := LeafDir(leaf); dir != "" {
			out = append(out, dir)
		}
	}
	return out
}

// Leaves returns project IDs as paths relative to the project root using
// forward slashes ("gitlab-test/2/Campus-Navigator"). A directory with
// markers is a leaf (descent stops — no fragmenting monorepos or
// node_modules); an unmarked dir contributes marked descendants, or itself
// when nothing below is marked. Depth is capped (root = 0, max 4). OS-level
// dirs are excluded. Roots come from projectRoots() (Issue #111).
func Leaves() []string {
	var out []string
	seen := map[string]bool{}
	var emit func(abs, rel string, depth int)
	emit = func(abs, rel string, depth int) {
		if hasMarker(abs) || depth >= 4 {
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
			return
		}
		var kids []string
		if subs, err := os.ReadDir(abs); err == nil {
			for _, s := range subs {
				if !s.IsDir() || skipDirs[strings.ToLower(s.Name())] {
					continue
				}
				kids = append(kids, s.Name())
			}
		}
		marked := false
		for _, k := range kids {
			if subtreeMarked(filepath.Join(abs, k), depth+1) {
				marked = true
				break
			}
		}
		if !marked {
			// Plain folder (or depth cap): one project, don't fragment.
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
			return
		}
		for _, k := range kids {
			// Bare dot-dirs (.claude, .cursor) are agent state, not projects —
			// skip unless they carry repo markers themselves.
			if strings.HasPrefix(k, ".") && !hasMarker(filepath.Join(abs, k)) {
				continue
			}
			emit(filepath.Join(abs, k), rel+"/"+k, depth+1)
		}
	}
	for _, root := range projectRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || SystemDir(e.Name()) || skipDirs[strings.ToLower(e.Name())] {
				continue
			}
			emit(filepath.Join(root, e.Name()), e.Name(), 1)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// subtreeMarked reports whether dir or any descendant (within depth cap)
// carries a project marker.
func subtreeMarked(abs string, depth int) bool {
	if hasMarker(abs) {
		return true
	}
	if depth >= 4 {
		return false
	}
	subs, err := os.ReadDir(abs)
	if err != nil {
		return false
	}
	for _, s := range subs {
		if !s.IsDir() || skipDirs[strings.ToLower(s.Name())] {
			continue
		}
		if subtreeMarked(filepath.Join(abs, s.Name()), depth+1) {
			return true
		}
	}
	return false
}

var (
	leavesOnce sync.Once
	leavesVal  []string
	fpMu       sync.Mutex
	fpCache    = map[string][2]string{}
)

// CachedLeaves memoizes Leaves for the process lifetime (harvest/scan runs).
func CachedLeaves() []string {
	leavesOnce.Do(func() { leavesVal = Leaves() })
	return leavesVal
}

// Fingerprint returns move-proof repo identity for a leaf dir: the git
// origin URL (survives moves AND renames) plus the root-commit hash
// (survives even without a remote). Empty strings when not a git repo.
// Results are cached per directory for harvest-scale call volumes.
func Fingerprint(dir string) (origin, root string) {
	fpMu.Lock()
	if v, ok := fpCache[dir]; ok {
		fpMu.Unlock()
		return v[0], v[1]
	}
	fpMu.Unlock()
	origin, root = fpRun(dir, "remote", "get-url", "origin"), fpRun(dir, "rev-list", "--max-parents=0", "HEAD")
	if i := strings.Index(root, "\n"); i >= 0 {
		root = root[:i]
	}
	fpMu.Lock()
	fpCache[dir] = [2]string{origin, root}
	fpMu.Unlock()
	return origin, root
}

func fpRun(dir string, args ...string) string {
	// Failure (not a repo, no git) yields "" — identity is best-effort.
	out, err := gitOut(dir, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitOut(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	return string(out), err
}

// ResolveLeaf maps a user-supplied project reference to a current leaf ID:
// a leaf path ("a/b"), a git origin URL, or a root-commit hash (both survive
// moves/renames). Returns "" when nothing matches.
func ResolveLeaf(ref string) string {
	for _, leaf := range CachedLeaves() {
		if leaf == ref {
			return leaf
		}
	}
	for _, leaf := range CachedLeaves() {
		origin, root := Fingerprint(LeafDir(leaf))
		if ref != "" && (ref == origin || ref == root) {
			return leaf
		}
	}
	return ""
}

// ForPath maps an absolute native path to the deepest matching leaf ("a/b"
// beats "a"). Roots come from projectRoots(); legacy D:\ prefixes are also
// honored on Windows so old transcripts still resolve after the #111 fix.
// Returns "" when nothing matches.
func ForPath(nativePath string) string {
	norm := filepath.ToSlash(strings.ToLower(nativePath))
	best := ""
	roots := projectRoots()
	candidates := make([]string, 0, len(roots)+1)
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		candidates = append(candidates, strings.TrimSuffix(filepath.ToSlash(strings.ToLower(r)), "/"))
	}
	// Legacy fallback: old transcripts encode D:\ paths even when roots move.
	if runtime.GOOS == "windows" {
		hasD := false
		for _, c := range candidates {
			if c == "d:" {
				hasD = true
				break
			}
		}
		if !hasD {
			candidates = append(candidates, "d:")
		}
	}
	for _, leaf := range CachedLeaves() {
		lowerLeaf := strings.ToLower(leaf)
		for _, c := range candidates {
			prefix := c + "/" + lowerLeaf + "/"
			if strings.HasPrefix(norm, prefix) && len(leaf) > len(best) {
				best = leaf
			}
			// Exact dir itself (e.g. the leaf root file listing).
			if norm == c+"/"+lowerLeaf && len(leaf) > len(best) {
				best = leaf
			}
		}
	}
	return best
}
