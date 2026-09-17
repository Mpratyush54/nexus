package mcp

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"central-memory/internal/daemon"
	"central-memory/internal/store"
)

func newTestServer(root string) (*Server, *InMemoryMemoryStore, *InMemoryEpisodeStore) {
	mem := NewInMemoryMemoryStore()
	ep := NewInMemoryEpisodeStore()
	s := New(
		Config{ProjectID: "p1", ProjectName: "nexus", Branch: "main"},
		mem, mem, ep,
		StaticWorkspaceProvider{Info: WorkspaceInfo{Project: "nexus", Branch: "main", Commit: "abc123", Path: root}},
		DaemonFileProxy{Root: root},
	)
	return s, mem, ep
}

func mustWrite(t *testing.T, ctx context.Context, mem *InMemoryMemoryStore, key, content string) string {
	t.Helper()
	w, err := mem.WriteMemory(ctx, MemoryWriteInput{Key: key, Content: content, Level: "project", Scope: "fact"})
	if err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if !mem.Confirm(w.ID) {
		t.Fatalf("Confirm(%s) = false", w.ID)
	}
	return w.ID
}

// memory_search returns Context Builder XML + piggyback reflection hint +
// token_count/budget_remaining (plan §1.4).
func TestMemorySearchReturnsXMLReflectionHintAndBudget(t *testing.T) {
	ctx := context.Background()
	s, mem, _ := newTestServer(t.TempDir())
	mustWrite(t, ctx, mem, "testing/framework", "The team uses pytest with fixture-based setup for all services.")
	mustWrite(t, ctx, mem, "auth/policy", "All APIs must use JWT authentication and reject expired tokens.")

	res, cerr := s.Call(ctx, "memory_search", map[string]any{"query": "how do we test services"})
	if cerr != nil {
		t.Fatalf("memory_search: %v", cerr)
	}
	m := res.(map[string]any)
	xmlOut, _ := m["context"].(string)
	if !strings.Contains(xmlOut, "<project_memory") || !strings.Contains(xmlOut, "pytest") {
		t.Errorf("context missing project_memory XML or pytest item:\n%s", xmlOut)
	}
	if m["reflection_hint"] != ReflectionHint {
		t.Errorf("reflection_hint = %q, want piggyback hint", m["reflection_hint"])
	}
	tc, _ := m["token_count"].(int)
	br, _ := m["budget_remaining"].(int)
	if tc <= 0 {
		t.Errorf("token_count = %d, want > 0", tc)
	}
	if tc+br != 4000 { // default budget: used + remaining == budget
		t.Errorf("token_count(%d) + budget_remaining(%d) != 4000", tc, br)
	}
	if m["items_included"].(int) < 2 {
		t.Errorf("items_included = %v, want >= 2", m["items_included"])
	}
}

func TestMemorySearchRequiresQuery(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	if _, cerr := s.Call(context.Background(), "memory_search", map[string]any{}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("empty query: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
	if _, cerr := s.Call(context.Background(), "memory_search", map[string]any{"query": "x", "level": "bogus"}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("bad level: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
}

// memory_write validates 20–2000 chars and stores as PROPOSED (plan §1.1).
func TestMemoryWriteValidation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestServer(t.TempDir())
	cases := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"too short", map[string]any{"key": "a/b", "content": "too short"}, true},
		{"empty key", map[string]any{"key": " ", "content": strings.Repeat("x", 30)}, true},
		{"bad level", map[string]any{"key": "a/b", "content": strings.Repeat("x", 30), "level": "galaxy"}, true},
		{"bad scope", map[string]any{"key": "a/b", "content": strings.Repeat("x", 30), "scope": "vibe"}, true},
		{"ok minimal", map[string]any{"key": "a/b", "content": strings.Repeat("x", 30)}, false},
		{"ok full", map[string]any{
			"key": "testing/framework", "content": "The team uses pytest with fixtures everywhere.",
			"scope": "decision", "level": "project",
			"tags": []any{"testing"}, "context_snippet": "Decided by Alice during auth refactor",
		}, false},
	}
	for _, c := range cases {
		res, cerr := s.Call(ctx, "memory_write", c.args)
		if c.wantErr {
			if cerr == nil || cerr.Code != CodeInvalidParams {
				t.Errorf("%s: cerr = %v, want code %d", c.name, cerr, CodeInvalidParams)
			}
			continue
		}
		if cerr != nil {
			t.Errorf("%s: unexpected error %v", c.name, cerr)
			continue
		}
		m := res.(map[string]any)
		if m["status"] != store.StatusProposed {
			t.Errorf("%s: status = %v, want PROPOSED", c.name, m["status"])
		}
		if m["id"] == "" || m["id"] == nil {
			t.Errorf("%s: empty id", c.name)
		}
	}
}

func TestMemoryWriteCharBoundaries(t *testing.T) {
	for _, n := range []int{19, 20, 2000, 2001} {
		in := MemoryWriteInput{Key: "k", Content: strings.Repeat("é", n), Level: "project", Scope: "fact"}
		err := ValidateMemoryWrite(in)
		if (n == 20 || n == 2000) && err != nil {
			t.Errorf("n=%d: want valid, got %v", n, err)
		}
		if (n == 19 || n == 2001) && err == nil {
			t.Errorf("n=%d: want invalid, got nil", n)
		}
	}
}

// memory_reflect is voluntary: empty calls ack, items land as PROPOSED.
func TestMemoryReflectVoluntary(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestServer(t.TempDir())

	res, cerr := s.Call(ctx, "memory_reflect", map[string]any{})
	if cerr != nil {
		t.Fatalf("empty reflect: %v", cerr)
	}
	if res.(map[string]any)["accepted"] != 0 {
		t.Errorf("empty reflect: accepted = %v, want 0", res)
	}

	res, cerr = s.Call(ctx, "memory_reflect", map[string]any{
		"summary": "Finished auth refactor.",
		"items": []any{map[string]any{
			"key": "auth/policy", "content": "All APIs must use JWT authentication tokens.",
		}},
	})
	if cerr != nil {
		t.Fatalf("reflect with items: %v", cerr)
	}
	m := res.(map[string]any)
	if m["accepted"] != 1 || len(m["ids"].([]string)) != 1 {
		t.Errorf("reflect: result = %v, want accepted=1 with 1 id", m)
	}

	bad := map[string]any{"items": []any{map[string]any{"key": "x", "content": "short"}}}
	if _, cerr := s.Call(ctx, "memory_reflect", bad); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("bad reflect item: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
}

func TestEpisodeReportAndSearch(t *testing.T) {
	ctx := context.Background()
	s, _, ep := newTestServer(t.TempDir())

	res, cerr := s.Call(ctx, "episode_report", map[string]any{
		"title": "Auth timeout on WebSocket upgrade", "episode_type": "bug_fix",
		"trigger": "ConnectionTimeout in ws.go:142 during load test",
		"tags":    []any{"websocket", "timeout"},
	})
	if cerr != nil {
		t.Fatalf("episode_report: %v", cerr)
	}
	m := res.(map[string]any)
	if m["status"] != "OPEN" || m["episode_type"] != "bug_fix" {
		t.Errorf("episode_report result = %v, want OPEN bug_fix", m)
	}

	// By error pattern (substring of the trigger).
	res, cerr = s.Call(ctx, "episode_search", map[string]any{"error_pattern": "ConnectionTimeout"})
	if cerr != nil {
		t.Fatalf("episode_search: %v", cerr)
	}
	if res.(map[string]any)["count"] != 1 {
		t.Errorf("error_pattern search count = %v, want 1", res)
	}

	// By file: seed involvement directly (report input has no files field;
	// the processor fills it in — issue #10).
	ep.mu.Lock()
	ep.episodes[0].FilesInvolved = []string{"internal/server/ws.go"}
	ep.episodes[0].ErrorPatterns = []string{"ConnectionTimeout"}
	ep.mu.Unlock()
	res, _ = s.Call(ctx, "episode_search", map[string]any{"file": "internal/server/ws.go"})
	if res.(map[string]any)["count"] != 1 {
		t.Errorf("file search count = %v, want 1", res)
	}
	res, _ = s.Call(ctx, "episode_search", map[string]any{"query": "websocket load test timeout"})
	if res.(map[string]any)["count"] != 1 {
		t.Errorf("semantic search count = %v, want 1", res)
	}
	res, _ = s.Call(ctx, "episode_search", map[string]any{"status": "RESOLVED"})
	if res.(map[string]any)["count"] != 0 {
		t.Errorf("status-filtered count = %v, want 0", res)
	}

	if _, cerr := s.Call(ctx, "episode_report", map[string]any{"title": " ", "episode_type": "bug_fix"}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("empty title: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
	if _, cerr := s.Call(ctx, "episode_report", map[string]any{"title": "x", "episode_type": "nope"}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("bad type: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
	if err := ValidateEpisodeReport(EpisodeReportInput{Title: "t", Type: "incident"}); err != nil {
		t.Errorf("valid report rejected: %v", err)
	}
}

func TestWorkspaceInfo(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	res, cerr := s.Call(context.Background(), "workspace_info", map[string]any{})
	if cerr != nil {
		t.Fatalf("workspace_info: %v", cerr)
	}
	m := res.(map[string]any)
	for _, k := range []string{"project", "branch", "commit", "is_dirty", "path"} {
		if _, ok := m[k]; !ok {
			t.Errorf("workspace_info missing key %q: %v", k, m)
		}
	}
	if m["project"] != "nexus" {
		t.Errorf("project = %v, want nexus", m["project"])
	}
}

// file_read/file_write proxy to the daemon sandbox: roundtrip works,
// traversal escapes are rejected, missing files error (plan §1.3/§1.4).
func TestFileSandboxProxy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, _, _ := newTestServer(root)

	content := "hello world from mcp sandbox proxy test file"
	res, cerr := s.Call(ctx, "file_write", map[string]any{"path": "notes/hello.txt", "content": content})
	if cerr != nil {
		t.Fatalf("file_write: %v", cerr)
	}
	if res.(map[string]any)["status"] != "ok" {
		t.Errorf("file_write result = %v", res)
	}

	res, cerr = s.Call(ctx, "file_read", map[string]any{"path": "notes/hello.txt"})
	if cerr != nil {
		t.Fatalf("file_read: %v", cerr)
	}
	m := res.(map[string]any)
	if m["content"] != content || m["size"] != len(content) {
		t.Errorf("file_read result = %v, want roundtripped content", m)
	}

	traversal := "../escape.txt"
	if _, cerr := s.Call(ctx, "file_read", map[string]any{"path": traversal}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("traversal read: cerr = %v, want code %d", cerr, CodeInvalidParams)
	} else if !strings.Contains(cerr.Message, "escapes workspace") {
		t.Errorf("traversal message = %q, want sandbox wording", cerr.Message)
	}
	if _, cerr := s.Call(ctx, "file_write", map[string]any{"path": traversal, "content": "x"}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("traversal write: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
	if _, cerr := s.Call(ctx, "file_read", map[string]any{"path": "missing.txt"}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("missing file: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}
	if _, cerr := s.Call(ctx, "file_read", map[string]any{}); cerr == nil || cerr.Code != CodeInvalidParams {
		t.Errorf("empty path: cerr = %v, want code %d", cerr, CodeInvalidParams)
	}

	// The proxy must surface the daemon's own sentinels (reuse proof).
	proxy := DaemonFileProxy{Root: root}
	if _, err := proxy.ReadFile(ctx, traversal); !errors.Is(err, daemon.ErrTraversal) {
		t.Errorf("proxy traversal err = %v, want errors.Is daemon.ErrTraversal", err)
	}
}

func TestUnknownTool(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	if _, cerr := s.Call(context.Background(), "nope", nil); cerr == nil || cerr.Code != CodeMethodNotFound {
		t.Errorf("unknown tool: cerr = %v, want code %d", cerr, CodeMethodNotFound)
	}
}

func TestToolsCatalogHasEight(t *testing.T) {
	s, _, _ := newTestServer(t.TempDir())
	tools := s.Tools()
	if len(tools) != 8 {
		t.Fatalf("Tools() = %d, want 8", len(tools))
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		seen[tl.Name] = true
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("tool %q missing description/schema", tl.Name)
		}
	}
	for _, want := range []string{"memory_search", "memory_write", "memory_reflect", "episode_search", "episode_report", "workspace_info", "file_read", "file_write"} {
		if !seen[want] {
			t.Errorf("catalog missing tool %q", want)
		}
	}
}

func TestHashEmbedDeterministicNormalized(t *testing.T) {
	a, b := HashEmbed("hello world"), HashEmbed("hello world")
	if len(a) != EmbedDim {
		t.Fatalf("dim = %d, want %d", len(a), EmbedDim)
	}
	var norm float64
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at %d", i)
		}
		norm += float64(a[i]) * float64(a[i])
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-5 {
		t.Errorf("norm = %v, want 1", math.Sqrt(norm))
	}
	if got := HashEmbed("completely different tokens xyzzy"); stringForCompare(a) == stringForCompare(got) {
		t.Error("distinct queries must embed distinctly")
	}
	zero := HashEmbed("")
	for _, v := range zero {
		if v != 0 {
			t.Fatal("empty query must embed to the zero vector")
		}
	}
}

func stringForCompare(v []float32) string {
	var sb strings.Builder
	for _, f := range v {
		if f != 0 {
			sb.WriteString("1")
		} else {
			sb.WriteString("0")
		}
	}
	return sb.String()
}

func TestValidateMemoryWriteLevelsScopes(t *testing.T) {
	base := MemoryWriteInput{Key: "k", Content: strings.Repeat("c", 40), Level: "project", Scope: "fact"}
	if err := ValidateMemoryWrite(base); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}
	for _, lvl := range []string{"organization", "project", "personal", "session"} {
		in := base
		in.Level = lvl
		if err := ValidateMemoryWrite(in); err != nil {
			t.Errorf("level %q rejected: %v", lvl, err)
		}
	}
	for _, sc := range []string{"fact", "preference", "decision", "constraint", "pattern", "episode_summary"} {
		in := base
		in.Scope = sc
		if err := ValidateMemoryWrite(in); err != nil {
			t.Errorf("scope %q rejected: %v", sc, err)
		}
	}
}
