package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"central-memory/internal/platform"
	"central-memory/internal/project"
)

// RootsFor builds candidate native dirs: home-level + per-project dot dirs.
// Leaf projects come from project.Leaves so nested repos (<root>/gitlab-test/X)
// each get their own dot-dir roots, not just the top folder.
func RootsFor(home string, agentDirs []string, projectDotDirs []string) []string {
	return RootsForIn(home, agentDirs, projectDotDirs, project.CachedLeaves(), platform.ProjectRoots())
}

// RootsForIn is RootsFor over explicit leaves/roots (testability seam;
// production passes project.CachedLeaves + platform.ProjectRoots).
func RootsForIn(home string, agentDirs []string, projectDotDirs []string, leaves []string, roots []string) []string {
	var rootsOut []string
	for _, d := range agentDirs {
		rootsOut = append(rootsOut, filepath.Join(home, d))
	}
	for _, leaf := range leaves {
		for _, dot := range projectDotDirs {
			for _, root := range roots {
				if strings.TrimSpace(root) == "" {
					continue
				}
				rootsOut = append(rootsOut, filepath.Join(root, filepath.FromSlash(leaf), dot))
			}
		}
	}
	return rootsOut
}

// ClassifyPath applies BACKUP/IGNORE/NEVER rules shared by all adapters.
func ClassifyPath(p string) Classification {
	base := strings.ToLower(filepath.Base(p))
	lower := strings.ToLower(p)
	// NEVER: credentials, tokens, keys — fail closed, never copied.
	neverSubs := []string{"credentials.json", "auth.json", "cookies", ".key", ".pem", "secret"}
	for _, s := range neverSubs {
		if strings.Contains(lower, s) {
			return Never
		}
	}
	// IGNORE: caches, logs, tmp.
	ignoreSubs := []string{"cache", "gpucache", ".log", string(filepath.Separator) + "tmp" + string(filepath.Separator), "logs" + string(filepath.Separator)}
	for _, s := range ignoreSubs {
		if strings.Contains(lower, s) {
			return Ignore
		}
	}
	_ = base
	return Backup
}

// ProjectOf infers the current leaf project for a native path. Folders move:
// when the path itself no longer matches (stale cwd in transcripts, old
// workspace.json URIs, pre-move Claude dir names), resolve falls back to
// basename matching and records the original location (see ProjectWas).
func ProjectOf(nativePath, home string) string {
	leaf, _ := resolve(nativePath, home)
	return leaf
}

// ProjectWas returns the stale original location when resolve() mapped via
// fallback ("" when the path matched directly).
func ProjectWas(nativePath, home string) string {
	_, was := resolve(nativePath, home)
	return was
}

var resolveCache = struct {
	sync.Mutex
	m map[string][2]string
}{m: map[string][2]string{}}

func resolve(nativePath, home string) (leaf, was string) {
	resolveCache.Lock()
	if v, ok := resolveCache.m[nativePath]; ok {
		resolveCache.Unlock()
		return v[0], v[1]
	}
	resolveCache.Unlock()
	leaf, was = resolveUncached(nativePath, home)
	resolveCache.Lock()
	resolveCache.m[nativePath] = [2]string{leaf, was}
	resolveCache.Unlock()
	return leaf, was
}

func resolveUncached(nativePath, home string) (string, string) {
	// Deepest leaf first.
	if leaf := project.ForPath(nativePath); leaf != "" {
		return leaf, ""
	}
	// Claude-style encoded project dirs.
	if leaf := claudeDirProject(nativePath); leaf != "" {
		return leaf, ""
	}
	// Transcript-level cwd (stale when the folder moved since the session).
	if cwd := fileCwd(nativePath); cwd != "" {
		if leaf := project.ForPath(cwd); leaf != "" {
			return leaf, cwd
		}
		if leaf, ok := basenameLeaf(cwd); ok {
			return leaf, cwd
		}
	}
	// Workspace folder URI (stale the same way).
	if folder := workspaceFolder(nativePath); folder != "" {
		if leaf := project.ForPath(folder); leaf != "" {
			return leaf, ""
		}
		if leaf, ok := basenameLeaf(folder); ok {
			return leaf, folder
		}
	}
	// Claude dir name with no other signal: suffix-match encoded leaf base.
	if leaf, was := claudeDirSuffix(nativePath); leaf != "" {
		return leaf, was
	}
	// Fallback: first segment under any configured project root
	// (replaces the old hardcoded D:\ prefix rule; set NEXUS_PROJECT_ROOTS
	// on machines whose projects live outside the home dir).
	if rel, ok := project.RootRel(nativePath); ok {
		if i := strings.Index(rel, "/"); i > 0 {
			return rel[:i], ""
		}
		if rel != "" {
			return rel, ""
		}
	}
	if proj := workspaceProject(nativePath); proj != "" {
		return proj, ""
	}
	marker := string(filepath.Separator) + "projects" + string(filepath.Separator)
	if i := strings.LastIndex(strings.ToLower(nativePath), marker); i >= 0 {
		rest := nativePath[i+len(marker):]
		if j := strings.Index(rest, string(filepath.Separator)); j > 0 {
			return rest[:j], ""
		}
		return rest, ""
	}
	return "global", ""
}

// basenameLeaf matches a stale path's final segment against current leaf
// base names. Unique match wins; zero or several = "" (never guess).
func basenameLeaf(stalePath string) (string, bool) {
	base := lastSegment(stalePath)
	if base == "" {
		return "", false
	}
	var match string
	n := 0
	for _, leaf := range project.CachedLeaves() {
		if strings.EqualFold(lastSegment(leaf), base) {
			match, n = leaf, n+1
		}
	}
	if n == 1 {
		return match, true
	}
	return "", false
}

func lastSegment(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	p = strings.Trim(p, `\`)
	if i := strings.LastIndex(p, `\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

var cwdRe = regexp.MustCompile(`"cwd"\s*:\s*"([^"]+)"`)

// fileCwd scans a transcript's first 64KB for its recorded working dir.
func fileCwd(p string) string {
	low := strings.ToLower(p)
	if !strings.HasSuffix(low, ".jsonl") && !strings.HasSuffix(low, ".json") {
		return ""
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() > 50<<20 {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := f.Read(buf)
	m := cwdRe.FindSubmatch(buf[:n])
	if m == nil {
		return ""
	}
	cwd := string(m[1])
	cwd = strings.ReplaceAll(cwd, `\\`, `\`)
	return cwd
}

// workspaceFolder returns the decoded folder URI sibling to a workspace file.
func workspaceFolder(p string) string {
	dir := filepath.Dir(p)
	for i := 0; i < 2; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
		if err == nil {
			var w struct {
				Folder string `json:"folder"`
			}
			if json.Unmarshal(data, &w) == nil && w.Folder != "" {
				if u, err := url.Parse(w.Folder); err == nil {
					decoded, _ := url.PathUnescape(u.Path)
					decoded = strings.ReplaceAll(decoded, "/", `\`)
					return strings.TrimLeft(decoded, `\`)
				}
				return w.Folder
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// claudeDirSuffix handles pre-move Claude dir names (D--SERVER-automation
// for a repo now at <root>/a/SERVER-automation): the encoded name must end
// with "-" + the leaf's encoded base, and exactly one leaf may match.
func claudeDirSuffix(nativePath string) (string, string) {
	marker := string(filepath.Separator) + "projects" + string(filepath.Separator)
	i := strings.LastIndex(strings.ToLower(nativePath), marker)
	if i < 0 {
		return "", ""
	}
	rest := nativePath[i+len(marker):]
	enc := rest
	if j := strings.Index(enc, string(filepath.Separator)); j >= 0 {
		enc = enc[:j]
	}
	enc = strings.ToLower(enc)
	var match string
	n := 0
	for _, leaf := range project.CachedLeaves() {
		base := strings.ToLower(lastSegment(leaf))
		if base != "" && strings.HasSuffix(enc, "-"+base) {
			match, n = leaf, n+1
		}
	}
	if n == 1 {
		return match, "encoded-dir:" + enc
	}
	return "", ""
}

// every known leaf the same way Claude does (":", "\", "/" -> "-") and
// matching case-insensitively. Handles nested repos and literal dashes
// because the match is against real leaves, not string surgery.
func claudeDirProject(nativePath string) string {
	marker := string(filepath.Separator) + "projects" + string(filepath.Separator)
	i := strings.LastIndex(strings.ToLower(nativePath), marker)
	if i < 0 {
		return ""
	}
	rest := nativePath[i+len(marker):]
	enc := rest
	if j := strings.Index(enc, string(filepath.Separator)); j >= 0 {
		enc = enc[:j]
	}
	enc = strings.ToLower(enc)
	for _, leaf := range project.CachedLeaves() {
		full := project.LeafDir(leaf)
		if full == "" {
			continue
		}
		var b strings.Builder
		for _, r := range full {
			if r == ':' || r == '\\' || r == '/' {
				b.WriteRune('-')
			} else {
				b.WriteRune(r)
			}
		}
		if strings.ToLower(b.String()) == enc {
			return leaf
		}
	}
	return ""
}

// workspace hash dir to its folder (Antigravity / VS Code family).
func workspaceProject(p string) string {
	dir := filepath.Dir(p)
	for i := 0; i < 2; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
		if err == nil {
			var w struct {
				Folder string `json:"folder"`
			}
			if json.Unmarshal(data, &w) == nil && w.Folder != "" {
				if u, err := url.Parse(w.Folder); err == nil {
					decoded, _ := url.PathUnescape(u.Path)
					decoded = strings.ReplaceAll(decoded, "/", `\`)
					decoded = strings.TrimLeft(decoded, `\`)
					if leaf := project.ForPath(decoded); leaf != "" {
						return leaf
					}
					// Same-absolute-path fallback under any configured root
					// (replaces the old hardcoded d:\ rule).
					if rel, ok := project.RootRel(decoded); ok {
						if j := strings.Index(rel, "/"); j > 0 {
							return rel[:j]
						}
						if rel != "" {
							return rel
						}
					}
				}
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// CopyFiltered copies BACKUP files from roots to destRoot, tagging each with
// root index to preserve structure. Skips files > maxBytes (large-file
// pointer rule: record, don't copy). Resumable: a destination file with the
// same size and equal-or-newer modtime is counted without re-copying, so an
// interrupted 16K-file harvest finishes on re-run instead of restarting.
// Missing roots are skipped (agent dirs that were never created); any walk,
// relativize, or copy failure aborts and is returned — callers must not
// report a clean export when files failed to copy.
// Returns copied + skipped lists.
func CopyFiltered(roots []string, destRoot string, maxBytes int64, projectFilter string, home string) (copied, skipped []Artifact, err error) {
	for ri, r := range roots {
		if _, serr := os.Stat(r); serr != nil {
			if os.IsNotExist(serr) {
				continue
			}
			return copied, skipped, fmt.Errorf("adapters: stat root %q: %w", r, serr)
		}
		werr := filepath.Walk(r, func(p string, info os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			if info.IsDir() {
				return nil
			}
			switch ClassifyPath(p) {
			case Never, Ignore:
				return nil
			}
			if projectFilter != "" && ProjectOf(p, home) != projectFilter {
				return nil
			}
			if info.Size() > maxBytes {
				skipped = append(skipped, Artifact{NativePath: p, Project: ProjectOf(p, home)})
				return nil
			}
			rel, rerr := filepath.Rel(r, p)
			if rerr != nil {
				return rerr
			}
			dst := filepath.Join(destRoot, safeName(r, ri), rel)
			if st, serr := os.Stat(dst); serr == nil && st.Size() == info.Size() && !st.ModTime().Before(info.ModTime()) {
				copied = append(copied, Artifact{NativePath: p, RawPath: dst, Project: ProjectOf(p, home), Was: ProjectWas(p, home)})
				return nil
			}
			if err := copyFile(p, dst); err != nil {
				return fmt.Errorf("adapters: copy %q: %w", p, err)
			}
			copied = append(copied, Artifact{NativePath: p, RawPath: dst, Project: ProjectOf(p, home), Was: ProjectWas(p, home)})
			return nil
		})
		if werr != nil {
			return copied, skipped, fmt.Errorf("adapters: walk %q: %w", r, werr)
		}
	}
	return copied, skipped, nil
}

func safeName(root string, i int) string {
	b := filepath.Base(root)
	if b == "." || b == "" || b == string(filepath.Separator) {
		b = "root"
	}
	// Prefix with the root index so same-named dirs from different roots
	// (two checkouts of "app" under different parents) never collide.
	safe := strings.Map(func(r rune) rune {
		switch r {
		case ':', ' ', '/', '\\':
			return '_'
		}
		return r
	}, b)
	if safe == "." || safe == "" {
		safe = "root"
	}
	return filepath.Clean(fmt.Sprintf("%d_%s", i, safe))
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
