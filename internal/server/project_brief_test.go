package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// briefFixture is a server with a deterministic (no LLM) extractor and a
// project owned by "owner" with "editor" as a member.
func briefFixture(t *testing.T) (*Server, *store.MemStore, string) {
	t.Helper()
	s := newTestServer()
	s.Extractor = extract.NewService(extract.Config{})
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "brief-proj-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, proj.ID, "editor", "owner"); err != nil {
		t.Fatal(err)
	}
	return s, mem, proj.ID
}

func addBriefMemory(t *testing.T, mem *store.MemStore, item *store.MemoryItem) *store.MemoryItem {
	t.Helper()
	if item.Status == "" {
		item.Status = store.StatusConfirmed
	}
	if item.Confidence == 0 {
		item.Confidence = 0.9
	}
	if err := mem.CreateMemoryItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestProjectBriefDeterministicExcludesPrivate(t *testing.T) {
	s, mem, pid := briefFixture(t)
	ctx := t.Context()
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/server", Content: "The server is a Go stdlib HTTP API over a store interface.", Category: "architecture", Tags: []string{"session:s1"}})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "deps/db", Content: "Postgres with pgvector holds every memory row.", Category: "dependencies", Tags: []string{"session:s2"}})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "conv/lf", Content: "Every Go file uses LF line endings and gofmt.", Category: "conventions"})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "pref/tabs", Content: "SECRET-PERSONAL editor prefers tabs in every file.", Level: store.LevelPersonal, UserID: "editor", ProposedBy: "editor", Confidence: 1})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "note/private", Content: "SECRET-PRIVATE project note visible to its author only.", Visibility: store.VisibilityPrivate, ProposedBy: "editor", Confidence: 1})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "note/shared", Content: "SECRET-SHARED note shared with one teammate only.", Visibility: store.VisibilityShared, ProposedBy: "editor", Confidence: 1})

	s.maybeRefreshProjectBrief(ctx, pid)
	brief := loadProjectBrief(ctx, mem, pid)
	if brief == nil {
		t.Fatal("brief not built")
	}
	if brief.Key != store.ProjectBriefKey || brief.Status != store.StatusConfirmed || brief.Level != store.LevelProject ||
		brief.Category != "architecture" || store.NormalizeVisibility(brief.Visibility) != store.VisibilityProject || !hasTag(brief, "pinned") {
		t.Fatalf("brief row shape: %+v", brief)
	}
	if strings.Contains(brief.Content, "SECRET") {
		t.Fatalf("private/personal memory leaked into brief:\n%s", brief.Content)
	}
	for _, want := range []string{"Go stdlib HTTP API", "(session:s1)", "Postgres with pgvector", "(session:s2)", "LF line endings"} {
		if !strings.Contains(brief.Content, want) {
			t.Fatalf("brief missing %q:\n%s", want, brief.Content)
		}
	}
	if len([]rune(brief.Content)) > extract.MaxBriefChars {
		t.Fatalf("brief too long: %d", len([]rune(brief.Content)))
	}

	// GET /v1/projects/{id}/brief: members only.
	rec := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/brief", loginAs(t, s, "editor"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get brief: %d %s", rec.Code, rec.Body.String())
	}
	var got projectBriefResponse
	decodeBody(t, rec, &got)
	if got.Content != brief.Content || got.UpdatedAt == nil || strings.Join(got.Sources, ",") != "s2,s1" {
		t.Fatalf("brief response: %+v", got)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/brief", loginAs(t, s, "stranger"), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stranger get: %d", rec.Code)
	}
}

func TestProjectBriefDebounceAndTriggers(t *testing.T) {
	s, mem, pid := briefFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	s.briefNow = func() time.Time { return now }
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/one", Content: "First architecture decision for the debounce test.", Category: "architecture"})

	s.maybeRefreshProjectBrief(ctx, pid)
	b1 := loadProjectBrief(ctx, mem, pid)
	if b1 == nil {
		t.Fatal("missing brief must be built")
	}

	time.Sleep(5 * time.Millisecond)
	for i := 0; i < briefMinNew; i++ {
		addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/new" + string(rune('a'+i)), Content: "New architecture decision number " + string(rune('a'+i)) + " in this project.", Category: "architecture", Confidence: 0.99})
	}

	// Within the debounce window nothing is re-checked, even with 10 new rows.
	s.maybeRefreshProjectBrief(ctx, pid)
	if b := loadProjectBrief(ctx, mem, pid); !b.UpdatedAt.Equal(b1.UpdatedAt) {
		t.Fatal("debounce ignored")
	}

	// After the window, 10 new memories trigger a rebuild (same row).
	now = now.Add(briefDebounce + time.Minute)
	s.maybeRefreshProjectBrief(ctx, pid)
	b2 := loadProjectBrief(ctx, mem, pid)
	if b2.ID != b1.ID || !b2.UpdatedAt.After(b1.UpdatedAt) || !strings.Contains(b2.Content, "New architecture decision") {
		t.Fatalf("10 new memories should rebuild in place: %+v", b2)
	}

	// One new memory and a young brief: not due.
	time.Sleep(5 * time.Millisecond)
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/late", Content: "A late decision added after the second brief.", Category: "architecture"})
	now = now.Add(briefDebounce + time.Minute)
	s.maybeRefreshProjectBrief(ctx, pid)
	if b := loadProjectBrief(ctx, mem, pid); !b.UpdatedAt.Equal(b2.UpdatedAt) {
		t.Fatal("one new memory must not rebuild a fresh brief")
	}

	// Older than 24h and something changed: rebuild.
	now = now.Add(briefMaxAge + time.Hour)
	s.maybeRefreshProjectBrief(ctx, pid)
	if b := loadProjectBrief(ctx, mem, pid); !b.UpdatedAt.After(b2.UpdatedAt) {
		t.Fatal("stale brief with changes must rebuild")
	}

	// Still exactly one brief row.
	rows, _ := mem.SearchMemory(ctx, pid, store.ProjectBriefKey, nil, 50)
	n := 0
	for _, r := range rows {
		if r.Key == store.ProjectBriefKey {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("brief rows = %d", n)
	}
}

func TestProjectBriefRefreshRoute(t *testing.T) {
	s, mem, pid := briefFixture(t)
	owner := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/brief/refresh", loginAs(t, s, "editor"), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor refresh: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/brief/refresh", owner, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("empty project refresh: %d %s", rec.Code, rec.Body.String())
	}
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "api/routes", Content: "Routes are registered in internal/server/routes.go.", Category: "api", Tags: []string{"session:s7"}})
	rec = doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/brief/refresh", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner refresh: %d %s", rec.Code, rec.Body.String())
	}
	var got projectBriefResponse
	decodeBody(t, rec, &got)
	if !strings.Contains(got.Content, "internal/server/routes.go") || len(got.Sources) != 1 || got.Sources[0] != "s7" {
		t.Fatalf("refresh response: %+v", got)
	}
}

// TestHarvestJobBuildsBrief runs a real harvest job against a fake
// OpenRouter: the job saves memories, then the worker builds the brief
// with the LLM brief prompt.
func TestHarvestJobBuildsBrief(t *testing.T) {
	s, mem, pid := briefFixture(t)
	q := store.NewMemHarvestQueue()
	s.Harvest = q
	var briefPrompts int
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		content := `{"session_summary":"Goal: add a brief. Actions: wrote project_brief.go. Outcome: agents receive a brief.","decisions":[{"key":"arch/brief","content":"Agents receive a project brief on MCP initialize.","level":"project","scope":"decision","category":"architecture","confidence":0.9}]}`
		if len(body.Messages) > 0 && strings.Contains(body.Messages[0].Content, "onboarding brief") {
			briefPrompts++
			content = `{"brief":"# brief-proj\nGo server.\n- Agents receive a project brief on MCP initialize (session:01234567-89ab-cdef-0123-456789abcdef)"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	defer or.Close()
	cfg := extract.Config{APIKey: "test-key"}
	svc := extract.NewService(cfg)
	svc.Client = &extract.Client{Cfg: cfg, HTTP: or.Client(), BaseURLOverride: or.URL}
	s.Extractor = svc

	ctx := t.Context()
	job, _, err := q.EnqueueHarvestJob(ctx, pid, "test", []store.HarvestTurn{
		{Speaker: "user", Content: "Please add a project brief for agents.", SessionID: harvestUUID},
		{Speaker: "assistant", Content: "Added project_brief.go and wired MCP initialize.", SessionID: harvestUUID},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.ClaimNextHarvestJob(ctx)
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	s.processHarvestJob(ctx, claimed)
	brief := loadProjectBrief(ctx, mem, pid)
	if brief == nil || briefPrompts != 1 || brief.Source != "brief:"+extract.ProviderOpenRouter {
		t.Fatalf("brief after harvest: prompts=%d %+v", briefPrompts, brief)
	}
	if got := briefSources(brief); len(got) != 1 || got[0] != harvestUUID {
		t.Fatalf("sources = %v", got)
	}
	// A second job right away is debounced: no second brief prompt.
	s.maybeRefreshProjectBrief(ctx, pid)
	if briefPrompts != 1 {
		t.Fatalf("debounce: prompts=%d", briefPrompts)
	}
}

func mcpCall(t *testing.T, s *Server, token, projectID string, body map[string]any) map[string]any {
	t.Helper()
	rec := doJSONWithHeaders(t, s, http.MethodPost, "/v1/agent/mcp", token, map[string]string{"X-Nexus-Project": projectID}, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp: %d %s", rec.Code, rec.Body.String())
	}
	var rpc struct {
		Error  any            `json:"error"`
		Result map[string]any `json:"result"`
	}
	decodeBody(t, rec, &rpc)
	if rpc.Error != nil {
		t.Fatalf("mcp error: %v", rpc.Error)
	}
	return rpc.Result
}

func TestCloudMCPInitializeAndKnowledgeCarryBrief(t *testing.T) {
	s, mem, pid := briefFixture(t)
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/server", Content: "The server is a Go stdlib HTTP API over a store interface.", Category: "architecture", Tags: []string{"session:s1"}})
	if _, err := s.rebuildProjectBrief(t.Context(), pid, nil, nil); err != nil {
		t.Fatal(err)
	}
	brief := loadProjectBrief(t.Context(), mem, pid)
	tok := loginAs(t, s, "editor")

	res := mcpCall(t, s, tok, pid, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	instr, _ := res["instructions"].(string)
	if !strings.Contains(instr, "Nexus shared memory is available") || !strings.Contains(instr, brief.Content) {
		t.Fatalf("initialize instructions: %q", instr)
	}

	res = mcpCall(t, s, tok, pid, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "project_knowledge", "arguments": map[string]any{}}})
	items, _ := res["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("knowledge items: %v", res)
	}
	first, _ := items[0].(map[string]any)
	if first["key"] != store.ProjectBriefKey || res["brief"] != brief.Content {
		t.Fatalf("brief must lead project_knowledge: %v", first)
	}
	for _, it := range items[1:] {
		if m, _ := it.(map[string]any); m["key"] == store.ProjectBriefKey {
			t.Fatal("brief listed twice")
		}
	}
}

func TestFilesChangedMarksStaleAndAgentsSeeIt(t *testing.T) {
	s, mem, pid := briefFixture(t)
	owner := loginAs(t, s, "owner")
	routes := addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "api/routes", Content: "Routes are registered in routes.go via registerRoutes.", Category: "api",
		Tags: []string{"session:s9"}, FilesAffected: []string{"internal/server/routes.go"}})
	other := addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "api/other", Content: "Other routes live in routes_extra.go for sessions.", Category: "api",
		FilesAffected: []string{"internal/server/routes_extra.go"}})
	time.Sleep(5 * time.Millisecond)

	url := "/v1/projects/" + pid + "/files-changed"
	if rec := doJSON(t, s, http.MethodPost, url, loginAs(t, s, "stranger"), map[string]any{"paths": []string{"routes.go"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, url, owner, map[string]any{"paths": []string{}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty paths: %d", rec.Code)
	}
	// A change before the memory was written does not make it stale.
	rec := doJSON(t, s, http.MethodPost, url, owner, map[string]any{"paths": []string{`D:\repo\internal\server\routes.go`}, "at": time.Now().Add(-time.Hour).Format(time.RFC3339)})
	var res struct {
		Marked int      `json:"marked"`
		IDs    []string `json:"ids"`
	}
	decodeBody(t, rec, &res)
	if rec.Code != http.StatusOK || res.Marked != 0 {
		t.Fatalf("old change: %d %+v", rec.Code, res)
	}
	rec = doJSON(t, s, http.MethodPost, url, owner, map[string]any{"paths": []string{`D:\repo\internal\server\routes.go`}})
	decodeBody(t, rec, &res)
	if rec.Code != http.StatusOK || res.Marked != 1 || res.IDs[0] != routes.ID {
		t.Fatalf("change: %d %+v", rec.Code, res)
	}
	if m, _ := mem.GetMemoryItem(t.Context(), other.ID); m.StaleSince != nil {
		t.Fatal("non-matching memory marked stale")
	}

	search := func() string {
		res := mcpCall(t, s, owner, pid, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": "memory_search", "arguments": map[string]any{"query": "routes", "file": "server/routes.go"}}})
		ctxXML, _ := res["context"].(string)
		return ctxXML
	}
	ctxXML := search()
	day := time.Now().UTC().Format("2006-01-02")
	want := `<item key="api/routes" confidence="0.90" scope="fact" date="` + day + `" source="session:s9" files="internal/server/routes.go" stale="true" note="files changed since; verify before relying">`
	if !strings.Contains(ctxXML, want) {
		t.Fatalf("stale item rendering:\n%s", ctxXML)
	}
	if strings.Index(ctxXML, `key="api/routes"`) > strings.Index(ctxXML, `key="api/other"`) {
		t.Fatalf("file match must lead:\n%s", ctxXML)
	}

	// Editing the memory clears the flag.
	rec = doJSON(t, s, http.MethodPut, "/memory/"+routes.ID, owner, map[string]any{"content": "Routes are registered in routes.go and project_brief.go."})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if ctxXML = search(); strings.Contains(ctxXML, `stale="true"`) {
		t.Fatalf("edit must clear stale:\n%s", ctxXML)
	}
}

func TestStoreToMCPMemoryCarriesProvenance(t *testing.T) {
	at := time.Now().UTC()
	in := &store.MemoryItem{ID: "m1", Key: "k", Content: "c", Category: "api", FilesAffected: []string{"a.go"}, CreatedAt: at, UpdatedAt: at, StaleSince: &at}
	out := storeToMCPMemory(in)
	if out.Category != "api" || len(out.FilesAffected) != 1 || !out.CreatedAt.Equal(at) || !out.UpdatedAt.Equal(at) || out.StaleSince == nil {
		t.Fatalf("dropped fields: %+v", out)
	}
}
