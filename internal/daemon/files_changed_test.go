package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"central-memory/internal/agentfiles"
)

const testProjectUUID = "11111111-2222-3333-4444-555555555555"

type filesChangedCapture struct {
	mu     sync.Mutex
	status int
	calls  int
	bodies []struct {
		Paths []string `json:"paths"`
		At    string   `json:"at"`
	}
	auth string
	path string
}

func (c *filesChangedCapture) handler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.auth = r.Header.Get("Authorization")
	c.path = r.URL.Path
	var body struct {
		Paths []string `json:"paths"`
		At    string   `json:"at"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.bodies = append(c.bodies, body)
	if c.status != 0 {
		w.WriteHeader(c.status)
		return
	}
	_, _ = w.Write([]byte(`{"marked":0}`))
}

func newTestReporter(t *testing.T, root string, status int) (*FilesChangedReporter, *filesChangedCapture) {
	t.Helper()
	cap := &filesChangedCapture{status: status}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	t.Cleanup(srv.Close)
	hs := NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID)
	return NewFilesChangedReporter(root, hs.mirrorConnection), cap
}

func TestChangedRelPathFiltering(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	keep := map[string]string{
		filepath.Join(root, "src", "main.go"):                 "src/main.go",
		filepath.Join(root, "README.md"):                      "README.md",
		filepath.Join(root, ".github", "workflows", "ci.yml"): ".github/workflows/ci.yml",
		"internal/x.go": "internal/x.go",
	}
	for in, want := range keep {
		got, ok := changedRelPath(root, in)
		if !ok || got != want {
			t.Fatalf("changedRelPath(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	drop := []string{
		filepath.Join(root, ".git", "index"),
		filepath.Join(root, "node_modules", "a", "index.js"),
		filepath.Join(root, "web", "dist", "app.js"),
		filepath.Join(root, "build", "out.o"),
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".codex", "x.jsonl"),
		filepath.Join(root, ".central-memory", "daemon.workspace"),
		filepath.Join(root, ".env.local"),
		filepath.Join(root, "server.key"),
		filepath.Join(root, "debug.log"),
		filepath.Join(root, "main.go~"),
		filepath.Join(root, ".main.go.swp"),
		filepath.Join(root, ".CLAUDE.md.nexus-123.tmp"),
		filepath.Join(root, "raw", "t.jsonl"),
		filepath.Join(filepath.Dir(root), "other", "a.go"),
		root,
	}
	for _, in := range drop {
		if got, ok := changedRelPath(root, in); ok {
			t.Fatalf("changedRelPath(%q) kept as %q", in, got)
		}
	}
}

func TestFilesChangedBatchesAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	rep, cap := newTestReporter(t, root, 0)
	rep.MaxBatch = 3
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	clock := base
	rep.now = func() time.Time { return clock }

	rep.Note(filepath.Join(root, "a.go"))
	clock = clock.Add(time.Second)
	rep.Note(filepath.Join(root, "pkg", "b.go"))
	rep.Note(filepath.Join(root, "pkg", "b.go")) // dedup
	rep.Note(filepath.Join(root, "node_modules", "x.js"))
	rep.Note(filepath.Join(root, "c.go"))
	rep.Note(filepath.Join(root, "d.go"))
	if rep.Pending() != 4 {
		t.Fatalf("pending=%d want 4", rep.Pending())
	}
	select {
	case <-rep.kick:
	default:
		t.Fatal("full batch did not signal a flush")
	}
	if n := rep.Flush(context.Background()); n != 4 {
		t.Fatalf("flushed %d want 4", n)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.calls != 2 {
		t.Fatalf("calls=%d want 2 (batch of 3 then 1)", cap.calls)
	}
	if got := strings.Join(cap.bodies[0].Paths, ","); got != "a.go,pkg/b.go,c.go" {
		t.Fatalf("first batch %q", got)
	}
	if got := strings.Join(cap.bodies[1].Paths, ","); got != "d.go" {
		t.Fatalf("second batch %q", got)
	}
	if cap.bodies[0].At != base.Format(time.RFC3339) {
		t.Fatalf("at=%q want earliest change %s", cap.bodies[0].At, base.Format(time.RFC3339))
	}
	if cap.auth != "Bearer tok" || cap.path != "/v1/projects/"+testProjectUUID+"/files-changed" {
		t.Fatalf("auth=%q path=%q", cap.auth, cap.path)
	}
}

func TestFilesChangedBackoffOn404(t *testing.T) {
	root := t.TempDir()
	rep, cap := newTestReporter(t, root, http.StatusNotFound)
	clock := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	rep.now = func() time.Time { return clock }

	rep.Note(filepath.Join(root, "a.go"))
	rep.Flush(context.Background())
	if cap.calls != 1 {
		t.Fatalf("calls=%d", cap.calls)
	}
	if !rep.BackoffUntil().Equal(clock.Add(time.Hour)) {
		t.Fatalf("backoff until %v", rep.BackoffUntil())
	}
	// During back-off: notes are dropped, nothing is posted.
	clock = clock.Add(30 * time.Minute)
	rep.Note(filepath.Join(root, "b.go"))
	rep.Flush(context.Background())
	if cap.calls != 1 || rep.Pending() != 0 {
		t.Fatalf("posted during back-off: calls=%d pending=%d", cap.calls, rep.Pending())
	}
	// After an hour it tries again.
	clock = clock.Add(31 * time.Minute)
	rep.Note(filepath.Join(root, "c.go"))
	rep.Flush(context.Background())
	if cap.calls != 2 {
		t.Fatalf("no retry after back-off: calls=%d", cap.calls)
	}
}

func TestFilesChangedRequeuesOnServerError(t *testing.T) {
	root := t.TempDir()
	rep, cap := newTestReporter(t, root, http.StatusBadGateway)
	rep.Note(filepath.Join(root, "a.go"))
	rep.Flush(context.Background())
	if rep.Pending() != 1 || !rep.BackoffUntil().IsZero() {
		t.Fatalf("pending=%d backoff=%v", rep.Pending(), rep.BackoffUntil())
	}
	cap.mu.Lock()
	cap.status = 0
	cap.mu.Unlock()
	if n := rep.Flush(context.Background()); n != 1 {
		t.Fatalf("retry flushed %d", n)
	}
}

func TestFilesChangedWaitsForProjectAndSuppress(t *testing.T) {
	root := t.TempDir()
	cap := &filesChangedCapture{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", "proj-basename")
	rep := NewFilesChangedReporter(root, hs.mirrorConnection)
	rep.Note(filepath.Join(root, "a.go"))
	rep.Suppress("CLAUDE.md", time.Minute)
	rep.Note(filepath.Join(root, "CLAUDE.md"))
	rep.Flush(context.Background())
	if cap.calls != 0 || rep.Pending() != 1 {
		t.Fatalf("posted before project resolved: calls=%d pending=%d", cap.calls, rep.Pending())
	}
	hs.SetProjectID(testProjectUUID)
	rep.Flush(context.Background())
	if cap.calls != 1 || strings.Join(cap.bodies[0].Paths, ",") != "a.go" {
		t.Fatalf("calls=%d bodies=%v", cap.calls, cap.bodies)
	}
}

func TestProjectWatcherReportsSourceChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, _ := newTestReporter(t, root, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runProjectWatcher(ctx, root, rep, nil)
	}()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = os.WriteFile(filepath.Join(root, "node_modules", "x", "i.js"), []byte("x"), 0o644)
		_ = os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644)
		time.Sleep(50 * time.Millisecond)
		rep.mu.Lock()
		_, got := rep.pending["src/main.go"]
		n := len(rep.pending)
		rep.mu.Unlock()
		if got {
			if n != 1 {
				t.Fatalf("unexpected pending paths: %v", rep.pending)
			}
			return
		}
	}
	t.Fatal("watcher did not report src/main.go")
}

// --- agent instruction files ---

func newAgentFilesServer(t *testing.T, briefStatus int, brief string, memories []map[string]any) (*httptest.Server, *int) {
	t.Helper()
	searches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/projects/"+testProjectUUID+"/brief":
			if briefStatus != 0 {
				w.WriteHeader(briefStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"content": brief, "updated_at": "2026-11-01T00:00:00Z", "sources": []string{}})
		case r.URL.Path == "/memory/search":
			searches++
			if r.URL.Query().Get("level") != "project" || r.URL.Query().Get("project_id") != testProjectUUID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": memories, "count": len(memories)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &searches
}

func TestAgentFilesSyncerWritesManagedSection(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "CLAUDE.md")
	_ = os.WriteFile(claude, []byte("# Mine\r\nkeep\r\n"), 0o644)
	mems := []map[string]any{
		{"id": "1", "key": "db", "content": "Use Postgres", "level": "project", "scope": "decision", "status": "CONFIRMED", "updated_at": "2026-10-01T00:00:00Z"},
		{"id": "2", "key": "draft", "content": "Maybe Redis", "level": "project", "scope": "decision", "status": "PROPOSED"},
		{"id": "3", "key": "ep", "content": "Episode", "level": "project", "scope": "episode_summary", "status": "CONFIRMED"},
		{"id": "4", "key": "style", "content": "gofmt everything", "level": "project", "scope": "fact", "status": "CONFIRMED", "use_count": 9},
	}
	srv, _ := newAgentFilesServer(t, 0, "Nexus is a memory layer.", mems)
	settings := agentfiles.Settings{Enabled: true, Files: agentfiles.AllFiles}
	var suppressed []string
	a := &AgentFilesSyncer{
		Root:         root,
		Store:        NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID),
		LoadSettings: func(string) (agentfiles.Settings, error) { return settings, nil },
		OnWrite:      func(rel string) { suppressed = append(suppressed, rel) },
	}
	rep, err := a.Sync(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.BriefFound || rep.Memories != 2 || len(rep.Files) != 3 {
		t.Fatalf("report %+v", rep)
	}
	got, _ := os.ReadFile(claude)
	s := string(got)
	if !strings.HasPrefix(s, "# Mine\r\nkeep\r\n\r\n"+agentfiles.StartMarker+"\r\n") {
		t.Fatalf("prefix/CRLF: %q", s)
	}
	if !strings.Contains(s, "Nexus is a memory layer.") || !strings.Contains(s, "**db**: Use Postgres") ||
		strings.Contains(s, "Maybe Redis") || strings.Contains(s, "Episode") {
		t.Fatalf("content: %s", s)
	}
	if strings.Index(s, "Use Postgres") > strings.Index(s, "gofmt everything") {
		t.Fatalf("decision should rank before fact")
	}
	for _, name := range []string{"AGENTS.md", "GEMINI.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s created without create_missing", name)
		}
	}
	if len(suppressed) != 3 {
		t.Fatalf("suppressed %v", suppressed)
	}
	// Second run: same fingerprint, nothing touched.
	rep, _ = a.Sync(context.Background(), false)
	if rep.Skipped != "unchanged" {
		t.Fatalf("second sync %+v", rep)
	}
	// Forced: runs but leaves the file byte-identical.
	rep, _ = a.Sync(context.Background(), true)
	if rep.Files[0].Action != agentfiles.ActionUnchanged {
		t.Fatalf("forced sync %+v", rep.Files[0])
	}
}

func TestAgentFilesSyncerDisabledAndBrief404(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("x\n"), 0o644)
	srv, searches := newAgentFilesServer(t, http.StatusNotFound, "", []map[string]any{
		{"id": "1", "key": "k", "content": "Decided", "level": "project", "scope": "decision", "status": "CONFIRMED"},
	})
	settings := agentfiles.DefaultSettings()
	a := &AgentFilesSyncer{
		Root:         root,
		Store:        NewHTTPMemoryStore(srv.URL, "tok", testProjectUUID),
		LoadSettings: func(string) (agentfiles.Settings, error) { return settings, nil },
	}
	rep, err := a.Sync(context.Background(), true)
	if err != nil || rep.Skipped != "disabled" || *searches != 0 {
		t.Fatalf("disabled: %+v %v searches=%d", rep, err, *searches)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(got) != "x\n" {
		t.Fatalf("disabled sync wrote %q", got)
	}
	settings.Enabled = true
	settings.CreateMissing = true
	rep, err = a.Sync(context.Background(), false)
	if err != nil || rep.BriefFound || rep.Memories != 1 {
		t.Fatalf("brief 404: %+v %v", rep, err)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "GEMINI.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !strings.Contains(string(b), "Decided") || strings.Contains(string(b), "### Brief") {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
}
