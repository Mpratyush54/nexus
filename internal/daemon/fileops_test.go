package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInSandboxAdversarial(t *testing.T) {
	d := newTestDaemon(t)
	root := d.Root

	allowed := []string{
		"a.txt",
		"sub/dir/b.txt",
		"./c.txt",
		"sub/../d.txt", // cleans back inside
	}
	for _, p := range allowed {
		if _, err := ResolveInSandbox(root, p); err != nil {
			t.Errorf("ResolveInSandbox(%q) rejected: %v", p, err)
		}
	}

	// Absolute escape outside root (Unix + Windows flavours).
	absEscapes := []string{
		"/etc/passwd",
		"/",
		`C:\Windows\System32\drivers\etc\hosts`,
	}
	for _, p := range absEscapes {
		if _, err := ResolveInSandbox(root, p); err == nil {
			t.Errorf("ResolveInSandbox(%q) allowed absolute escape", p)
		}
	}

	traversals := []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../..",
		"sub/../../../etc/passwd",
		"..",
		"a/../../../../etc/passwd",
	}
	for _, p := range traversals {
		if _, err := ResolveInSandbox(root, p); err == nil {
			t.Errorf("ResolveInSandbox(%q) allowed traversal", p)
		}
	}

	// Sibling-prefix attack: <root>-sibling must not pass the HasPrefix check.
	sibling := root + "-sibling"
	if _, err := ResolveInSandbox(root, sibling); err == nil {
		t.Errorf("ResolveInSandbox sibling-prefix %q allowed", sibling)
	}
}

func TestFileReadWriteRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	if err := WriteFileSandboxed(d.Root, "notes/hello.txt", []byte("hello world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := ReadFileSandboxed(d.Root, "notes/hello.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q", data)
	}
}

func TestFileHTTPStatuses(t *testing.T) {
	d := newTestDaemon(t)
	if err := WriteFileSandboxed(d.Root, "ok.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}

	// Traversal → 403 (read and write).
	for _, body := range []string{
		`{"path":"../../etc/passwd"}`,
		`{"path":"/etc/passwd"}`,
		`{"path":"..\\..\\windows\\win.ini"}`,
	} {
		req := authReq(t, d, http.MethodPost, "/file/read", body)
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("read %s: got %d, want 403", body, rec.Code)
		}
		wreq := authReq(t, d, http.MethodPost, "/file/write", `{"path":"../../x.txt","content":"hi"}`)
		wrec := httptest.NewRecorder()
		d.Handler().ServeHTTP(wrec, wreq)
		if wrec.Code != http.StatusForbidden {
			t.Errorf("write traversal: got %d, want 403", wrec.Code)
		}
	}

	// Secret content → 403 on write; secret-bearing file → 403 on read.
	secret := "api_key = 'abcdefghij1234567890XYZ'"
	req := authReq(t, d, http.MethodPost, "/file/write", `{"path":"s.txt","content":"`+secret+`"}`)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("write secret: got %d, want 403", rec.Code)
	}
	// Plant a secret file directly (bypassing the write guard) and confirm read refuses.
	_ = os.WriteFile(filepath.Join(d.Root, "planted.txt"), []byte("ghp_"+"abcdefghijklmnopqrstuvwxyz0123456789"), 0o644)
	rreq := authReq(t, d, http.MethodPost, "/file/read", `{"path":"planted.txt"}`)
	rrec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rrec, rreq)
	if rrec.Code != http.StatusForbidden {
		t.Errorf("read secret file: got %d, want 403", rrec.Code)
	}

	// Missing file → 404.
	mreq := authReq(t, d, http.MethodPost, "/file/read", `{"path":"nope.txt"}`)
	mrec := httptest.NewRecorder()
	d.Handler().ServeHTTP(mrec, mreq)
	if mrec.Code != http.StatusNotFound {
		t.Errorf("missing file: got %d, want 404", mrec.Code)
	}
}

func TestReadCap1MB(t *testing.T) {
	d := newTestDaemon(t)
	big := make([]byte, MaxReadBytes+16)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(d.Root, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileSandboxed(d.Root, "big.bin"); err == nil {
		t.Fatal("over-cap read allowed")
	} else if !strings.Contains(err.Error(), "1MB") {
		t.Fatalf("unexpected error: %v", err)
	}
	req := authReq(t, d, http.MethodPost, "/file/read", `{"path":"big.bin"}`)
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("big file HTTP: got %d, want 413", rec.Code)
	}
}
