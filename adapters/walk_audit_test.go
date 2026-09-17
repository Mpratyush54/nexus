package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	projectpkg "central-memory/internal/project"
)

func clearAuditResolveCache(t *testing.T) {
	t.Helper()
	resolveCache.Lock()
	resolveCache.m = map[string][2]string{}
	resolveCache.Unlock()
}

func TestAuditRootsForHomeAndLeaves(t *testing.T) {
	home := t.TempDir()
	roots := RootsFor(home, []string{"mydir"}, []string{"mydot"})
	foundHome := false
	for _, r := range roots {
		if r == filepath.Join(home, "mydir") {
			foundHome = true
		}
	}
	if !foundHome {
		t.Errorf("RootsFor should include home agent dir, got %v", roots)
	}
	// Per-leaf dot dirs for every cached leaf.
	for _, leaf := range projectpkg.CachedLeaves() {
		want := filepath.Join(`D:\`, filepath.FromSlash(leaf), "mydot")
		found := false
		for _, r := range roots {
			if r == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RootsFor missing leaf root %q (roots=%v)", want, roots)
		}
	}
	if got := RootsFor(home, nil, nil); len(got) != 0 && len(projectpkg.CachedLeaves()) == 0 {
		t.Errorf("RootsFor(nil,nil) with no leaves should be empty, got %v", got)
	}
}

func TestAuditClassifyPathBackupTable(t *testing.T) {
	backup := []string{
		filepath.Join("home", "user", ".claude", "projects", "x", "session.jsonl"),
		filepath.Join("D:", "proj", ".codex", "notes.md"),
		filepath.Join(t.TempDir(), "transcript.json"),
		"notes.txt",
	}
	for _, p := range backup {
		if got := ClassifyPath(p); got != Backup {
			t.Errorf("ClassifyPath(%q) = %v, want Backup", p, got)
		}
	}
}

func TestAuditClassifyPathNeverTable(t *testing.T) {
	never := []string{
		`C:\u\.claude\credentials.json`,
		`C:\u\auth.json`,
		`C:\u\cookies.txt`,
		`C:\u\id_rsa.key`,
		`C:\u\server.pem`,
		`C:\u\my-secret-token.txt`,
	}
	for _, p := range never {
		if got := ClassifyPath(p); got != Never {
			t.Errorf("ClassifyPath(%q) = %v, want Never", p, got)
		}
	}
}

func TestAuditClassifyPathIgnoreTable(t *testing.T) {
	sep := string(filepath.Separator)
	ignore := []string{
		filepath.Join("x", "Cache", "f.json"),
		filepath.Join("x", "gpucache", "f.bin"),
		filepath.Join("x", "debug.log"),
		"x" + sep + "tmp" + sep + "f.json",
		"x" + sep + "logs" + sep + "f.txt",
	}
	for _, p := range ignore {
		if got := ClassifyPath(p); got != Ignore {
			t.Errorf("ClassifyPath(%q) = %v, want Ignore", p, got)
		}
	}
}

// REGRESSION (actual behavior): ClassifyPath matches NEVER/IGNORE substrings
// overbroadly via strings.Contains over the whole path, so words that merely
// contain a keyword as part of a larger word misclassify.
// BUG (no tracking issue filed): narrow matching (word/token boundaries) is
// needed so e.g. "secretary-*" stays Backup and "cachet"/"precache" stay Backup.
// This test pins the CURRENT overbroad results so the bug is visible.
func TestAuditClassifyPathOverbroadSubstringsKnownBug(t *testing.T) {
	cases := []struct {
		path string
		want Classification
	}{
		{filepath.Join("docs", "secretary-notes.md"), Never},        // contains "secret"
		{filepath.Join("docs", "my-secretary-handbook.txt"), Never}, // contains "secret"
		{filepath.Join("proj", "desecrate.txt"), Backup},            // no "secret" substring -> Backup
		{filepath.Join("proj", "cachet-design.md"), Ignore},         // contains "cache"
		{filepath.Join("proj", "precache-manifest.json"), Ignore},   // contains "cache"
	}
	for _, tc := range cases {
		if got := ClassifyPath(tc.path); got != tc.want {
			t.Errorf("ClassifyPath(%q) = %v, want %v (pinned overbroad-substring behavior)", tc.path, got, tc.want)
		}
	}
}

func TestAuditProjectOfGlobalFallback(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	if got := ProjectOf(filepath.Join(home, "some", "file.json"), home); got != "global" {
		t.Errorf("ProjectOf(home-relative) = %q want global", got)
	}
	if got := ProjectWas(filepath.Join(home, "some", "file.json"), home); got != "" {
		t.Errorf("ProjectWas(direct/global) = %q want empty", got)
	}
}

func TestAuditProjectOfDPrefixFallback(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	// D:\-prefixed path with no other signal falls back to first segment.
	if got := ProjectOf(`D:\audit-fallback-proj-xyz\file.txt`, home); got != "audit-fallback-proj-xyz" {
		t.Errorf("ProjectOf D:\\ fallback = %q", got)
	}
}

func TestAuditProjectOfProjectsMarkerFallback(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	p := filepath.Join(home, "x", "projects", "somedir", "file.json")
	// Ends with .../projects/somedir/... -> rel fallback returns "somedir".
	if got := ProjectOf(p, home); got != "somedir" {
		t.Errorf("ProjectOf projects-marker = %q want somedir", got)
	}
}

func TestAuditFileCwdScansFirst64KB(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	content := `{"cwd": "D:\\my-cwd-proj", "other": 1}` + "\n" + strings.Repeat("x", 100)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fileCwd(p); got != `D:\my-cwd-proj` {
		t.Errorf("fileCwd = %q", got)
	}
	// Non-json extensions yield "".
	txt := filepath.Join(dir, "t.txt")
	_ = os.WriteFile(txt, []byte(`"cwd": "D:\\x"`), 0o644)
	if got := fileCwd(txt); got != "" {
		t.Errorf("fileCwd(txt) = %q want empty", got)
	}
	// Missing file yields "".
	if got := fileCwd(filepath.Join(dir, "nope.json")); got != "" {
		t.Errorf("fileCwd(missing) = %q", got)
	}
	// No cwd key yields "".
	plain := filepath.Join(dir, "plain.json")
	_ = os.WriteFile(plain, []byte(`{"a":1}`), 0o644)
	if got := fileCwd(plain); got != "" {
		t.Errorf("fileCwd(no cwd) = %q", got)
	}
}

func TestAuditWorkspaceFolderDecodesURI(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ws1")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// workspace.json sits in dir; file lives in dir/sub/file.
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{"folder": "file:///d%3A/my-proj"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := workspaceFolder(filepath.Join(sub, "state.db"))
	if got == "" {
		t.Errorf("workspaceFolder should decode folder URI, got empty")
	}
	if !strings.Contains(strings.ToLower(got), "my-proj") {
		t.Errorf("workspaceFolder = %q, want to contain my-proj", got)
	}
	// No workspace.json -> "".
	if got := workspaceFolder(filepath.Join(t.TempDir(), "lonely", "f.db")); got != "" {
		t.Errorf("workspaceFolder(missing) = %q want empty", got)
	}
}

func TestAuditLastSegmentTable(t *testing.T) {
	cases := []struct{ in, want string }{
		{`D:\a\b`, "b"},
		{`D:/a/b/`, "b"},
		{`a/b/c`, "c"},
		{"lonely", "lonely"},
		{"", ""},
		{`\\\`, ""},
	}
	for _, tc := range cases {
		if got := lastSegment(tc.in); got != tc.want {
			t.Errorf("lastSegment(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuditBasenameLeafUniqueVsAmbiguous(t *testing.T) {
	leaves := projectpkg.CachedLeaves()
	if len(leaves) == 0 {
		t.Skip("no D:\\ leaves on this machine; cannot test basenameLeaf")
	}
	// Unique base: full leaf path's base must resolve when exactly one leaf has it.
	// Find a base that appears exactly once.
	counts := map[string]int{}
	lower := map[string]string{}
	for _, l := range leaves {
		b := strings.ToLower(lastSegment(l))
		counts[b]++
		lower[b] = l
	}
	var uniqueBase, uniqueLeaf string
	for b, n := range counts {
		if n == 1 {
			uniqueBase, uniqueLeaf = b, lower[b]
			break
		}
	}
	if uniqueBase == "" {
		t.Skip("no unique leaf basename to test")
	}
	stale := `D:\old-location\` + uniqueBase
	got, ok := basenameLeaf(stale)
	if !ok || got != uniqueLeaf {
		t.Errorf("basenameLeaf(%q) = (%q,%v) want (%q,true)", stale, got, ok, uniqueLeaf)
	}
	// Ambiguous or empty base must not guess.
	if _, ok := basenameLeaf(""); ok {
		t.Errorf("basenameLeaf(empty) should be false")
	}
}

// REGRESSION (actual behavior): resolve() caches by path only, with no
// invalidation on content change, so a later file edit is invisible.
// BUG tracked in #109: cache needs invalidation by mtime/size.
// This test pins the CURRENT stale result (second == first == "global").
func TestAuditResolveCacheStalenessKnownBug(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	dir := t.TempDir()
	p := filepath.Join(dir, "sess.jsonl")
	// First resolution: no cwd -> global, cached.
	_ = os.WriteFile(p, []byte(`{"no": "cwd here"}`), 0o644)
	first := ProjectOf(p, home)
	if first != "global" {
		t.Fatalf("setup: ProjectOf(no cwd) = %q, want global", first)
	}
	// Add a cwd pointing at a D:\ path; a fresh resolve would return the
	// D:\ first-segment fallback, but the cache still returns the old value.
	_ = os.WriteFile(p, []byte(`{"cwd": "D:\\stale-cache-proj-xyz"}`), 0o644)
	second := ProjectOf(p, home)
	if second != "global" {
		t.Errorf("expected pinned stale behavior second=%q, want %q (BUG #109: stale cache)", second, "global")
	}
}

// FIXED (#108): safeName cleans ':' and ' ' and prefixes the root index so
// same-basename roots land in different dest dirs.
func TestAuditSafeNameCleansAndCollidesKnownBug(t *testing.T) {
	// Cleaning: ':' and ' ' become '_'.
	if got := safeName(`D:\my proj`, 0); strings.ContainsAny(got, ": ") {
		t.Errorf("safeName should clean ':' and ' ': %q", got)
	}
	if got := safeName(".", 0); got != "00-root" {
		t.Errorf("safeName(.) = %q want 00-root (index-prefixed)", got)
	}
	// Same basename with different indices must NOT collide.
	a := safeName(filepath.Join("D:", "projA", ".claude"), 0)
	b := safeName(filepath.Join("D:", "projB", ".claude"), 1)
	if a == b {
		t.Errorf("FIXED #108: safeName must use index, got collision %q == %q", a, b)
	}
}

func TestAuditCopyFilteredCopiesSkipsResumes(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "ok.json"), []byte("abc"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "tok-secret.json"), []byte("x"), 0o644) // NEVER
	_ = os.WriteFile(filepath.Join(root, "debug.log"), []byte("x"), 0o644)       // IGNORE
	_ = os.WriteFile(filepath.Join(root, "big.json"), []byte("123456789012345"), 0o644)
	dest := t.TempDir()
	copied, skipped, err := CopyFiltered([]string{root}, dest, 10, "", home)
	if err != nil {
		t.Fatalf("CopyFiltered: %v", err)
	}
	bases := map[string]bool{}
	for _, c := range copied {
		bases[filepath.Base(c.NativePath)] = true
		if c.RawPath == "" {
			t.Errorf("copied entry missing RawPath: %+v", c)
		}
	}
	if !bases["ok.json"] {
		t.Errorf("ok.json should be copied: %v", bases)
	}
	if bases["tok-secret.json"] || bases["debug.log"] {
		t.Errorf("NEVER/IGNORE files must be skipped: %v", bases)
	}
	if len(skipped) != 1 || filepath.Base(skipped[0].NativePath) != "big.json" {
		t.Errorf("big.json should be in skipped, got %+v", skipped)
	}
	// Resumable: second run with same sizes/mtimes counts without re-copy.
	copied2, _, _ := CopyFiltered([]string{root}, dest, 10, "", home)
	if len(copied2) != len(copied) {
		t.Errorf("resumable second run copied %d, want %d", len(copied2), len(copied))
	}
}

// FIXED (#108): same-basename roots land in different dest dirs because
// safeName prefixes the root index.
func TestAuditCopyFilteredSameBasenameCollisionKnownBug(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	r1 := filepath.Join(t.TempDir(), "same")
	r2 := filepath.Join(t.TempDir(), "other", "same")
	_ = os.MkdirAll(r1, 0o755)
	_ = os.MkdirAll(r2, 0o755)
	_ = os.WriteFile(filepath.Join(r1, "f.json"), []byte("from-root-1"), 0o644)
	_ = os.WriteFile(filepath.Join(r2, "f.json"), []byte("from-root-2-DIFFERENT-CONTENT!!"), 0o644)
	dest := t.TempDir()
	copied, _, err := CopyFiltered([]string{r1, r2}, dest, 1<<20, "", home)
	if err != nil {
		t.Fatalf("CopyFiltered: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("expected 2 copied, got %d", len(copied))
	}
	// Fixed: distinct roots must map to distinct RawPaths.
	if copied[0].RawPath == copied[1].RawPath {
		t.Errorf("FIXED #108: expected distinct RawPaths, got collision %q", copied[0].RawPath)
	}
	// Both payloads must survive (no overwrite).
	for _, c := range copied {
		if _, err := os.Stat(c.RawPath); err != nil {
			t.Errorf("dest copy missing %q: %v", c.RawPath, err)
		}
	}
}

func TestAuditCopyFilteredProjectFilter(t *testing.T) {
	clearAuditResolveCache(t)
	home := t.TempDir()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.json"), []byte("{}"), 0o644)
	dest := t.TempDir()
	copied, _, _ := CopyFiltered([]string{root}, dest, 1<<20, "definitely-no-such-project-xyz", home)
	if len(copied) != 0 {
		t.Errorf("projectFilter mismatch should copy 0, got %d", len(copied))
	}
}

func TestAuditCopyFileRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	dst := filepath.Join(t.TempDir(), "sub", "dir", "dst.txt")
	_ = os.WriteFile(src, []byte("payload"), 0o644)
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "payload" {
		t.Errorf("copyFile content = %q", got)
	}
	if err := copyFile(filepath.Join(t.TempDir(), "missing-src"), dst); err == nil {
		t.Errorf("copyFile(missing src) should error")
	}
}

func TestAuditClaudeDirHelpersDoNotPanic(t *testing.T) {
	// Exercise all fallback helpers on paths that miss every signal.
	p := filepath.Join(t.TempDir(), "plain", "file.json")
	if got := claudeDirProject(p); got != "" {
		t.Errorf("claudeDirProject(plain) = %q want empty", got)
	}
	if leaf, was := claudeDirSuffix(p); leaf != "" || was != "" {
		t.Errorf("claudeDirSuffix(plain) = (%q,%q) want empty", leaf, was)
	}
	if got := workspaceProject(p); got != "" {
		t.Errorf("workspaceProject(plain) = %q want empty", got)
	}
	// Suffix with ambiguous/no match must return empty, never guess.
	amb := filepath.Join("x", "projects", "D--x", "f.json")
	if leaf, _ := claudeDirSuffix(amb); leaf != "" {
		// Only acceptable if a real leaf actually suffix-matches; otherwise must be "".
		found := false
		for _, l := range projectpkg.CachedLeaves() {
			if strings.EqualFold(lastSegment(l), "x") {
				found = true
			}
		}
		if !found {
			t.Errorf("claudeDirSuffix ambiguous should be empty, got %q", leaf)
		}
	}
}
