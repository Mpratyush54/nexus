package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// linkStore adds a CodeLinkReader to the fake store.
type linkStore struct {
	*fakeStore
	links []CodeLink
}

func (l *linkStore) CodeLinksForFile(_ context.Context, _ string, path string, limit int) ([]CodeLink, error) {
	var out []CodeLink
	for _, c := range l.links {
		if filePathMatches(c.Path, path) {
			out = append(out, c)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func rowsOf(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		row, _ := r.(map[string]any)
		out = append(out, row)
	}
	return out
}

func TestCodeLinkToolSchemas(t *testing.T) {
	for _, tl := range ListTools() {
		if tl.Name != "related_to" && tl.Name != "file_history" {
			continue
		}
		req, ok := tl.InputSchema["required"].([]string)
		if !ok || len(req) != 1 || req[0] != "file" {
			t.Fatalf("%s required = %v", tl.Name, tl.InputSchema["required"])
		}
		props, _ := tl.InputSchema["properties"].(map[string]any)
		if _, ok := props["file"]; !ok {
			t.Fatalf("%s lacks file property", tl.Name)
		}
	}
}

func TestRelatedToAndFileHistoryWithLinks(t *testing.T) {
	_, fs := newTestServerWithT(t)
	ctx := context.Background()
	old := &MemoryItem{ProjectID: "proj_test", Key: "mcp/old", Content: "Only files_affected names tools.go here.", FilesAffected: []string{"internal/mcp/tools.go"}}
	_ = fs.CreateMemoryItem(ctx, old)
	linked := &MemoryItem{ID: "mem_linked", ProjectID: "proj_test", Key: "mcp/linked", Content: "Tools are dispatched by CallTool.", Tags: []string{"session:s1"}}
	_ = fs.CreateMemoryItem(ctx, linked)
	now := time.Now().UTC()
	ls := &linkStore{fakeStore: fs, links: []CodeLink{
		{Path: "internal/mcp/tools.go", Relation: "touched", Source: "tool_call", LinkedAt: now, CodeChanged: true, Memory: linked},
		{Path: "internal/mcp/tools.go", Relation: "decided_about", Source: "llm", LinkedAt: now.Add(-time.Minute), Memory: linked},
		{Path: "internal/mcp/tools.go", Relation: "touched", Source: "tool_call", LinkedAt: now.Add(-time.Hour),
			Session: &SessionRef{ID: "s1", Title: "Add tools", StartedAt: now.Add(-2 * time.Hour), LastActiveAt: now.Add(-time.Hour)}},
	}}
	s := NewServer(ls, Config{ProjectID: "proj_test", ProjectName: "central-memory"})

	m := resultMap(t, callTool(t, s, "related_to", map[string]any{"file": "mcp/tools.go"}))
	items := rowsOf(m, "items")
	if m["match"] != "links+files_affected" || len(items) != 3 {
		t.Fatalf("related_to: %v", m)
	}
	first := items[0]
	if first["key"] != "mcp/linked" || first["relation"] != "touched,decided_about" || first["stale"] != true || first["note"] != CodeChangedNote || first["source"] != "session:s1" {
		t.Fatalf("first: %v", first)
	}
	var sawSess, sawOld bool
	for _, it := range items {
		sawSess = sawSess || (it["kind"] == "session" && it["title"] == "Add tools")
		sawOld = sawOld || it["key"] == "mcp/old"
	}
	if !sawSess || !sawOld {
		t.Fatalf("items: %v", items)
	}

	h := resultMap(t, callTool(t, s, "file_history", map[string]any{"file": "tools.go"}))
	sessions := rowsOf(h, "sessions")
	if len(sessions) != 1 || sessions[0]["session_id"] != "s1" || sessions[0]["title"] != "Add tools" {
		t.Fatalf("file_history: %v", h)
	}

	// memory_search file= puts linked memories first, files_affected after.
	r := resultMap(t, callTool(t, s, "memory_search", map[string]any{"query": "nothing-matches-this", "file": "internal/mcp/tools.go"}))
	ctxXML, _ := r["context"].(string)
	if !strings.Contains(ctxXML, `key="mcp/linked"`) || !strings.Contains(ctxXML, `key="mcp/old"`) ||
		strings.Index(ctxXML, `key="mcp/linked"`) > strings.Index(ctxXML, `key="mcp/old"`) {
		t.Fatalf("memory_search order:\n%s", ctxXML)
	}

	if r := callTool(t, s, "related_to", map[string]any{}); r.Error == nil {
		t.Fatal("missing file accepted")
	}
}

func TestRelatedToFallsBackWithoutLinkStore(t *testing.T) {
	s, fs := newTestServerWithT(t)
	_ = fs.CreateMemoryItem(context.Background(), &MemoryItem{ProjectID: "proj_test", Key: "mcp/old", Content: "Only files_affected names tools.go here.", FilesAffected: []string{"internal/mcp/tools.go"}})
	m := resultMap(t, callTool(t, s, "related_to", map[string]any{"file": "tools.go"}))
	items := rowsOf(m, "items")
	if m["match"] != "files_affected" || len(items) != 1 || items[0]["key"] != "mcp/old" {
		t.Fatalf("fallback: %v", m)
	}
	if r := callTool(t, s, "file_history", map[string]any{"file": "tools.go"}); r.Error == nil {
		t.Fatal("file_history without links must error")
	}
}
