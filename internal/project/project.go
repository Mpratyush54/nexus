// Package project detects leaf projects under configurable roots. A
// top-level folder like <root>/gitlab-test can hold several real repos —
// the folder itself is not the project, each repo underneath is. A
// directory is a leaf project when it carries a repo marker (.git,
// package.json, go.mod, ...). A top-level dir without markers but with
// marked children contributes each marked child as "parent/child". Scan
// depth is capped at 2 to stay fast. OS-level dirs ($RECYCLE.BIN, ...)
// are never projects.
//
// Roots come from internal/platform ProjectRoots (NEXUS_PROJECT_ROOTS or
// the user home dir) — never a hardcoded drive letter — so this package
// works on Windows, macOS, Linux, and containers. Leaf IDs stay portable
// forward-slash paths relative to their containing root
// ("gitlab-test/2/Campus-Navigator"). The zero-arg Leaves/CachedLeaves/
// ResolveLeaf/ForPath wrappers use the configured roots; the *In variants
// take explicit roots/leaves and exist for tests and callers that already
// resolved them.
package project

import (
	"os"
	"os/exec"
	"path/filepath"
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

// Leaves returns project IDs as portable forward-slash paths relative to
// their containing root ("gitlab-test/2/Campus-Navigator"), scanning the
// configured platform.ProjectRoots. A directory with markers is a leaf
// (descent stops — no fragmenting monorepos or node_modules); an unmarked
// dir contributes marked descendants, or itself when nothing below is
// marked. Depth is capped (root = 0, max 4). OS-level dirs are excluded.
// Missing/unreadable roots are skipped, never fatal.
func Leaves() []string {
	return LeavesIn(platform.ProjectRoots())
}

// LeavesIn is Leaves over explicit roots (testability seam; production
// passes platform.ProjectRoots via Leaves).
func LeavesIn(roots []string) []string {
	var out []string
	var emit func(abs, rel string, depth int)
	emit = func(abs, rel string, depth int) {
		if hasMarker(abs) || depth >= 4 {
			out = append(out, rel)
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
			out = append(out, rel)
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
	for _, root := range roots {
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
	return out
}

// LeafDir maps a leaf ID ("a/b" or "a") to its absolute directory: the
// first configured root that actually contains it, else the first root
// joined with the leaf (best guess for messaging), else "" when no roots
// are configured or the leaf is global/empty.
func LeafDir(leaf string) string {
	return LeafDirIn(leaf, platform.ProjectRoots())
}

// LeafDirIn is LeafDir over explicit roots (testability seam).
func LeafDirIn(leaf string, roots []string) string {
	if leaf == "" || leaf == "global" {
		return ""
	}
	rel := filepath.FromSlash(leaf)
	var first string
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if first == "" {
			first = root
		}
		abs := filepath.Join(root, rel)
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return abs
		}
	}
	if first == "" {
		return ""
	}
	return filepath.Join(first, rel)
}

// RootRel returns nativePath relative (forward-slash form) to its
// containing configured root. ok is false when the path sits under no
// known root. Comparison is case-insensitive so Windows volume roots
// match regardless of case.
func RootRel(nativePath string) (rel string, ok bool) {
	return RootRelIn(nativePath, platform.ProjectRoots())
}

// RootRelIn is RootRel over explicit roots (testability seam).
func RootRelIn(nativePath string, roots []string) (string, bool) {
	norm := filepath.ToSlash(strings.ToLower(nativePath))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		base := strings.TrimRight(filepath.ToSlash(strings.ToLower(root)), "/")
		if norm == base {
			return "", true
		}
		if strings.HasPrefix(norm, base+"/") {
			return norm[len(base)+1:], true
		}
	}
	return "", false
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
	return ResolveLeafIn(ref, CachedLeaves(), platform.ProjectRoots())
}

// ResolveLeafIn is ResolveLeaf over explicit leaves/roots (testability
// seam; production passes CachedLeaves + platform.ProjectRoots).
func ResolveLeafIn(ref string, leaves []string, roots []string) string {
	for _, leaf := range leaves {
		if leaf == ref {
			return leaf
		}
	}
	for _, leaf := range leaves {
		dir := LeafDirIn(leaf, roots)
		if dir == "" {
			continue
		}
		origin, root := Fingerprint(dir)
		if ref != "" && (ref == origin || ref == root) {
			return leaf
		}
	}
	return ""
}

// ForPath maps an absolute native path to the deepest matching leaf ("a/b"
// beats "a"). A leaf matches when the path is its directory (or under it)
// below any configured root. Returns "" when nothing matches.
func ForPath(nativePath string) string {
	return ForPathIn(nativePath, CachedLeaves(), platform.ProjectRoots())
}

// ForPathIn is ForPath over explicit leaves/roots (testability seam).
func ForPathIn(nativePath string, leaves []string, roots []string) string {
	norm := filepath.ToSlash(strings.ToLower(nativePath))
	best := ""
	for _, leaf := range leaves {
		rel := filepath.ToSlash(strings.ToLower(filepath.FromSlash(leaf)))
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			prefix := strings.TrimRight(filepath.ToSlash(strings.ToLower(root)), "/") + "/" + rel
			if strings.HasPrefix(norm, prefix+"/") && len(leaf) > len(best) {
				best = leaf
			}
			// Exact dir itself (e.g. the leaf root file listing).
			if norm == prefix {
				best = leaf
			}
		}
	}
	return best
}
