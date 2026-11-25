package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func turnWithCalls(calls ...map[string]any) Event {
	flat := make([]any, len(calls))
	for i, c := range calls {
		flat[i] = c
	}
	return Event{Type: EventConversationTurn, Payload: map[string]any{"session_id": "s", "tool_calls": flat}}
}

func TestTouchedPathsFromEventsShapes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	abs := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	codexPatch := "*** Begin Patch\n*** Update File: internal/a.go\n@@\n-x\n+y\n*** Add File: docs/new.md\n+hi\n*** End Patch"
	batch := []Event{
		// Claude Code
		turnWithCalls(
			map[string]any{"name": "Edit", "input": map[string]any{"file_path": abs("internal/server/routes.go"), "old_string": "a", "new_string": "b"}},
			map[string]any{"name": "Write", "input": map[string]any{"file_path": "cmd/main.go", "content": "package main"}},
			map[string]any{"name": "MultiEdit", "input": map[string]any{"file_path": abs("internal/server/routes.go")}},
			map[string]any{"name": "NotebookEdit", "input": map[string]any{"notebook_path": abs("nb/a.ipynb")}},
			map[string]any{"name": "Read", "input": map[string]any{"file_path": abs("README.md")}},
			map[string]any{"name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		),
		// Codex: raw apply_patch string, and shell running apply_patch.
		turnWithCalls(
			map[string]any{"name": "apply_patch", "arguments": codexPatch},
			map[string]any{"name": "shell", "args": map[string]any{"command": []any{"apply_patch", "*** Begin Patch\n*** Update File: internal/b.go\n*** End Patch"}}},
			map[string]any{"name": "shell", "args": map[string]any{"command": []any{"bash", "-lc", "ls"}}},
		),
		// Cursor / OpenCode / text editor tool.
		turnWithCalls(
			map[string]any{"name": "edit_file", "args": map[string]any{"target_file": "web/app.tsx"}},
			map[string]any{"name": "edit", "input": map[string]any{"filePath": abs("pkg/x.go")}},
			map[string]any{"name": "str_replace_editor", "input": map[string]any{"command": "view", "path": abs("pkg/view.go")}},
			map[string]any{"name": "str_replace_editor", "input": map[string]any{"command": "create", "path": abs("pkg/created.go")}},
			map[string]any{"name": "functions.apply_patch", "input": "*** Begin Patch\n*** Delete File: old/gone.go\n*** End Patch"},
		),
		// Dropped: outside the workspace, ignored folders, env files.
		turnWithCalls(
			map[string]any{"name": "Write", "input": map[string]any{"file_path": filepath.Join(filepath.Dir(root), "elsewhere.go")}},
			map[string]any{"name": "Edit", "input": map[string]any{"file_path": abs("node_modules/x/index.js")}},
			map[string]any{"name": "Edit", "input": map[string]any{"file_path": abs(".env")}},
		),
	}
	got := TouchedPathsFromEvents(batch, root)
	want := []string{
		"internal/server/routes.go", "cmd/main.go", "nb/a.ipynb",
		"internal/a.go", "docs/new.md", "internal/b.go",
		"web/app.tsx", "pkg/x.go", "pkg/created.go", "old/gone.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("touched =\n%v\nwant\n%v", got, want)
	}
}

func TestFileContentHash(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "a.go")
	if err := os.WriteFile(text, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("package a\n"))
	if got, err := fileContentHash(text); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %q %v", got, err)
	}
	bin := filepath.Join(dir, "b.bin")
	_ = os.WriteFile(bin, []byte{1, 2, 0, 3}, 0o644)
	if _, err := fileContentHash(bin); err == nil {
		t.Fatal("binary hashed")
	}
	big := filepath.Join(dir, "big.txt")
	_ = os.WriteFile(big, []byte(strings.Repeat("a", maxHashBytes+1)), 0o644)
	if _, err := fileContentHash(big); err == nil {
		t.Fatal("large file hashed")
	}
	if _, err := fileContentHash(filepath.Join(dir, "missing.go")); err == nil {
		t.Fatal("missing file hashed")
	}
	if _, err := fileContentHash(dir); err == nil {
		t.Fatal("directory hashed")
	}
}

type codeLinksBody struct {
	SessionID       string `json:"session_id"`
	SessionNativeID string `json:"session_native_id"`
	Harness         string `json:"harness"`
	Links           []struct {
		Path     string `json:"path"`
		Relation string `json:"relation"`
		Source   string `json:"source"`
		Hash     string `json:"hash"`
	} `json:"links"`
}

type codeLinksCapture struct {
	mu     sync.Mutex
	status int
	calls  int
	path   string
	bodies []codeLinksBody
}

func (c *codeLinksCapture) handler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.path = r.URL.Path
	var body codeLinksBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.bodies = append(c.bodies, body)
	if c.status != 0 {
		w.WriteHeader(c.status)
		return
	}
	_, _ = w.Write([]byte(`{"linked":1}`))
}

func newTestCodeLinks(t *testing.T, root string, status int) (*CodeLinksReporter, *codeLinksCapture) {
	t.Helper()
	cap := &codeLinksCapture{status: status}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	t.Cleanup(srv.Close)
	hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)
	return NewCodeLinksReporter(root, hs.mirrorConnection), cap
}

func TestCodeLinksReporterPostsPerSessionWithHashes(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "internal"), 0o755)
	if err := os.WriteFile(filepath.Join(root, "internal", "a.go"), []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0, 0}, 0o644)
	rep, cap := newTestCodeLinks(t, root, 0)
	cloud := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	rep.NoteEvents(cloud, "native-1", "claude", []Event{turnWithCalls(
		map[string]any{"name": "Edit", "input": map[string]any{"file_path": filepath.Join(root, "internal", "a.go")}},
		map[string]any{"name": "Write", "input": map[string]any{"file_path": "logo.png"}},
	)})
	rep.Note("", "native-2", "codex", []string{"gone.go"})
	rep.Note("not-a-uuid", "native-2", "codex", []string{"gone.go"}) // duplicate

	if n := rep.Flush(context.Background()); n != 3 {
		t.Fatalf("sent = %d", n)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.calls != 2 || cap.path != "/v1/projects/"+testProjectUUID+"/code-links" {
		t.Fatalf("calls=%d path=%s", cap.calls, cap.path)
	}
	first := cap.bodies[0]
	if first.SessionID != cloud || first.SessionNativeID != "native-1" || first.Harness != "claude" || len(first.Links) != 2 {
		t.Fatalf("first body: %+v", first)
	}
	sum := sha256.Sum256([]byte("package internal\n"))
	for _, l := range first.Links {
		if l.Relation != "touched" || l.Source != "tool_call" {
			t.Fatalf("link: %+v", l)
		}
		switch l.Path {
		case "internal/a.go":
			if l.Hash != hex.EncodeToString(sum[:]) {
				t.Fatalf("text hash: %+v", l)
			}
		case "logo.png":
			if l.Hash != "" {
				t.Fatalf("binary must not be hashed: %+v", l)
			}
		default:
			t.Fatalf("unexpected path %q", l.Path)
		}
	}
	second := cap.bodies[1]
	if second.SessionID != "" || second.SessionNativeID != "native-2" || len(second.Links) != 1 || second.Links[0].Hash != "" {
		t.Fatalf("second body: %+v", second)
	}
	if rep.Pending() != 0 {
		t.Fatalf("pending = %d", rep.Pending())
	}
}

func TestCodeLinksReporterBackoffAndRetry(t *testing.T) {
	root := t.TempDir()
	rep, cap := newTestCodeLinks(t, root, http.StatusNotFound)
	rep.Note("", "n1", "claude", []string{"a.go"})
	if n := rep.Flush(context.Background()); n != 0 || rep.BackoffUntil().IsZero() {
		t.Fatalf("404 must back off: n=%d", n)
	}
	rep.Note("", "n1", "claude", []string{"b.go"})
	if rep.Pending() != 0 {
		t.Fatal("notes during backoff must be dropped")
	}

	// 409 (session not mirrored yet) is retried, then dropped.
	rep2, cap2 := newTestCodeLinks(t, root, http.StatusConflict)
	rep2.Note("", "n2", "codex", []string{"a.go"})
	for i := 0; i < codeLinksMaxAttempts+1; i++ {
		rep2.Flush(context.Background())
	}
	cap2.mu.Lock()
	calls := cap2.calls
	cap2.mu.Unlock()
	if calls != codeLinksMaxAttempts || rep2.Pending() != 0 {
		t.Fatalf("409 retries: calls=%d pending=%d", calls, rep2.Pending())
	}
	_ = cap
}

func TestCodeLinksReporterWaitsForConnection(t *testing.T) {
	rep := NewCodeLinksReporter(t.TempDir(), func() (string, string, string, *http.Client, error) {
		return "http://127.0.0.1:1", "tok", "", http.DefaultClient, nil
	})
	rep.Note("", "n", "claude", []string{"a.go"})
	for i := 0; i < 5; i++ {
		rep.Flush(context.Background())
	}
	if rep.Pending() != 1 {
		t.Fatalf("unresolved project must keep the batch: pending=%d", rep.Pending())
	}
}

func TestFilesChangedSendsHashesAndFallsBack(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var bodies []map[string]any
	reject := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, body)
		if _, has := body["hashes"]; has && reject {
			http.Error(w, `invalid JSON body: json: unknown field "hashes"`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"marked":0}`))
	}))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)

	// A server that knows hashes gets them.
	mu.Lock()
	reject = false
	mu.Unlock()
	rep := NewFilesChangedReporter(root, hs.mirrorConnection)
	rep.Note(filepath.Join(root, "a.go"))
	rep.Note(filepath.Join(root, "deleted.go"))
	if n := rep.Flush(context.Background()); n != 2 {
		t.Fatalf("sent = %d", n)
	}
	sum := sha256.Sum256([]byte("package a\n"))
	mu.Lock()
	hashes, _ := bodies[0]["hashes"].(map[string]any)
	mu.Unlock()
	if len(hashes) != 1 || hashes["a.go"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("hashes = %v", hashes)
	}

	// An older server rejects the field: the batch is resent without it.
	mu.Lock()
	reject = true
	bodies = nil
	mu.Unlock()
	old := NewFilesChangedReporter(root, hs.mirrorConnection)
	old.Note(filepath.Join(root, "a.go"))
	if n := old.Flush(context.Background()); n != 1 {
		t.Fatalf("fallback sent = %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	if _, has := bodies[1]["hashes"]; has {
		t.Fatal("retry must omit hashes")
	}
	if !old.noHashes || old.BackoffUntil().After(time.Now()) {
		t.Fatal("fallback must disable hashes without backing off")
	}
}
