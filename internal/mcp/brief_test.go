package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// briefStore adds the cloud-only seams (brief + project_knowledge) to the
// fake store.
type briefStore struct {
	*fakeStore
	brief string
}

func (b *briefStore) ProjectBrief(_ context.Context, _ string) (string, error) {
	return b.brief, nil
}

func (b *briefStore) GetSessionSummary(context.Context, string) (string, error) { return "", nil }

func (b *briefStore) FetchSessionTurns(context.Context, string, string, int) ([]TurnPage, string, error) {
	return nil, "", nil
}

func (b *briefStore) ProjectKnowledge(ctx context.Context, projectID string, limit int) ([]*MemoryItem, error) {
	return b.SearchMemory(ctx, projectID, "", nil, limit)
}

const testBrief = "# Project brief: central-memory\n- Architecture: Go server with Postgres (session:abc123)"

func newBriefServer(t *testing.T) (*Server, *briefStore) {
	t.Helper()
	_, fs := newTestServerWithT(t)
	bs := &briefStore{fakeStore: fs, brief: testBrief}
	s := NewServer(bs, Config{ProjectID: "proj_test", ProjectName: "central-memory", Branch: "main"})
	return s, bs
}

func TestInitializeInstructionsIncludeBrief(t *testing.T) {
	s, bs := newBriefServer(t)
	m := resultMap(t, callRaw(t, s, "initialize", map[string]any{"protocolVersion": ProtocolVersion}))
	instr, _ := m["instructions"].(string)
	if !strings.Contains(instr, "Nexus shared memory is available") {
		t.Fatalf("instructions missing intro: %q", instr)
	}
	if !strings.Contains(instr, testBrief) {
		t.Fatalf("instructions missing brief: %q", instr)
	}

	// No brief yet: the intro alone, still present.
	bs.brief = ""
	m = resultMap(t, callRaw(t, s, "initialize", nil))
	instr, _ = m["instructions"].(string)
	if instr != InstructionsIntro {
		t.Fatalf("instructions without brief = %q", instr)
	}

	// Stores without a brief seam (desktop fakes) still get the intro.
	plain, _ := newTestServerWithT(t)
	m = resultMap(t, callRaw(t, plain, "initialize", nil))
	if m["instructions"] != InstructionsIntro {
		t.Fatalf("plain instructions = %v", m["instructions"])
	}
}

func TestProjectKnowledgePutsBriefFirst(t *testing.T) {
	s, bs := newBriefServer(t)
	ctx := context.Background()
	_ = bs.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "proj_test", Key: "db/choice", Content: "Postgres with pgvector stores all memories.", Level: "project", Tags: []string{"pinned"}})
	// The stored brief row must not appear twice.
	_ = bs.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "proj_test", Key: ProjectBriefKey, Content: testBrief, Level: "project"})

	m := resultMap(t, callTool(t, s, "project_knowledge", map[string]any{}))
	if m["brief"] != testBrief {
		t.Fatalf("brief field = %v", m["brief"])
	}
	items, _ := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want brief + 1: %v", len(items), items)
	}
	first, _ := items[0].(map[string]any)
	if first["key"] != ProjectBriefKey || first["content"] != testBrief {
		t.Fatalf("first item is not the brief: %v", first)
	}
	second, _ := items[1].(map[string]any)
	if second["key"] != "db/choice" {
		t.Fatalf("second item = %v", second)
	}
}

func TestMemorySearchTopFallbackLeadsWithBrief(t *testing.T) {
	s, bs := newBriefServer(t)
	ctx := context.Background()
	_ = bs.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "proj_test", Key: "auth/jwt", Content: "Auth uses short-lived JWTs signed by the server.", Level: "personal"})

	m := resultMap(t, callTool(t, s, "memory_search", map[string]any{"query": "zzzz unrelated qqqq"}))
	if m["match"] != "top" {
		t.Fatalf("match = %v, want top", m["match"])
	}
	ctxXML, _ := m["context"].(string)
	bi := strings.Index(ctxXML, "<project_brief>")
	ii := strings.Index(ctxXML, "<item ")
	if bi < 0 || ii < 0 || bi > ii {
		t.Fatalf("brief must come before items:\n%s", ctxXML)
	}
	if !strings.Contains(ctxXML, "Go server with Postgres") {
		t.Fatalf("brief content missing:\n%s", ctxXML)
	}

	// A relevant hit gets no brief.
	m = resultMap(t, callTool(t, s, "memory_search", map[string]any{"query": "JWTs"}))
	if strings.Contains(m["context"].(string), "<project_brief>") {
		t.Fatalf("brief should only lead the top fallback")
	}
}

func TestContextXMLItemProvenance(t *testing.T) {
	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	stale := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	items := []*MemoryItem{
		{
			Key: "api/routes", Content: "Routes live in routes.go.", Level: "project", Confidence: 0.9,
			Tags: []string{"source:test", "session:ses-42"}, CreatedAt: created, UpdatedAt: updated,
			FilesAffected: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"},
		},
		{Key: "conv/fmt", Content: "Always gofmt.", Level: "project", Confidence: 0.8, CreatedAt: created, StaleSince: &stale},
		{Key: "plain", Content: "No provenance.", Level: "project", Confidence: 0.5},
	}
	out := buildContextXML("p", "main", items)
	for _, want := range []string{
		`key="api/routes" confidence="0.90" scope="fact" date="2026-11-20" source="session:ses-42" files="a.go,b.go,c.go,d.go,e.go">`,
		`key="conv/fmt" confidence="0.80" scope="fact" date="2026-11-01" stale="true" note="files changed since; verify before relying">`,
		`<item key="plain" confidence="0.50" scope="fact">`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "f.go") {
		t.Fatalf("files must cap at 5:\n%s", out)
	}
}

func TestMemorySearchFileFilter(t *testing.T) {
	s, fs := newTestServerWithT(t)
	ctx := context.Background()
	_ = fs.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "proj_test", Key: "tools/schema", Content: "Tool schemas must keep required as an array.", FilesAffected: []string{`internal\mcp\tools.go`}, Confidence: 0.4})
	_ = fs.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "proj_test", Key: "other/tools", Content: "Other tools note mentioning schemas.", FilesAffected: []string{"cmd/tools.go"}, Confidence: 0.9})
	_ = fs.CreateMemoryItem(ctx, &MemoryItem{ProjectID: "proj_test", Key: "near/miss", Content: "Similar suffix but a different file.", FilesAffected: []string{"internal/mcp/oldtools.go"}})

	m := resultMap(t, callTool(t, s, "memory_search", map[string]any{"query": "schemas", "file": "D:/repo/internal/mcp/tools.go"}))
	ctxXML := m["context"].(string)
	first := strings.Index(ctxXML, `key="tools/schema"`)
	other := strings.Index(ctxXML, `key="other/tools"`)
	if first < 0 || other < 0 || first > other {
		t.Fatalf("file match must lead the results:\n%s", ctxXML)
	}
	if strings.Count(ctxXML, `key="tools/schema"`) != 1 {
		t.Fatalf("file match duplicated:\n%s", ctxXML)
	}
	if !strings.HasPrefix(m["match"].(string), "file") {
		t.Fatalf("match = %v", m["match"])
	}

	// file alone (no query) is accepted and returns only that file's memories first.
	m = resultMap(t, callTool(t, s, "memory_search", map[string]any{"file": "mcp/tools.go"}))
	ctxXML = m["context"].(string)
	if !strings.Contains(ctxXML, `key="tools/schema"`) || strings.Contains(ctxXML, `key="near/miss"`) {
		t.Fatalf("file-only search:\n%s", ctxXML)
	}

	// Schema exposes file and keeps required an array.
	for _, tool := range ListTools() {
		if tool.Name != "memory_search" {
			continue
		}
		props := tool.InputSchema["properties"].(map[string]any)
		if _, ok := props["file"]; !ok {
			t.Fatal("memory_search schema missing file")
		}
		if _, ok := tool.InputSchema["required"].([]string); !ok {
			t.Fatalf("required must be an array: %T", tool.InputSchema["required"])
		}
	}
}

func TestFilePathMatches(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"internal/mcp/tools.go", `D:\repo\internal\mcp\tools.go`, true},
		{"mcp/tools.go", "internal/mcp/tools.go", true},
		{"./internal/mcp/tools.go", "internal/mcp/tools.go", true},
		{"ools.go", "internal/mcp/tools.go", false},
		{"internal/mcp/oldtools.go", "tools.go", false},
		{"", "tools.go", false},
	}
	for _, c := range cases {
		if got := filePathMatches(c.a, c.b); got != c.want {
			t.Errorf("filePathMatches(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
