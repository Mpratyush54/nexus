package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"central-memory/internal/project"
)

// RootsFor builds candidate native dirs: home-level + per-project dot dirs.
// Leaf projects come from project.Leaves so nested repos (<root>/gitlab-test/X)
// each get their own dot-dir roots, not just the top folder. Leaf absolute
// paths come from project.LeafDir (platform-aware roots, Issue #111) — never
// a hardcoded drive letter.
func RootsFor(home string, agentDirs []string, projectDotDirs []string) []string {
	var roots []string
	for _, d := range agentDirs {
		roots = append(roots, filepath.Join(home, d))
	}
	for _, leaf := range project.CachedLeaves() {
		base := project.LeafDir(leaf)
		if base == "" {
			continue
		}
		for _, dot := range projectDotDirs {
			roots = append(roots, filepath.Join(base, dot))
		}
	}
	return roots
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
	// Root-relative fallback (Issue #111): first segment under any known
	// project root. Legacy D:\ paths still resolve via project.ForPath above
	// (which honors the Windows D:\ default); this covers env-configured
	// roots without hardcoding a drive letter.
	if leaf, ok := rootRelativeLeaf(nativePath); ok {
		return leaf, ""
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

// rootRelativeLeaf returns the first path segment under any known project
// root (Issue #111 platform-aware fallback). It covers new folders not yet
// in the leaf cache without hardcoding a drive letter. ok=false when the
// path is not under a known root.
func rootRelativeLeaf(nativePath string) (leaf string, ok bool) {
	norm := filepath.ToSlash(strings.ToLower(nativePath))
	for _, r := range project.Roots() {
		if strings.TrimSpace(r) == "" {
			continue
		}
		prefix := strings.TrimSuffix(filepath.ToSlash(strings.ToLower(r)), "/") + "/"
		if !strings.HasPrefix(norm, prefix) {
			continue
		}
		rel := strings.TrimPrefix(norm, prefix)
		// Return the top-level segment ("a" for "a/b/c"); deeper leaves are
		// resolved by project.ForPath before this fallback runs.
		if i := strings.Index(rel, "/"); i > 0 {
			return rel[:i], true
		}
		if rel != "" {
			return rel, true
		}
	}
	return "", false
}

// encodeClaudeDir encodes an absolute path the same way Claude does (":",
// "\", "/" -> "-") for dir-name comparison.
func encodeClaudeDir(abs string) string {
	var b strings.Builder
	for _, r := range abs {
		if r == ':' || r == '\\' || r == '/' {
			b.WriteRune('-')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// every known leaf the same way Claude does (":", "\", "/" -> "-") and
// matching case-insensitively. Handles nested repos and literal dashes
// because the match is against real leaves, not string surgery. Candidate
// absolute paths are built from project.Roots() (Issue #111) plus the
// legacy Windows D:\ form so old encodings still match.
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
		candidates := []string{project.LeafDir(leaf)}
		// Legacy encoding used D:\ even when roots move; keep matching it.
		legacy := filepath.Join(`D:\`, filepath.FromSlash(leaf))
		if legacy != candidates[0] {
			candidates = append(candidates, legacy)
		}
		for _, full := range candidates {
			if full == "" {
				continue
			}
			if encodeClaudeDir(full) == enc {
				return leaf
			}
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
					// Root-relative fallback without hardcoding D:\ (Issue #111).
					if leaf, ok := rootRelativeLeaf(decoded); ok {
						return leaf
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
// Returns copied + skipped lists plus an aggregated error (Issue #108):
// copy/mkdir failures are collected across all roots via errors.Join instead
// of being swallowed; walk errors on missing roots are ignored (Discover
// with missing roots must stay non-fatal), other walk errors are aggregated.
func CopyFiltered(roots []string, destRoot string, maxBytes int64, projectFilter string, home string) (copied, skipped []Artifact, err error) {
	var errs []error
	for ri, r := range roots {
		walkErr := filepath.Walk(r, func(p string, info os.FileInfo, werr error) error {
			if werr != nil {
				if os.IsNotExist(werr) {
					return nil
				}
				errs = append(errs, fmt.Errorf("walk %s: %w", p, werr))
				return nil
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
			rel, relErr := filepath.Rel(r, p)
			if relErr != nil {
				errs = append(errs, fmt.Errorf("rel %s: %w", p, relErr))
				return nil
			}
			dst := filepath.Join(destRoot, safeName(r, ri), rel)
			if st, serr := os.Stat(dst); serr == nil && st.Size() == info.Size() && !st.ModTime().Before(info.ModTime()) {
				copied = append(copied, Artifact{NativePath: p, RawPath: dst, Project: ProjectOf(p, home), Was: ProjectWas(p, home)})
				return nil
			}
			if cerr := copyFile(p, dst); cerr != nil {
				errs = append(errs, fmt.Errorf("copy %s: %w", p, cerr))
				return nil
			}
			copied = append(copied, Artifact{NativePath: p, RawPath: dst, Project: ProjectOf(p, home)})
			return nil
		})
		if walkErr != nil && !os.IsNotExist(walkErr) {
			errs = append(errs, fmt.Errorf("walk root %s: %w", r, walkErr))
		}
	}
	return copied, skipped, errors.Join(errs...)
}

// safeName maps a source root to a collision-free destination segment
// (Issue #108). The root index prefixes the cleaned base name so two roots
// with the same folder name ("projA/.claude" vs "projB/.claude") land in
// different dest dirs. Cleaning replaces ':' and ' ' with '_' and maps
// "."/empty to "root".
func safeName(root string, i int) string {
	b := filepath.Base(root)
	if b == "." || b == "" {
		b = "root"
	}
	cleaned := strings.Map(func(r rune) rune {
		if r == ':' || r == ' ' {
			return '_'
		}
		return r
	}, b)
	return filepath.Clean(fmt.Sprintf("%02d-%s", i, cleaned))
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
