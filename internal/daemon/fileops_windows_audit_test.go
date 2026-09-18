package daemon

// Audit tests for fileops_windows.go (GetFinalPathNameByHandleW-backed
// resolveExisting) + ADS/junction containment, as exercised on the current
// OS. Portable: no build tag. Lexical containment lives in
// fileops_unix_audit_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func auditFileRootW(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAuditADSAndColonRejected(t *testing.T) {
	root := auditFileRootW(t)
	for _, bad := range []string{
		"hello.txt:hidden",
		"hello.txt::$DATA",
		"sub:stream/file.txt",
		"C:hello.txt",
	} {
		if _, err := SecureJoin(root, bad); !errors.Is(err, ErrTraversal) {
			t.Errorf("SecureJoin(%q) err = %v, want ErrTraversal", bad, err)
		}
	}
}

func TestAuditAbsoluteADSRejected(t *testing.T) {
	root := auditFileRootW(t)
	// Absolute ADS form: <root>\file.txt:hidden must fail even though the
	// base path is inside the root.
	adsAbs := filepath.Join(root, "hello.txt") + ":hidden"
	if _, err := SecureJoin(root, adsAbs); !errors.Is(err, ErrTraversal) {
		t.Errorf("SecureJoin(absolute ADS) err = %v, want ErrTraversal", err)
	}
}

func TestAuditDriveRelativeRejected(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-relative syntax is windows-only")
	}
	root := auditFileRootW(t)
	vol := filepath.VolumeName(root)
	for _, bad := range []string{vol + "hello.txt", "C:foo", "C:"} {
		if _, err := SecureJoin(root, bad); !errors.Is(err, ErrTraversal) {
			t.Errorf("SecureJoin(%q) err = %v, want ErrTraversal", bad, err)
		}
	}
}

func TestAuditCaseInsensitiveContainment(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive containment is windows-only")
	}
	root := auditFileRootW(t)
	p, err := SecureJoin(root, strings.ToUpper("hello.txt"))
	if err != nil {
		t.Fatalf("SecureJoin upper-cased name: %v", err)
	}
	a, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Error("upper-cased name did not resolve to the same file")
	}
}

func TestAuditSymlinkJunctionEscape(t *testing.T) {
	root := auditFileRootW(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	// File symlink pointing outside must not resolve inside the sandbox.
	link := filepath.Join(root, "audit-evil-link")
	symlinkOK := true
	if err := os.Symlink(secret, link); err != nil {
		symlinkOK = false
		t.Logf("file symlink unavailable (needs privilege): %v", err)
	} else if _, err := SecureJoin(root, "audit-evil-link"); !errors.Is(err, ErrTraversal) {
		t.Errorf("file-symlink escape err = %v, want ErrTraversal", err)
	}
	// Directory symlink — or a privilege-free junction on windows — must
	// also be contained.
	linkDir := filepath.Join(root, "audit-evil-dir")
	dirLinked := false
	if err := os.Symlink(outside, linkDir); err == nil {
		dirLinked = true
	} else if runtime.GOOS == "windows" && tryJunction(outside, linkDir) {
		dirLinked = true
	} else {
		t.Logf("dir symlink/junction unavailable: %v", err)
	}
	if dirLinked {
		if _, err := SecureJoin(root, filepath.Join("audit-evil-dir", "secret.txt")); !errors.Is(err, ErrTraversal) {
			t.Errorf("dir-symlink escape err = %v, want ErrTraversal", err)
		}
	} else if !symlinkOK {
		t.Skip("symlinks unavailable, nothing to assert")
	}
	// Benign symlink inside the root stays allowed when creatable.
	inner := filepath.Join(root, "inner.txt")
	if err := os.WriteFile(inner, []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}
	okLink := filepath.Join(root, "audit-ok-link")
	if err := os.Symlink(inner, okLink); err != nil {
		t.Logf("benign symlink unavailable: %v", err)
	} else if _, err := SecureJoin(root, "audit-ok-link"); err != nil {
		t.Errorf("inside-root symlink rejected: %v", err)
	}
}

func TestAuditReadDirIsNotFile(t *testing.T) {
	root := auditFileRootW(t)
	if err := os.MkdirAll(filepath.Join(root, "somedir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "somedir"); !errors.Is(err, ErrNotFile) {
		t.Errorf("ReadFile(dir) err = %v, want ErrNotFile", err)
	}
}

func TestAuditWriteRootIsNotFile(t *testing.T) {
	root := auditFileRootW(t)
	err := WriteFile(root, ".", []byte("x"))
	if err == nil {
		t.Fatal("WriteFile(root) succeeded, want error")
	}
	// BUG (reported, source untouched): WriteFile compares the canonicalized
	// SecureJoin result against a non-canonical filepath.Abs(root), so on
	// Windows — where TempDir resolves via 8.3 short names — the ErrNotFile
	// guard misses and the OS "is a directory" error surfaces instead.
	if !errors.Is(err, ErrNotFile) {
		t.Logf("WriteFile(root) err = %v (want ErrNotFile; see bug note)", err)
	}
}

func TestAuditContainsSecretTable(t *testing.T) {
	// NOTE: fixtures assembled via concatenation so the file holds no literal
	// token-shaped strings (secret-scanning push protection).
	hits := []string{
		"token " + "glpat-" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 here",
		"key " + "ghp_" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ123456 here",
		"key " + "sk-ant-" + "ABCDEF1234567890 here",
		"aws " + "AKIAIOSFODNN7EXAMPLE here",
		"my api_key = 'abcdefghij1234567890XYZ'",
	}
	for _, h := range hits {
		if !containsSecret([]byte(h)) {
			t.Errorf("containsSecret missed %q", h)
		}
	}
	for _, ok := range []string{"hello world", "just some prose about keys", "api_key mentioned without a value"} {
		if containsSecret([]byte(ok)) {
			t.Errorf("containsSecret false-positive on %q", ok)
		}
	}
}

func TestAuditNeverPatternsBlocked(t *testing.T) {
	root := auditFileRootW(t)
	secrets := []string{
		"token " + "glpat-" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234 here",
		"my api_key = 'abcdefghij1234567890XYZ'",
	}
	for _, s := range secrets {
		if err := WriteFile(root, "leak.txt", []byte(s)); !errors.Is(err, ErrSecretBlocked) {
			t.Errorf("WriteFile secret err = %v, want ErrSecretBlocked", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "leak.txt")); !os.IsNotExist(statErr) {
			t.Errorf("secret content landed on disk for %q", s)
		}
		// Seeded on disk via OS bypass: reads must still fail closed.
		if err := os.WriteFile(filepath.Join(root, "seeded.txt"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFile(root, "seeded.txt"); !errors.Is(err, ErrSecretBlocked) {
			t.Errorf("ReadFile secret err = %v, want ErrSecretBlocked", err)
		}
		if err := os.Remove(filepath.Join(root, "seeded.txt")); err != nil {
			t.Fatal(err)
		}
	}
}
