package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func saveLeavesState(t *testing.T) {
	t.Helper()
	fpMu.Lock()
	origFp := make(map[string][2]string, len(fpCache))
	for k, v := range fpCache {
		origFp[k] = v
	}
	fpMu.Unlock()
	origVal := append([]string(nil), leavesVal...)
	t.Cleanup(func() {
		leavesOnce = sync.Once{}
		// If the original had a memoized value, re-memoize it so later
		// callers see the same snapshot without a live D:\ rescan.
		if origVal != nil {
			leavesOnce.Do(func() {})
		}
		leavesVal = origVal
		fpMu.Lock()
		fpCache = origFp
		fpMu.Unlock()
	})
}

func setFakeLeaves(t *testing.T, leaves []string) {
	t.Helper()
	leavesOnce = sync.Once{}
	leavesOnce.Do(func() {})
	leavesVal = append([]string(nil), leaves...)
}

func TestAuditMarkersContents(t *testing.T) {
	if len(Markers) == 0 {
		t.Fatal("Markers empty")
	}
	need := map[string]bool{".git": false, "go.mod": false, "*.sln": false, "package.json": false}
	for _, m := range Markers {
		if _, ok := need[m]; ok {
			need[m] = true
		}
	}
	for m, found := range need {
		if !found {
			t.Errorf("Markers missing %q (got %v)", m, Markers)
		}
	}
}

func TestAuditSystemDirExtra(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"$RECYCLE.BIN", true},
		{"$Recycle.Bin", true},
		{"SYSTEM VOLUME INFORMATION", true},
		{"WindowsApps", true},
		{"node_modules", false},
		{".git", false},
		{"D:", false},
	}
	for _, tc := range cases {
		if got := SystemDir(tc.in); got != tc.want {
			t.Errorf("SystemDir(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestAuditHasMarkerExtra(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.sln"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasMarker(dir) {
		t.Errorf("hasMarker should match *.sln glob")
	}
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "app.sln.bak"), []byte("x"), 0o644)
	if hasMarker(dir2) {
		t.Errorf("hasMarker should not match app.sln.bak")
	}
	dir3 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir3, "GO.MOD"), []byte("x"), 0o644)
	if hasMarker(dir3) {
		t.Errorf("hasMarker is case-sensitive; GO.MOD should not match go.mod")
	}
	if hasMarker(filepath.Join(t.TempDir(), "missing")) {
		t.Errorf("hasMarker(missing) should be false")
	}
}

func TestAuditSubtreeMarkedDepthCap(t *testing.T) {
	// At depth >= 4 only a direct marker counts; descendants are not descended into.
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(deep, "go.mod"), []byte("x"), 0o644)
	if subtreeMarked(base, 4) {
		t.Errorf("subtreeMarked(depth=4) should not descend, got true")
	}
	if !subtreeMarked(deep, 4) {
		t.Errorf("subtreeMarked direct marker at depth 4 should be true")
	}
	// Shallow depth finds nested markers.
	if !subtreeMarked(base, 0) {
		t.Errorf("subtreeMarked(depth=0) should find nested go.mod")
	}
	// skipDirs are never descended: node_modules marker must not count.
	nm := filepath.Join(t.TempDir(), "node_modules", "pkg")
	_ = os.MkdirAll(nm, 0o755)
	_ = os.WriteFile(filepath.Join(nm, "package.json"), []byte("{}"), 0o644)
	parent := filepath.Dir(filepath.Dir(nm))
	if subtreeMarked(parent, 0) {
		t.Errorf("subtreeMarked should ignore node_modules markers")
	}
}

func TestAuditLeavesInvariantsLive(t *testing.T) {
	leaves := Leaves()
	if leaves == nil {
		t.Skip("Leaves() nil (no D:\\ access); skipping invariant checks")
	}
	seen := map[string]bool{}
	for _, l := range leaves {
		if l == "" {
			t.Errorf("Leaves contains empty leaf")
		}
		if strings.Contains(l, `\`) {
			t.Errorf("leaf %q should use forward slashes", l)
		}
		if seen[l] {
			t.Errorf("duplicate leaf %q", l)
		}
		seen[l] = true
		top := l
		if i := strings.Index(top, "/"); i >= 0 {
			top = top[:i]
		}
		if SystemDir(top) {
			t.Errorf("leaf %q under system dir", l)
		}
		if segs := strings.Split(l, "/"); len(segs) > 4 {
			t.Errorf("leaf %q exceeds depth cap 4", l)
		}
	}
}

func TestAuditLeavesDepthCapDocumented(t *testing.T) {
	// Leaves caps at depth 4 (D:\ = 0, max 4): documented, pinned here.
	// Live assertion: no leaf exceeds 4 segments (checked above). This test
	// additionally pins subtreeMarked's cap boundary.
	dir := t.TempDir()
	if subtreeMarked(dir, 4) && !hasMarker(dir) {
		t.Errorf("depth-4 subtreeMarked without marker should be false")
	}
}

// REGRESSION (actual behavior): CachedLeaves memoizes via sync.Once for the
// process lifetime, so later filesystem changes (new projects on D:\) are
// invisible until restart.
// BUG (no tracking issue filed): memo needs refresh/invalidation.
// This test pins memoization semantics only and NEVER touches the live
// filesystem (no Leaves() call): two consecutive CachedLeaves calls return
// identical slices.
func TestAuditCachedLeavesStalenessKnownBug(t *testing.T) {
	saveLeavesState(t)
	// Prime cache with a fake value; both calls must see the same snapshot.
	setFakeLeaves(t, []string{"stale/fake-leaf"})
	first := CachedLeaves()
	second := CachedLeaves()
	if len(first) != 1 || first[0] != "stale/fake-leaf" {
		t.Fatalf("setup failed: CachedLeaves = %v", first)
	}
	if len(second) != 1 || second[0] != "stale/fake-leaf" {
		t.Fatalf("CachedLeaves not memoized: %v", second)
	}
	_ = second
}

func TestAuditForPathDeepestAndExact(t *testing.T) {
	saveLeavesState(t)
	setFakeLeaves(t, []string{"a", "a/b"})
	cases := []struct{ path, want string }{
		{`D:\a\b\file.txt`, "a/b"},
		{`D:\a\file.txt`, "a"},
		{`D:\a\b`, "a/b"}, // exact leaf dir
		{`D:\a`, "a"},
		{`d:\A\B\FILE.TXT`, "a/b"}, // case-insensitive
		{`D:\other\file.txt`, ""},
		{`C:\a\b\file.txt`, ""},
		{`D:\a\bc\file.txt`, "a"}, // prefix boundary: a/bc must not match a/b
		{"", ""},
	}
	// D:\ roots resolve only on Windows (issue #147: the legacy D:\ root
	// exists solely on GOOS=windows; elsewhere these paths are relative
	// and match nothing).
	if runtime.GOOS != "windows" {
		cases = []struct{ path, want string }{
			{`D:\a\b\file.txt`, ""},
			{`D:\a\file.txt`, ""},
			{`D:\other\file.txt`, ""},
			{"", ""},
		}
	}
	for _, tc := range cases {
		if got := ForPath(tc.path); got != tc.want {
			t.Errorf("ForPath(%q) = %q want %q", tc.path, got, tc.want)
		}
	}
}

func TestAuditResolveLeafExactAndMiss(t *testing.T) {
	saveLeavesState(t)
	setFakeLeaves(t, []string{"a/b", "c"})
	fpMu.Lock()
	fpCache = map[string][2]string{}
	fpMu.Unlock()
	if got := ResolveLeaf("a/b"); got != "a/b" {
		t.Errorf("ResolveLeaf exact = %q want a/b", got)
	}
	if got := ResolveLeaf("c"); got != "c" {
		t.Errorf("ResolveLeaf exact single = %q", got)
	}
	if got := ResolveLeaf("no/such/leaf"); got != "" {
		t.Errorf("ResolveLeaf miss = %q want empty", got)
	}
	if got := ResolveLeaf(""); got != "" {
		t.Errorf("ResolveLeaf(empty) = %q want empty", got)
	}
}

func TestAuditResolveLeafByRepoAndRoot(t *testing.T) {
	saveLeavesState(t)
	setFakeLeaves(t, []string{"my/proj"})
	// Key by the resolved leaf dir, not a hardcoded drive (issue #147):
	// LeafDir is platform-appropriate (D:\ on Windows, home/env roots
	// elsewhere), so the fingerprint cache hits on every GOOS.
	leafDir := LeafDir("my/proj")
	fpMu.Lock()
	fpCache[leafDir] = [2]string{"https://example.com/r.git", "abc123root"}
	fpMu.Unlock()
	if got := ResolveLeaf("https://example.com/r.git"); got != "my/proj" {
		t.Errorf("ResolveLeaf by origin = %q", got)
	}
	if got := ResolveLeaf("abc123root"); got != "my/proj" {
		t.Errorf("ResolveLeaf by root = %q", got)
	}
}

// REGRESSION (actual behavior): Fingerprint returns the raw
// `git remote get-url` string (case and .git suffix preserved) instead of a
// normalized form, so ResolveLeaf exact-match misses equivalent refs.
// BUG: needs URL normalization (trailing .git, case, trailing slash, SSH vs
// HTTPS forms). Related to #86 (identity). This test pins the CURRENT raw
// return value.
func TestAuditFingerprintRawVsNormalizedKnownBug(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initCmds := [][]string{
		{"init"},
		{"remote", "add", "origin", "https://Example.com/MyRepo.git"},
	}
	for _, args := range initCmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git setup failed: %v (%s)", err, out)
		}
	}
	// Need at least one commit for root hash; use local identity.
	for _, args := range [][]string{
		{"config", "user.email", "t@t.t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "x"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		_ = cmd.Run()
	}
	fpMu.Lock()
	delete(fpCache, dir)
	fpMu.Unlock()
	origin, root := Fingerprint(dir)
	if origin == "" {
		t.Fatalf("Fingerprint origin empty after remote add")
	}
	// Pinned: raw value preserves case and .git suffix instead of normalizing.
	if origin != "https://Example.com/MyRepo.git" {
		t.Errorf("expected pinned raw URL %q, got %q (BUG: not normalized; related #86)", "https://Example.com/MyRepo.git", origin)
	}
	_ = root
	// Cached second call must agree.
	o2, r2 := Fingerprint(dir)
	if o2 != origin || r2 != root {
		t.Errorf("Fingerprint cache mismatch: (%q,%q) vs (%q,%q)", o2, r2, origin, root)
	}
}

func TestAuditFingerprintNonRepoCached(t *testing.T) {
	saveLeavesState(t)
	dir := t.TempDir()
	fpMu.Lock()
	delete(fpCache, dir)
	fpMu.Unlock()
	o1, r1 := Fingerprint(dir)
	if o1 != "" || r1 != "" {
		t.Fatalf("Fingerprint(plain) = (%q,%q) want empty", o1, r1)
	}
	o2, r2 := Fingerprint(dir)
	if o2 != o1 || r2 != r1 {
		t.Errorf("cached mismatch")
	}
}
