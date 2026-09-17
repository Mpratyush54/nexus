package mcp

// Hardened file access tests (nexus issue #96): traversal/ADS/symlink
// rejection, secret screening both directions, and audit logging.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecureJoinHardening(t *testing.T) {
	s, _ := newTestServerWithT(t)
	root := s.cfg.WorkspacePath

	// Benign relative paths join inside (compare canonical forms: temp
	// dirs may carry 8.3 short names on Windows).
	p, err := secureJoin(root, "sub/note.md")
	if err != nil {
		t.Fatalf("secureJoin benign: %v", err)
	}
	canonRoot, _ := filepath.EvalSymlinks(root)
	if canonRoot == "" {
		canonRoot = root
	}
	if p != canonRoot && !strings.HasPrefix(foldPath(p), foldPath(canonRoot)+string(filepath.Separator)) {
		t.Fatalf("joined path %q outside root %q", p, root)
	}

	// Traversal, absolute escapes, ADS colons, NUL, and blanks fail.
	outside := filepath.Join(filepath.Dir(root), "evil.md")
	for _, bad := range []string{
		"../evil.md",
		"..",
		outside,
		"notes.txt:hidden",
		"sub\x00evil.md",
		"   ",
	} {
		if _, err := secureJoin(root, bad); err == nil {
			t.Errorf("secureJoin(%q) allowed, want error", bad)
		}
	}
}

func TestSecureJoinSymlinkEscape(t *testing.T) {
	s, _ := newTestServerWithT(t)
	root := s.cfg.WorkspacePath

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "evil-link")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := secureJoin(root, "evil-link"); err == nil {
		t.Error("secureJoin via file symlink outside root allowed, want error")
	}
	okLink := filepath.Join(root, "ok-link")
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, okLink); err != nil {
		t.Fatal(err)
	}
	if _, err := secureJoin(root, "ok-link"); err != nil {
		t.Errorf("secureJoin via inside symlink rejected: %v", err)
	}
}

func TestFileWriteReadSecretBlocked(t *testing.T) {
	s, _ := newTestServerWithT(t)

	// Write path: secret-looking content never lands on disk.
	secret := "deploy with api_key = 'abcdefghij1234567890XYZ'"
	r := callTool(t, s, "file_write", map[string]any{"path": "leak.txt", "content": secret})
	if r.Error == nil {
		t.Fatal("secret write allowed, want refusal")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.WorkspacePath, "leak.txt")); !os.IsNotExist(err) {
		t.Error("secret content was written to disk despite block")
	}

	// Read path: a file that already contains a secret is not returned.
	planted := filepath.Join(s.cfg.WorkspacePath, "planted.txt")
	if err := os.WriteFile(planted, []byte("token glpat-abcdefghij1234567890 here"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = callTool(t, s, "file_read", map[string]any{"path": "planted.txt"})
	if r.Error == nil {
		t.Fatal("secret read allowed, want refusal")
	}
}

func TestFileAccessLogHook(t *testing.T) {
	s, _ := newTestServerWithT(t)
	type entry struct{ op, path string }
	var got []entry
	s.cfg.FileAccessLog = func(op, path string, size int) {
		got = append(got, entry{op, path})
	}
	if r := callTool(t, s, "file_write", map[string]any{"path": "log/me.md", "content": "hello audit log"}); r.Error != nil {
		t.Fatalf("write: %v", r.Error)
	}
	if r := callTool(t, s, "file_read", map[string]any{"path": "log/me.md"}); r.Error != nil {
		t.Fatalf("read: %v", r.Error)
	}
	if len(got) != 2 || got[0].op != "write" || got[1].op != "read" {
		t.Fatalf("audit log = %+v, want [write read]", got)
	}
	// Refused operations never log.
	_ = callTool(t, s, "file_read", map[string]any{"path": "../evil.md"})
	if len(got) != 2 {
		t.Fatalf("refused op logged: %+v", got)
	}
}
