package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryWriteValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"valid", map[string]any{
			"key":     "testing/framework",
			"content": "The team uses pytest with fixture-based setup for all integration tests.",
		}, false},
		{"missing key", map[string]any{
			"content": "The team uses pytest with fixture-based setup for all integration tests.",
		}, true},
		{"too short", map[string]any{"key": "a/b", "content": "too short"}, true},
		{"too long", map[string]any{
			"key": "a/b", "content": strings.Repeat("x", 2001),
		}, true},
		{"bad scope", map[string]any{
			"key": "a/b", "content": strings.Repeat("y", 40), "scope": "vibe",
		}, true},
		{"bad level", map[string]any{
			"key": "a/b", "content": strings.Repeat("z", 40), "level": "galaxy",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServerWithT(t)
			r := callTool(t, s, "memory_write", tc.args)
			if tc.wantErr {
				if r.Error == nil || r.Error.Code != ErrInvalidParams {
					t.Fatalf("got %+v, want code %d", r.Error, ErrInvalidParams)
				}
				return
			}
			m := resultMap(t, r)
			if m["status"] != "PROPOSED" {
				t.Errorf("status = %v, want PROPOSED", m["status"])
			}
			if _, ok := m["id"].(string); !ok {
				t.Errorf("missing id: %v", m)
			}
		})
	}
}

func TestMemoryWriteBoundaryLengths(t *testing.T) {
	s, _ := newTestServerWithT(t)
	for _, n := range []int{19, 20, 2000, 2001} {
		r := callTool(t, s, "memory_write", map[string]any{
			"key": "k/boundary", "content": strings.Repeat("c", n),
		})
		valid := n >= 20 && n <= 2000
		if valid && r.Error != nil {
			t.Errorf("n=%d: unexpected error %v", n, r.Error)
		}
		if !valid && (r.Error == nil || r.Error.Code != ErrInvalidParams) {
			t.Errorf("n=%d: got %+v, want code %d", n, r.Error, ErrInvalidParams)
		}
	}
}

func TestEpisodeReportValidation(t *testing.T) {
	s, _ := newTestServerWithT(t)
	bad := []map[string]any{
		{"episode_type": "bug_fix"},
		{"title": "Outage", "episode_type": "mystery"},
		{"title": "   ", "episode_type": "bug_fix"},
	}
	for i, args := range bad {
		r := callTool(t, s, "episode_report", args)
		if r.Error == nil || r.Error.Code != ErrInvalidParams {
			t.Errorf("case %d: got %+v, want code %d", i, r.Error, ErrInvalidParams)
		}
	}
}

func TestBuildContextXMLLevelsAndEscape(t *testing.T) {
	items := []*MemoryItem{
		{Key: "a/x", Content: "Use JWT & <never> API keys", Level: "organization", Scope: "constraint", Confidence: 1},
		{Key: "b/y", Content: "pytest all the way", Level: "project", Scope: "decision", Confidence: 0.95},
		{Key: "c/z", Content: "plain fact", Level: "", Scope: "", Confidence: 0.5}, // defaults
	}
	xml := buildContextXML("demo", "main", items)
	for _, want := range []string{
		"<project_memory", "</project_memory>",
		"<organization>", "<project>",
		"Use JWT &amp; &lt;never&gt; API keys", // escaped
		`confidence="0.95"`, `scope="decision"`,
		`scope="fact"`, // default scope applied
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("XML missing %q:\n%s", want, xml)
		}
	}
	for _, no := range []string{"<never>", "& API"} {
		if strings.Contains(xml, no) {
			t.Errorf("XML contains unescaped %q:\n%s", no, xml)
		}
	}
	// Level ordering: organization before project.
	if strings.Index(xml, "<organization>") > strings.Index(xml, "<project>") {
		t.Error("organization section must precede project section")
	}
}

func TestTokenBudgetAccounting(t *testing.T) {
	s, ms := newTestServerWithT(t)
	ctx := context.Background()
	if err := ms.CreateMemoryItem(ctx, &MemoryItem{
		ProjectID: "proj_test", Key: "k/v",
		Content: "A sufficiently long memory content string for token math.",
		Level:   "project", Scope: "fact",
	}); err != nil {
		t.Fatal(err)
	}
	r := callTool(t, s, "memory_search", map[string]any{"query": "token math"})
	m := resultMap(t, r)
	ctxXML, _ := m["context"].(string)
	wantTokens := float64(len([]rune(ctxXML)) / 4)
	if got := num(t, m, "token_count"); got != wantTokens {
		t.Errorf("token_count = %v, want %v", got, wantTokens)
	}
	wantRemaining := float64(DefaultTokenBudget - len([]rune(ctxXML)))
	if got := num(t, m, "budget_remaining"); got != wantRemaining {
		t.Errorf("budget_remaining = %v, want %v", got, wantRemaining)
	}
}

func TestResolvePathSandbox(t *testing.T) {
	s, _ := newTestServerWithT(t)
	root := s.cfg.WorkspacePath

	// Round-trip inside the root works.
	r := callTool(t, s, "file_write", map[string]any{"path": "sub/note.md", "content": "hello sandbox"})
	resultMap(t, r)
	r = callTool(t, s, "file_read", map[string]any{"path": "sub/note.md"})
	m := resultMap(t, r)
	if m["content"] != "hello sandbox" {
		t.Errorf("content = %v", m["content"])
	}

	// Traversal escapes and absolute paths outside the root are rejected
	// with InvalidParams. The outside path is computed dynamically so the
	// test holds on every OS (Windows drive/volume semantics differ).
	outsideAbs := filepath.Join(filepath.Dir(root), "mcp-outside-evil.md")
	for _, evil := range []string{"../evil.md", ".." + string(filepath.Separator) + "evil.md", outsideAbs} {
		r = callTool(t, s, "file_read", map[string]any{"path": evil})
		if r.Error == nil || r.Error.Code != ErrInvalidParams {
			t.Errorf("path %q: got %+v, want code %d", evil, r.Error, ErrInvalidParams)
		}
		r = callTool(t, s, "file_write", map[string]any{"path": evil, "content": "x"})
		if r.Error == nil || r.Error.Code != ErrInvalidParams {
			t.Errorf("write %q: got %+v, want code %d", evil, r.Error, ErrInvalidParams)
		}
	}
	_ = root
}

func TestFileReadMissing(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callTool(t, s, "file_read", map[string]any{"path": "does/not-exist.md"})
	if r.Error == nil || r.Error.Code != ErrInternal {
		t.Fatalf("got %+v, want internal error", r.Error)
	}
}

func TestEpisodeSearchFilters(t *testing.T) {
	s, ms := newTestServerWithT(t)
	ctx := context.Background()
	for _, ep := range []*Episode{
		{ProjectID: "proj_test", Title: "WS timeout", EpisodeType: "bug_fix",
			ErrorPatterns: []string{"ConnectionTimeout"}, FilesInvolved: []string{"internal/server/ws.go"},
			Status: "RESOLVED"},
		{ProjectID: "proj_test", Title: "Slow query", EpisodeType: "investigation",
			FilesInvolved: []string{"internal/store/db.go"}, Status: "OPEN"},
	} {
		if err := ms.CreateEpisode(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	r := callTool(t, s, "episode_search", map[string]any{"error_pattern": "connectiontimeout"})
	if got := num(t, resultMap(t, r), "count"); got != 1 {
		t.Errorf("error_pattern count = %v, want 1", got)
	}
	r = callTool(t, s, "episode_search", map[string]any{"file": "store/db.go"})
	if got := num(t, resultMap(t, r), "count"); got != 1 {
		t.Errorf("file count = %v, want 1", got)
	}
	r = callTool(t, s, "episode_search", map[string]any{"status": "OPEN"})
	if got := num(t, resultMap(t, r), "count"); got != 1 {
		t.Errorf("status count = %v, want 1", got)
	}
}
