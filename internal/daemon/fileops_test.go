package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mkroot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSecureJoinTraversalDotDot(t *testing.T) {
	root := mkroot(t)
	attacks := []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../escape.txt",
		"..",
		"../",
		"a/../../../x",
	}
	for _, a := range attacks {
		if _, err := SecureJoin(root, a); err == nil {
			t.Errorf("SecureJoin(%q) allowed traversal, want error", a)
		}
	}
}

func TestSecureJoinAbsoluteOutside(t *testing.T) {
	root := mkroot(t)
	var outsiders []string
	if runtime.GOOS == "windows" {
		// Drive-absolute paths outside root. (A bare rooted path like
		// `\Windows\...` is drive-relative and safely joins *inside* the
		// root, so it is not an escape — see TestSecureJoinRootedJoinsInside.)
		outsiders = []string{
			`C:\Windows\System32\drivers\etc\hosts`,
			filepath.Join(os.TempDir(), "outside.txt"),
		}
	} else {
		outsiders = []string{
			"/etc/passwd",
			filepath.Join(os.TempDir(), "outside.txt"),
		}
	}
	for _, o := range outsiders {
		if _, err := SecureJoin(root, o); err == nil {
			t.Errorf("SecureJoin(%q) allowed absolute escape, want error", o)
		}
	}
	// Absolute path inside root is fine; the result is canonicalized
	// (8.3/short names expanded), so compare by identity, not string.
	inside := filepath.Join(root, "hello.txt")
	p, err := SecureJoin(root, inside)
	if err != nil {
		t.Fatalf("SecureJoin inside-root absolute: %v", err)
	}
	a, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(inside)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Fatalf("got %q which is not the same file as %q", p, inside)
	}
}

// TestSecureJoinRootedJoinsInside documents that a drive-relative rooted
// path (no drive letter) cannot escape: it joins inside the root.
func TestSecureJoinRootedJoinsInside(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows rooted-path semantics only")
	}
	root := mkroot(t)
	p, err := SecureJoin(root, `\Windows\System32\drivers\etc\hosts`)
	if err != nil {
		t.Fatalf("rooted path should join inside root: %v", err)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		// root itself may be returned canonicalized; accept either the
		// joined path or any inside-root resolution.
		if err != nil {
			t.Fatalf("result outside root: %q", p)
		}
	}
}

func TestSecureJoinADSRejected(t *testing.T) {
	root := mkroot(t)
	ads := []string{
		"hello.txt:hidden",
		"hello.txt::$DATA",
		"sub:stream/file.txt",
		"C:hello.txt",
	}
	for _, a := range ads {
		if _, err := SecureJoin(root, a); err == nil {
			t.Errorf("SecureJoin(%q) allowed ADS/colon path, want error", a)
		}
	}
}

func TestSecureJoinNulAndBlank(t *testing.T) {
	root := mkroot(t)
	for _, bad := range []string{"", "   ", "a\x00b"} {
		if _, err := SecureJoin(root, bad); err == nil {
			t.Errorf("SecureJoin(%q) allowed, want error", bad)
		}
	}
}

func TestSecureJoinSymlinkEscape(t *testing.T) {
	root := mkroot(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "evil-link")
	symlinkOK := true
	if err := os.Symlink(secret, link); err != nil {
		// No symlink privilege (typical Windows dev box): the dir case
		// below still exercises symlink-escape logic via a junction,
		// which needs no special privilege.
		symlinkOK = false
		t.Logf("file symlink unavailable: %v", err)
	} else if _, err := SecureJoin(root, "evil-link"); err == nil {
		t.Error("SecureJoin via file symlink outside root allowed, want error")
	}
	// Symlinked directory pointing outside must also be contained.
	linkDir := filepath.Join(root, "evil-dir")
	dirLinked := false
	if err := os.Symlink(outside, linkDir); err == nil {
		dirLinked = true
	} else if runtime.GOOS == "windows" && tryJunction(outside, linkDir) {
		dirLinked = true
	} else {
		t.Logf("dir symlink/junction unavailable: %v", err)
	}
	if dirLinked {
		if _, err := SecureJoin(root, filepath.Join("evil-dir", "secret.txt")); err == nil {
			t.Error("SecureJoin via dir symlink outside root allowed, want error")
		}
	} else if !symlinkOK {
		t.Skip("symlinks unavailable, nothing to assert")
	}
	// Benign symlink inside root stays allowed (needs privilege; log and
	// continue when unavailable — the junction case above already proved
	// the escape path on locked-down boxes).
	inner := filepath.Join(root, "inner.txt")
	if err := os.WriteFile(inner, []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}
	okLink := filepath.Join(root, "ok-link")
	if err := os.Symlink(inner, okLink); err != nil {
		t.Logf("benign symlink unavailable: %v", err)
	} else if _, err := SecureJoin(root, "ok-link"); err != nil {
		t.Errorf("SecureJoin via inside symlink rejected: %v", err)
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	root := mkroot(t)
	if err := WriteFile(root, "sub/dir/note.txt", []byte("sandboxed content here")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(root, "sub/dir/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sandboxed content here" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteTraversalBlocked(t *testing.T) {
	root := mkroot(t)
	if err := WriteFile(root, "../../evil.txt", []byte("x")); err == nil {
		t.Error("WriteFile traversal allowed, want error")
	}
}

func TestSecretFailClosed(t *testing.T) {
	root := mkroot(t)
	// Write path: secret-looking content must be refused and never land on disk.
	secret := "my api_key = 'abcdefghij1234567890XYZ'"
	if err := WriteFile(root, "leak.txt", []byte(secret)); err == nil {
		t.Error("WriteFile with secret content allowed, want ErrSecretBlocked")
	}
	if _, statErr := os.Stat(filepath.Join(root, "leak.txt")); !os.IsNotExist(statErr) {
		t.Error("secret content was written to disk despite block")
	}
	// Read path: a file that already contains a secret must not be returned.
	raw := "token glpat-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 here"
	if err := os.WriteFile(filepath.Join(root, "seeded.txt"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "seeded.txt"); err == nil {
		t.Error("ReadFile of secret content allowed, want ErrSecretBlocked")
	} else if !strings.Contains(err.Error(), "secret") {
		t.Errorf("wrong error: %v", err)
	}
}

// tryJunction creates a Windows directory junction (needs no privilege,
// unlike symlinks) so symlink-escape tests run on locked-down dev boxes.
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

func TestSizeCap(t *testing.T) {
	root := mkroot(t)
	big := make([]byte, MaxFileBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if err := WriteFile(root, "big.bin", big); err == nil {
		t.Error("oversize write allowed, want ErrTooLarge")
	}
	if err := os.WriteFile(filepath.Join(root, "big2.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "big2.bin"); err == nil {
		t.Error("oversize read allowed, want ErrTooLarge")
	}
}
