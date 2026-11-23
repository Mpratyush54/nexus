package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// gitCheckout makes dir a git repository with the given origin.
func gitCheckout(t *testing.T, dir, origin string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestNormalizeOrigin(t *testing.T) {
	for _, u := range []string{
		"https://github.com/Owner/Repo.git", "git@github.com:owner/repo.git",
		"https://user@github.com/owner/repo/", "ssh://git@github.com/owner/repo",
	} {
		if got := normalizeOrigin(u); got != "github.com/owner/repo" {
			t.Errorf("normalizeOrigin(%q) = %q", u, got)
		}
	}
}

func TestResolveEditPathOtherCheckoutOfSameRepo(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "old-clone")
	other := filepath.Join(base, "work")
	foreign := filepath.Join(base, "foreign")
	gitCheckout(t, root, "https://github.com/acme/nexus.git")
	gitCheckout(t, other, "git@github.com:Acme/nexus.git")
	gitCheckout(t, foreign, "https://github.com/acme/other.git")

	cases := []struct {
		cwd, path, base, rel string
		ok                   bool
	}{
		{"", filepath.Join(root, "a", "x.go"), root, "a/x.go", true},
		{"", "a/x.go", root, "a/x.go", true},
		// Absolute path in another checkout of the same repository.
		{"", filepath.Join(other, "internal", "core", "nxcall.go"), other, "internal/core/nxcall.go", true},
		// Relative path from a chat working in that checkout.
		{other, "internal/y.go", other, "internal/y.go", true},
		{"", filepath.Join(foreign, "z.go"), "", "", false},
		{"", filepath.Join(base, "loose.go"), "", "", false},
		{"", filepath.Join(root, "node_modules", "p", "i.js"), "", "", false},
	}
	for _, c := range cases {
		loc, ok := resolveEditPath(root, c.cwd, c.path)
		if ok != c.ok || (ok && (loc.rel != c.rel || !strings.EqualFold(loc.base, c.base))) {
			t.Errorf("resolveEditPath(%q, %q) = %+v %v, want %s %s %v", c.cwd, c.path, loc, ok, c.base, c.rel, c.ok)
		}
	}
}

// A chat attributed to a stale clone edits a second checkout of the same
// repository: its links and hunks are reported, located in the file the
// agent changed.
func TestCodeLinksReporterLocatesEditsInOtherCheckout(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "old-clone")
	other := filepath.Join(base, "work")
	gitCheckout(t, root, "https://github.com/acme/nexus.git")
	gitCheckout(t, other, "https://github.com/acme/nexus")
	file := filepath.Join(other, "internal", "x.go")
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	if err := os.WriteFile(file, []byte("package x\n\nfunc A() int {\n\treturn 2\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)
	rep := NewCodeLinksReporter(root, hs.mirrorConnection)
	rep.NoteEvents("", "native-1", "claude", []Event{turnWithCalls(map[string]any{
		"name": "Edit", "id": "toolu_1",
		"input": map[string]any{"file_path": file, "old_string": "\treturn 1", "new_string": "\treturn 2"},
	})})
	if n := rep.Flush(context.Background()); n != 1 {
		t.Fatalf("sent = %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	links, _ := bodies[0]["links"].([]any)
	changes, _ := bodies[0]["changes"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["path"] != "internal/x.go" || links[0].(map[string]any)["hash"] == "" {
		t.Fatalf("links = %v", links)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	c := changes[0].(map[string]any)
	if c["path"] != "internal/x.go" || c["start_line"] != float64(4) || c["after_snippet"] != "\treturn 2" {
		t.Fatalf("change = %v", c)
	}
}

func TestChunkCodeChanges(t *testing.T) {
	var many []codeChange
	for i := 0; i < 1200; i++ {
		many = append(many, codeChange{Path: "a.go", StartLine: i + 1, EndLine: i + 1})
	}
	chunks := chunkCodeChanges(many)
	if len(chunks) != 3 || len(chunks[0]) != maxChangesPerReport || len(chunks[2]) != 200 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	big := make([]string, 2000)
	for i := range big {
		big[i] = "0123456789abcdef"
	}
	var heavy []codeChange
	for i := 0; i < 60; i++ {
		heavy = append(heavy, codeChange{Path: "b.go", StartLine: 1, EndLine: 2000, AddedHashes: big})
	}
	total := 0
	for _, ch := range chunkCodeChanges(heavy) {
		raw, _ := json.Marshal(ch)
		if len(raw) > 2<<20-64<<10 {
			t.Fatalf("chunk of %d bytes", len(raw))
		}
		total += len(ch)
	}
	if total != len(heavy) {
		t.Fatalf("lost hunks: %d", total)
	}
	if chunkCodeChanges(nil) != nil {
		t.Fatal("nil in, chunks out")
	}
}

// Any 400 other than the unknown-field rejection keeps hunks on.
func TestCodeLinksReporterBadRequestKeepsHunks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.go")
	if err := os.WriteFile(file, []byte("x := 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			http.Error(w, `{"error":"too many changes"}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)
	rep := NewCodeLinksReporter(root, hs.mirrorConnection)
	rep.NoteEvents("", "n1", "claude", []Event{turnWithCalls(map[string]any{
		"name": "Edit", "input": map[string]any{"file_path": file, "old_string": "x := 1", "new_string": "x := 2"},
	})})
	if n := rep.Flush(context.Background()); n != 1 {
		t.Fatalf("sent = %d", n)
	}
	rep.mu.Lock()
	off := rep.noChanges
	rep.mu.Unlock()
	if off || calls != 2 {
		t.Fatalf("noChanges=%v calls=%d", off, calls)
	}
}
