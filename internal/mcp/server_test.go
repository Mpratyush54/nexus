package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// errNotFound stands in for the store package's not-found error.
var errNotFound = errors.New("not found")

// fakeStore is an in-memory Store for MCP unit tests. It mirrors the
// substring-match semantics of the real in-memory store without importing
// internal/store, keeping this package stdlib-only while the store
// package's pgx/pgvector wiring lands. Production wiring is a thin
// adapter over the same method shapes.
type fakeStore struct {
	mu        sync.RWMutex
	seq       int64
	memories  []*MemoryItem
	episodes  []*Episode
	workspace *Workspace
	project   *Project
}

func newFakeStore() *fakeStore { return &fakeStore{} }

func (f *fakeStore) CreateMemoryItem(_ context.Context, item *MemoryItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	if item.ID == "" {
		item.ID = "mem_fake_" + strconv.FormatInt(f.seq, 10)
	}
	if item.Confidence == 0 {
		item.Confidence = 1.0
	}
	if item.Status == "" {
		item.Status = "PROPOSED"
	}
	if item.Level == "" {
		item.Level = "project"
	}
	f.memories = append(f.memories, item)
	return nil
}

func (f *fakeStore) SearchMemory(_ context.Context, projectID string, query string, tags []string, limit int) ([]*MemoryItem, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	terms := strings.Fields(strings.ToLower(query))
	var out []*MemoryItem
	for _, item := range f.memories {
		if item.ProjectID != "" && item.ProjectID != projectID {
			continue
		}
		if item.Status == "REJECTED" || item.Status == "SUPERSEDED" {
			continue
		}
		if len(terms) > 0 {
			combined := strings.ToLower(item.Key + " " + item.Content + " " + strings.Join(item.Tags, " "))
			matched := false
			for _, t := range terms {
				if strings.Contains(combined, t) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(tags) > 0 && !hasAnyTag(item.Tags, tags) {
			continue
		}
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func hasAnyTag(have, want []string) bool {
	set := map[string]bool{}
	for _, t := range have {
		set[strings.ToLower(t)] = true
	}
	for _, t := range want {
		if set[strings.ToLower(t)] {
			return true
		}
	}
	return false
}

func (f *fakeStore) CreateEpisode(_ context.Context, ep *Episode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	if ep.ID == "" {
		ep.ID = "ep_fake_" + strconv.FormatInt(f.seq, 10)
	}
	if ep.Status == "" {
		ep.Status = "OPEN"
	}
	f.episodes = append(f.episodes, ep)
	return nil
}

func (f *fakeStore) SearchEpisodes(_ context.Context, projectID, errorPattern, query, file, status string, limit int) ([]*Episode, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	lowErr := strings.ToLower(errorPattern)
	lowQ := strings.ToLower(query)
	lowFile := strings.ToLower(file)
	var out []*Episode
	for _, ep := range f.episodes {
		if ep.ProjectID != "" && ep.ProjectID != projectID {
			continue
		}
		match := false
		if lowErr != "" {
			// Either-direction substring: stored patterns may be broader
			// ("ConnectionTimeout") or narrower than the query.
			for _, p := range ep.ErrorPatterns {
				lp := strings.ToLower(p)
				if strings.Contains(lp, lowErr) || strings.Contains(lowErr, lp) {
					match = true
					break
				}
			}
			if !match {
				narrative := strings.ToLower(ep.Title + " " + ep.Trigger + " " + ep.RootCause + " " + ep.Resolution)
				match = strings.Contains(narrative, lowErr)
			}
		}
		if !match && lowQ != "" {
			narrative := strings.ToLower(ep.Title + " " + ep.Trigger + " " + ep.RootCause + " " + ep.Resolution)
			match = strings.Contains(narrative, lowQ)
		}
		if !(match || (lowErr == "" && lowQ == "")) {
			continue
		}
		// File/status narrow BEFORE the limit (issue #156).
		if lowFile != "" {
			hit := false
			for _, fi := range ep.FilesInvolved {
				if strings.Contains(strings.ToLower(fi), lowFile) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if status != "" && !strings.EqualFold(ep.Status, status) {
			continue
		}
		out = append(out, ep)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) GetActiveWorkspace(_ context.Context, _ string) (*Workspace, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.workspace == nil {
		return nil, errNotFound
	}
	return f.workspace, nil
}

func (f *fakeStore) GetProject(_ context.Context, _ string) (*Project, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.project == nil {
		return nil, errNotFound
	}
	return f.project, nil
}

// tempDir-aware constructor helper (testing.T needed for TempDir).
func newTestServerWithT(t *testing.T) (*Server, *fakeStore) {
	t.Helper()
	dir := t.TempDir()
	ms := newFakeStore()
	ms.workspace = &Workspace{Branch: "main", CommitSHA: "abc123", Path: dir}
	ms.project = &Project{DisplayName: "central-memory", FolderName: "central-memory"}
	s := NewServer(ms, Config{
		ProjectID:     "proj_test",
		ProjectName:   "central-memory",
		Branch:        "main",
		CommitSHA:     "abc123",
		WorkspacePath: dir,
		TokenBudget:   DefaultTokenBudget,
	})
	return s, ms
}

func callRaw(t *testing.T, s *Server, method string, params any) *Response {
	t.Helper()
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		rawParams = b
	}
	id := json.RawMessage(`1`)
	req := Request{JSONRPC: JSONRPCVersion, ID: &id, Method: method, Params: rawParams}
	frame, _ := json.Marshal(req)
	return s.Handle(context.Background(), frame)
}

func callTool(t *testing.T, s *Server, name string, args any) *Response {
	t.Helper()
	var rawArgs json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		rawArgs = b
	}
	return callRaw(t, s, "tools/call", map[string]any{"name": name, "arguments": json.RawMessage(rawArgs)})
}

func resultMap(t *testing.T, r *Response) map[string]any {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	m, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want map", r.Result)
	}
	return m
}

// num coerces a result field to float64 regardless of whether it was stored
// as int, int64, float64, or json.Number.
func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	switch v := m[key].(type) {
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case float32:
		return float64(v)
	case float64:
		return v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			t.Fatalf("%s = %q, not numeric", key, v.String())
		}
		return f
	default:
		t.Fatalf("%s is %T, want numeric", key, m[key])
		return 0
	}
}

func TestHandleInitialize(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callRaw(t, s, "initialize", map[string]any{"protocolVersion": ProtocolVersion})
	m := resultMap(t, r)
	if m["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v", m["protocolVersion"])
	}
	info, _ := m["serverInfo"].(map[string]any)
	if info["name"] != ServerName {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
	if _, ok := m["capabilities"]; !ok {
		t.Error("missing capabilities")
	}
}

func TestHandleToolsList(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callRaw(t, s, "tools/list", nil)
	m := resultMap(t, r)
	tools, _ := m["tools"].([]Tool)
	if len(tools) != 8 {
		t.Fatalf("got %d tools, want 8", len(tools))
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("tool %q missing description/schema", tl.Name)
		}
	}
	for _, want := range []string{
		"memory_search", "memory_write", "memory_reflect",
		"episode_search", "episode_report", "workspace_info",
		"file_read", "file_write",
	} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
	// NO sampling: locked decision.
	for name := range names {
		if strings.Contains(name, "sampl") {
			t.Errorf("sampling tool must not exist: %q", name)
		}
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callTool(t, s, "memory_delete", nil)
	if r.Error == nil || r.Error.Code != ErrMethodNotFound {
		t.Fatalf("got %+v, want code %d", r.Error, ErrMethodNotFound)
	}
}

func TestDispatchUnknownMethod(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callRaw(t, s, "roots/list", nil)
	if r.Error == nil || r.Error.Code != ErrMethodNotFound {
		t.Fatalf("got %+v, want code %d", r.Error, ErrMethodNotFound)
	}
}

func TestDispatchMissingToolName(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callRaw(t, s, "tools/call", map[string]any{})
	if r.Error == nil || r.Error.Code != ErrInvalidParams {
		t.Fatalf("got %+v, want code %d", r.Error, ErrInvalidParams)
	}
}

func TestMemorySearchHintPresence(t *testing.T) {
	s, ms := newTestServerWithT(t)
	ctx := context.Background()
	if err := ms.CreateMemoryItem(ctx, &MemoryItem{
		ProjectID: "proj_test", Key: "testing/framework",
		Content: "The team uses pytest with fixture-based setup for integration tests.",
		Level:   "project", Scope: "decision",
	}); err != nil {
		t.Fatal(err)
	}

	r := callTool(t, s, "memory_search", map[string]any{"query": "pytest fixtures"})
	m := resultMap(t, r)

	hint, _ := m["reflection_hint"].(string)
	if hint != ReflectionHint {
		t.Errorf("reflection_hint = %q, want piggyback hint", hint)
	}
	ctxXML, _ := m["context"].(string)
	if !strings.Contains(ctxXML, "pytest") {
		t.Errorf("context missing seeded memory: %s", ctxXML)
	}
	if !strings.HasPrefix(ctxXML, "<project_memory") || !strings.HasSuffix(ctxXML, "</project_memory>") {
		t.Errorf("context is not a project_memory block: %s", ctxXML)
	}
	if n := num(t, m, "items_included"); n < 1 {
		t.Errorf("items_included = %v, want >= 1", m["items_included"])
	}
	if n := num(t, m, "token_count"); n <= 0 {
		t.Errorf("token_count = %v, want > 0", m["token_count"])
	}
	if _, ok := m["budget_remaining"]; !ok {
		t.Error("missing budget_remaining")
	}
}

func TestMemorySearchRequiresQuery(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callTool(t, s, "memory_search", map[string]any{"query": "   "})
	if r.Error == nil || r.Error.Code != ErrInvalidParams {
		t.Fatalf("got %+v, want code %d", r.Error, ErrInvalidParams)
	}
}

func TestMemoryReflectVoluntary(t *testing.T) {
	s, ms := newTestServerWithT(t)
	ctx := context.Background()

	// Empty call is a valid no-op — never an error.
	r := callTool(t, s, "memory_reflect", map[string]any{})
	m := resultMap(t, r)
	if n := num(t, m, "recorded"); n != 0 {
		t.Errorf("recorded = %v, want 0", m["recorded"])
	}

	// With takeaways: recorded as PROPOSED session memories.
	r = callTool(t, s, "memory_reflect", map[string]any{
		"summary": "Finished auth refactor",
		"memories": []any{map[string]any{
			"key":     "auth/library",
			"content": "The team decided to use the jwx library for JWT verification in middleware.",
		}},
	})
	m = resultMap(t, r)
	if n := num(t, m, "recorded"); n != 1 {
		t.Errorf("recorded = %v, want 1", m["recorded"])
	}
	if _, ok := m["message"].(string); !ok {
		t.Error("missing confirmation message")
	}
	items, err := ms.SearchMemory(ctx, "proj_test", "jwx", nil, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("reflected memory not persisted: %v %v", items, err)
	}
	if items[0].Status != "PROPOSED" || items[0].Level != "session" {
		t.Errorf("got status=%q level=%q, want PROPOSED/session", items[0].Status, items[0].Level)
	}
}

func TestEpisodeReportAndSearch(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callTool(t, s, "episode_report", map[string]any{
		"title": "Auth timeout on WebSocket upgrade", "episode_type": "bug_fix",
		"trigger": "ConnectionTimeout in ws.go:142",
	})
	m := resultMap(t, r)
	if m["status"] != "OPEN" {
		t.Errorf("status = %v, want OPEN", m["status"])
	}

	r = callTool(t, s, "episode_search", map[string]any{"query": "WebSocket"})
	m = resultMap(t, r)
	if n := num(t, m, "count"); n != 1 {
		t.Fatalf("count = %v, want 1", m["count"])
	}
}

func TestWorkspaceInfo(t *testing.T) {
	s, _ := newTestServerWithT(t)
	r := callTool(t, s, "workspace_info", map[string]any{})
	m := resultMap(t, r)
	if m["project"] != "central-memory" || m["branch"] != "main" || m["commit"] != "abc123" {
		t.Errorf("unexpected workspace_info: %v", m)
	}
}

func TestServeStdioRoundTrip(t *testing.T) {
	s, _ := newTestServerWithT(t)
	in := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n"
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2: %q", len(lines), out.String())
	}
	for i, line := range lines {
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("line %d not JSON: %s", i, line)
		}
		if resp.Error != nil {
			t.Fatalf("line %d error: %v", i, resp.Error)
		}
	}
}
