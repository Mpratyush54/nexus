package security

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"central-memory/internal/daemon"
)

// TestValidateRejectsAllAdversarial requires ValidatePath to reject 100% of
// the lexical attack classes. The symlink class is excluded by design:
// link names are lexically innocent ("evil-link" looks benign) and only the
// runtime sandbox — which resolves the link target through the kernel — can
// judge them. Symlink safety is proven by TestDaemonSandboxHoldsAdversarial.
func TestValidateRejectsAllAdversarial(t *testing.T) {
	cases := AdversarialCases()
	if len(cases) == 0 {
		t.Fatal("empty adversarial corpus")
	}
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.Category] = true
		if c.Category == "symlink" {
			continue
		}
		if err := ValidatePath(c.Path); err == nil {
			t.Errorf("ValidatePath(%q) [%s/%s] allowed, want rejection",
				c.Path, c.Category, c.Name)
		}
	}
	// Every class named in the issue must be represented.
	for _, want := range []string{"dotdot", "symlink", "ads", "nul", "absolute", "doubleslash", "trailingdot"} {
		found := false
		for _, c := range cases {
			if c.Category == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("corpus missing category %q", want)
		}
		_ = seen
	}
}

// TestValidateAllowsBenign guards against over-blocking: normal workspace
// paths must pass the strict pre-check.
func TestValidateAllowsBenign(t *testing.T) {
	for _, ok := range []string{
		"hello.txt",
		"sub/dir/note.txt",
		"a-b_c/d.e",
		".cursorrules",
		".github/copilot-instructions.md",
	} {
		if err := ValidatePath(ok); err != nil {
			t.Errorf("ValidatePath(%q) rejected benign path: %v", ok, err)
		}
	}
}

// TestDaemonSandboxHoldsAdversarial proves the existing sandbox
// (daemon.SecureJoin, unmodified per issue scope) safely handles the full
// corpus: no case may resolve outside the root. True escapes must be
// rejected with an error; confusing-but-contained inputs (rooted paths that
// Join folds inside per TestSecureJoinRootedJoinsInside, interior "//" that
// Clean absorbs, trailing-dot aliases that canonicalize inside) pass by
// staying contained. Symlink entries are materialized as real links pointing
// outside the root first.
//
// NOTE on root canonicalization: SecureJoin returns kernel-canonical paths
// (long names, junctions resolved), while t.TempDir() may carry 8.3 short
// segments (PRATYU~1). Containment is therefore measured against the
// canonical root from SecureJoin(root, "."), never the raw temp string.
func TestDaemonSandboxHoldsAdversarial(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOK := false
	if err := os.Symlink(secret, filepath.Join(root, "evil-link")); err == nil {
		symlinkOK = true
	} else {
		t.Logf("file symlink unavailable: %v", err)
	}
	dirLinked := false
	if err := os.Symlink(outside, filepath.Join(root, "evil-dir")); err == nil {
		dirLinked = true
	} else if runtime.GOOS == "windows" && tryJunction(outside, filepath.Join(root, "evil-dir")) {
		dirLinked = true
	} else {
		t.Logf("dir symlink/junction unavailable: %v", err)
	}
	canonRoot, err := daemon.SecureJoin(root, ".")
	if err != nil {
		t.Fatalf("SecureJoin(root, .) for canonical root: %v", err)
	}

	passed := 0
	skipped := 0
	for _, c := range AdversarialCases() {
		if c.Category == "symlink" && !symlinkOK && !dirLinked {
			skipped++
			continue
		}
		if c.Name == "symlink file" && !symlinkOK {
			skipped++
			continue
		}
		if (c.Name == "symlink dir" || c.Name == "symlink dotdot") && !dirLinked {
			skipped++
			continue
		}
		got, err := daemon.SecureJoin(root, c.Path)
		if err != nil {
			passed++
			continue
		}
		// Resolved without error: must still be contained in the canonical
		// root. Anything resolving inside is safe (not an escape), even if
		// the strict pre-check would have rejected the spelling.
		rel, relErr := filepath.Rel(canonRoot, got)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("SecureJoin(%q) [%s] escaped to %q, want rejection/containment",
				c.Path, c.Name, got)
			continue
		}
		passed++
	}
	t.Logf("sandbox held %d/%d adversarial cases (%d skipped: no symlink privilege)", passed, len(AdversarialCases()), skipped)
	if passed+skipped != len(AdversarialCases()) {
		t.Fatalf("not all adversarial cases accounted for: passed=%d skipped=%d total=%d",
			passed, skipped, len(AdversarialCases()))
	}
}

// TestSecretEnforceFailClosed: every NeverPattern family blocks; benign prose
// (including docs that merely mention "api_key") passes.
func TestSecretEnforceFailClosed(t *testing.T) {
	secrets := []string{
		"token glpat-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 here",
		"login with ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 now",
		"key sk-ant-abcDEF123-xyz789 here",
		"aws AKIAIOSFODNN7EXAMPLE here",
		"my api_key = 'abcdefghij1234567890XYZ'",
		`config api-key: "abcdefghij1234567890XYZ"`,
	}
	for _, s := range secrets {
		if err := EnforceSecretString(s); err == nil {
			t.Errorf("EnforceSecret allowed secret %q, want ErrSecretBlocked", s)
		}
		if !ContainsSecret([]byte(s)) {
			t.Errorf("ContainsSecret missed %q", s)
		}
	}
	benign := []string{
		"hello world",
		"sandboxed content here",
		"docs mention api_key but carry no long token value",
		"",
	}
	for _, s := range benign {
		if err := EnforceSecretString(s); err != nil {
			t.Errorf("EnforceSecret blocked benign %q: %v", s, err)
		}
	}
}

// TestSecretMatchesDaemonSemantics keeps the wrapper consistent with the
// daemon's fail-closed behavior: blocked content must never reach disk and a
// pre-existing secret file must not be readable through the daemon.
func TestSecretMatchesDaemonSemantics(t *testing.T) {
	root := t.TempDir()
	leak := "my api_key = 'abcdefghij1234567890XYZ'"
	if err := EnforceSecretString(leak); err == nil {
		t.Fatal("wrapper allowed secret the daemon must block")
	}
	if err := daemon.WriteFile(root, "leak.txt", []byte(leak)); err == nil {
		t.Error("daemon WriteFile allowed secret, want block")
	}
	raw := "token glpat-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 here"
	if err := os.WriteFile(filepath.Join(root, "seeded.txt"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.ReadFile(root, "seeded.txt"); err == nil {
		t.Error("daemon ReadFile of secret allowed, want block")
	}
}

// TestVisibilityEnforce covers the private-vs-shared matrix, including the
// fail-closed normalization of unknown visibility values.
func TestVisibilityEnforce(t *testing.T) {
	cases := []struct {
		name    string
		viewer  string
		owner   string
		vis     Visibility
		wantOK  bool
	}{
		{"owner reads private", "alice", "alice", VisibilityPrivate, true},
		{"owner reads shared", "alice", "alice", VisibilityShared, true},
		{"stranger blocked on private", "bob", "alice", VisibilityPrivate, false},
		{"member reads shared", "bob", "alice", VisibilityShared, true},
		{"anonymous denied shared", "", "alice", VisibilityShared, false},
		{"anonymous denied private", "", "alice", VisibilityPrivate, false},
		{"unknown visibility fails closed", "bob", "alice", Visibility("confidential"), false},
		{"empty visibility fails closed", "bob", "alice", Visibility(""), false},
		{"owner wins on unknown visibility", "alice", "alice", Visibility("bogus"), true},
	}
	for _, c := range cases {
		if got := CanAccess(c.viewer, c.owner, c.vis); got != c.wantOK {
			t.Errorf("%s: CanAccess=%v want %v", c.name, got, c.wantOK)
		}
		if err := EnforceBranchAccess(c.viewer, c.owner, c.vis); (err == nil) != c.wantOK {
			t.Errorf("%s: EnforceBranchAccess err=%v wantOK=%v", c.name, err, c.wantOK)
		}
	}
	if NormalizeVisibility(VisibilityShared) != VisibilityShared {
		t.Error("NormalizeVisibility mangled shared")
	}
	if NormalizeVisibility(Visibility("weird")) != VisibilityPrivate {
		t.Error("NormalizeVisibility did not fail closed to private")
	}
}

// TestRateLimiterPresets enforces the plan §6.4 caps: 100/s events, 50/s
// fileops (burst-sized buckets exhausted deterministically, no sleeps).
func TestRateLimiterPresets(t *testing.T) {
	ev := NewEventLimiter()
	for i := 0; i < 100; i++ {
		if !ev.Allow() {
			t.Fatalf("event limiter denied within burst at %d", i)
		}
	}
	if ev.Allow() {
		t.Error("event limiter allowed 101st immediate event, want rate-limited")
	}

	fo := NewFileOpsLimiter()
	for i := 0; i < 50; i++ {
		if !fo.Allow() {
			t.Fatalf("fileops limiter denied within burst at %d", i)
		}
	}
	if fo.Allow() {
		t.Error("fileops limiter allowed 51st immediate op, want rate-limited")
	}
}

// TestRateLimiterRefill proves the bucket is a refilling token bucket, not a
// one-shot counter: after ~200ms at 20/s, ~4 tokens are back.
func TestRateLimiterRefill(t *testing.T) {
	l := NewLimiter(20, 20)
	for i := 0; i < 20; i++ {
		if !l.Allow() {
			t.Fatalf("denied within burst at %d", i)
		}
	}
	if l.Allow() {
		t.Fatal("allowed over burst, want limited")
	}
	time.Sleep(200 * time.Millisecond)
	refilled := 0
	for i := 0; i < 20; i++ {
		if l.Allow() {
			refilled++
		}
	}
	if refilled < 2 || refilled > 8 {
		t.Errorf("refilled=%d after 200ms at 20/s, want ~4 (bounds 2..8)", refilled)
	}
}

// TestRateLimiterConcurrent exercises the mutex under -race.
func TestRateLimiterConcurrent(t *testing.T) {
	l := NewLimiter(1000, 1000)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				l.Allow()
			}
		}()
	}
	wg.Wait()
	if l.Allow() {
		t.Error("limiter still admits after 1600 draws on 1000-bucket, want exhausted")
	}
}

// TestRateLimiterRejectsNonPositiveN fails closed on caller bugs.
func TestRateLimiterRejectsNonPositiveN(t *testing.T) {
	l := NewLimiter(100, 100)
	if l.AllowN(0) || l.AllowN(-1) {
		t.Error("AllowN(<=0) admitted, want rejection")
	}
}

func tryJunction(target, link string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		println(string(out))
		return false
	}
	return true
}
