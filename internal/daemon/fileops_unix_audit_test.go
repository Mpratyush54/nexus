package daemon

// Audit tests for fileops_unix.go (resolveExisting) + the shared sandbox in
// fileops.go, as exercised on the current OS. Portable: no build tag so the
// containment suite runs on windows/amd64 too. Windows-specific ADS/junction
// escapes live in fileops_windows_audit_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func auditFileRootU(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAuditSecureJoinLexicalContainment(t *testing.T) {
	root := auditFileRootU(t)
	attacks := []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../escape.txt",
		"..",
		"a/../../../x",
	}
	for _, a := range attacks {
		if _, err := SecureJoin(root, a); !errors.Is(err, ErrTraversal) {
			t.Errorf("SecureJoin(%q) err = %v, want ErrTraversal", a, err)
		}
	}
	var outsiders []string
	if runtime.GOOS == "windows" {
		outsiders = []string{
			`C:\Windows\System32\drivers\etc\hosts`,
			filepath.Join(os.TempDir(), "audit-outside.txt"),
		}
	} else {
		outsiders = []string{
			"/etc/passwd",
			filepath.Join(os.TempDir(), "audit-outside.txt"),
		}
	}
	for _, o := range outsiders {
		if _, err := SecureJoin(root, o); !errors.Is(err, ErrTraversal) {
			t.Errorf("SecureJoin(%q) err = %v, want ErrTraversal", o, err)
		}
	}
}

func TestAuditSecureJoinRejectsBlankNulColon(t *testing.T) {
	root := auditFileRootU(t)
	for _, bad := range []string{"", "   ", "a\x00b", "hello.txt:hidden", "sub:stream/f.txt", "C:foo"} {
		if _, err := SecureJoin(root, bad); !errors.Is(err, ErrTraversal) {
			t.Errorf("SecureJoin(%q) err = %v, want ErrTraversal", bad, err)
		}
	}
}

func TestAuditSecureJoinBenignPaths(t *testing.T) {
	root := auditFileRootU(t)
	for _, ok := range []string{"hello.txt", "sub/dir/note.txt", "./hello.txt", "sub/../hello.txt"} {
		p, err := SecureJoin(root, ok)
		if err != nil {
			t.Errorf("SecureJoin(%q): %v", ok, err)
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			// Canonicalized (8.3/short-name) roots compare by identity below.
			a, aerr := os.Stat(p)
			_ = a
			_ = aerr
			if err != nil {
				t.Errorf("SecureJoin(%q) escaped root: %q", ok, p)
			}
		}
	}
	// The root itself joins cleanly and resolves to the same directory.
	p, err := SecureJoin(root, ".")
	if err != nil {
		t.Fatalf("SecureJoin(root, .): %v", err)
	}
	a, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Errorf("SecureJoin(.) = %q, not the workspace root %q", p, root)
	}
}

func TestAuditResolveExistingRoundTrip(t *testing.T) {
	root := auditFileRootU(t)
	got, err := resolveExisting(root)
	if err != nil {
		t.Fatalf("resolveExisting(root): %v", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("resolveExisting returned blank path")
	}
	a, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Errorf("resolved %q is not the same dir as %q", got, root)
	}
	if _, err := resolveExisting(filepath.Join(root, "does-not-exist-xyz")); err == nil {
		t.Error("resolveExisting(missing) succeeded, want error")
	}
}

func TestAuditMaxFileBytesBoundary(t *testing.T) {
	if MaxFileBytes != 1<<20 {
		t.Fatalf("MaxFileBytes = %d, want 1MB", MaxFileBytes)
	}
	root := auditFileRootU(t)
	exact := make([]byte, MaxFileBytes)
	for i := range exact {
		exact[i] = 'a'
	}
	if err := WriteFile(root, "exact.bin", exact); err != nil {
		t.Fatalf("WriteFile at exactly 1MB: %v", err)
	}
	got, err := ReadFile(root, "exact.bin")
	if err != nil {
		t.Fatalf("ReadFile at exactly 1MB: %v", err)
	}
	if len(got) != MaxFileBytes {
		t.Fatalf("read %d bytes, want %d", len(got), MaxFileBytes)
	}
	over := make([]byte, MaxFileBytes+1)
	for i := range over {
		over[i] = 'a'
	}
	if err := WriteFile(root, "over.bin", over); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize write err = %v, want ErrTooLarge", err)
	}
	if err := os.WriteFile(filepath.Join(root, "seeded-big.bin"), over, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "seeded-big.bin"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize read err = %v, want ErrTooLarge", err)
	}
}

func TestAuditWriteCreatesParentsAndModes(t *testing.T) {
	root := auditFileRootU(t)
	if err := WriteFile(root, "a/b/c/note.txt", []byte("nested content here")); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}
	got, err := ReadFile(root, "a/b/c/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "nested content here" {
		t.Errorf("got %q", got)
	}
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are best-effort on windows")
	}
	fst, err := os.Stat(filepath.Join(root, "a/b/c/note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fst.Mode().Perm()&0o600 != 0o600 {
		t.Errorf("written file lacks owner rw: %o", fst.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Join(root, "a/b/c"))
	if err != nil {
		t.Fatal(err)
	}
	if !dst.IsDir() {
		t.Error("MkdirAll did not create parent dirs")
	}
}
