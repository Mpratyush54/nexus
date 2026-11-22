package server

// Cloud MCP adapter: vector search with a similarity floor and keyword
// fallback, use tracking on agent retrieval only, and server-derived
// ownership for personal writes. Browsing GET /memory/search is not use.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

// qualityVec is a unit vector with cosine `sim` to e0 (the test query).
func qualityVec(sim float64) []float32 {
	v := make([]float32, store.EmbeddingDim)
	v[0] = float32(sim)
	v[1] = float32(math.Sqrt(1 - sim*sim))
	return v
}

func cloudMCPSearch(t *testing.T, s *Server, projectID, user, query string) map[string]any {
	t.Helper()
	srv := mcp.NewServer(agentMCPStore{Store: s.Store, UserID: user}, mcp.Config{
		ProjectID: projectID, ProjectName: "quality", AgentName: "test-agent",
		// Every query embeds to e0, so a row's similarity is its qualityVec arg.
		Embed: func(string) []float32 { return qualityVec(1) },
	})
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory_search",
			"arguments": map[string]any{"query": query, "project_id": projectID},
		},
	})
	resp := srv.Handle(context.Background(), raw)
	if resp == nil || resp.Error != nil {
		t.Fatalf("memory_search failed: %+v", resp)
	}
	out, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func qualityProject(t *testing.T, s *Server, name string) *store.Project {
	t.Helper()
	ctx := context.Background()
	p, err := s.Store.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ClaimProject(ctx, p.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	return p
}

func qualityMemory(t *testing.T, s *Server, projectID, key, content string, sim float64) *store.MemoryItem {
	t.Helper()
	m := &store.MemoryItem{
		ProjectID: projectID, Key: key, Content: content, Status: store.StatusConfirmed,
		Confidence: 0.9, ProposedBy: "alice", Embedding: qualityVec(sim),
	}
	if err := s.Store.CreateMemoryItem(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCloudMCPVectorBelowFloorFallsBackToKeyword(t *testing.T) {
	s := newTestServer()
	p := qualityProject(t, s, "cloud-floor-low")
	// Similar enough to rank (> 0) but under store.MinVectorSimilarity.
	m := qualityMemory(t, s, p.ID, "deploy/order", "deploy runs migrations before switching traffic", store.MinVectorSimilarity/2)

	res := cloudMCPSearch(t, s, p.ID, "alice", "deploy")
	if res["match"] == "vector" {
		t.Fatalf("below-floor rows were served as vector hits: %v", res["match"])
	}
	if res["match"] != "phrase" {
		t.Fatalf("match = %v, want phrase (keyword fallback)", res["match"])
	}
	if n, _ := res["items_included"].(float64); n < 1 {
		t.Fatalf("keyword fallback served nothing: %v", res)
	}
	got, err := s.Store.GetMemoryItem(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UseCount != 1 {
		t.Fatalf("use_count = %d after MCP search, want 1", got.UseCount)
	}
}

func TestCloudMCPVectorAboveFloorUsedAndRecordsUse(t *testing.T) {
	s := newTestServer()
	p := qualityProject(t, s, "cloud-floor-high")
	m := qualityMemory(t, s, p.ID, "deploy/order", "deploy runs migrations before switching traffic", 0.9)
	before, _ := s.Store.GetMemoryItem(context.Background(), m.ID)

	// The query shares no words with the row: only the vector path finds it.
	res := cloudMCPSearch(t, s, p.ID, "alice", "release sequencing")
	if res["match"] != "vector" {
		t.Fatalf("match = %v, want vector", res["match"])
	}
	got, err := s.Store.GetMemoryItem(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UseCount != 1 || got.LastUsedAt.IsZero() {
		t.Fatalf("MCP memory_search did not record use: count=%d last=%v", got.UseCount, got.LastUsedAt)
	}
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("recording use moved updated_at: %v -> %v", before.UpdatedAt, got.UpdatedAt)
	}
}

func TestCloudMCPVectorRespectsViewer(t *testing.T) {
	s := newTestServer()
	p := qualityProject(t, s, "cloud-viewer")
	ctx := context.Background()
	if err := s.Store.GrantMember(ctx, p.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	priv := &store.MemoryItem{
		ProjectID: p.ID, Key: "editor/theme", Level: store.LevelPersonal, Status: store.StatusConfirmed,
		Content: "alice prefers a dark editor theme everywhere", Confidence: 0.9,
		UserID: "alice", ProposedBy: "alice", Embedding: qualityVec(0.9),
	}
	if err := s.Store.CreateMemoryItem(ctx, priv); err != nil {
		t.Fatal(err)
	}
	a := agentMCPStore{Store: s.Store, UserID: "alice"}
	b := agentMCPStore{Store: s.Store, UserID: "bob"}
	if got, err := a.SearchMemoryVector(ctx, p.ID, qualityVec(1), 10); err != nil || len(got) != 1 {
		t.Fatalf("owner vector search = %d rows, err %v; want 1", len(got), err)
	}
	if got, err := b.SearchMemoryVector(ctx, p.ID, qualityVec(1), 10); err != nil || len(got) != 0 {
		t.Fatalf("teammate vector search = %d rows, err %v; want 0", len(got), err)
	}
	if got, err := b.SearchMemory(ctx, p.ID, "", nil, 10); err != nil || len(got) != 0 {
		t.Fatalf("teammate keyword search = %d rows, err %v; want 0", len(got), err)
	}
}

func TestCloudMCPPersonalWriteOwnedByCaller(t *testing.T) {
	s := newTestServer()
	p := qualityProject(t, s, "cloud-write")
	a := agentMCPStore{Store: s.Store, UserID: "alice"}
	item := &mcp.MemoryItem{
		ProjectID: p.ID, Key: "editor/font", Level: "personal", UserID: "someone-else",
		Content: "I prefer a monospace font with ligatures in the editor.",
	}
	if err := a.CreateMemoryItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	got, err := s.Store.GetMemoryItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "alice" || got.ProposedBy != "alice" || got.Visibility != store.VisibilityPrivate {
		t.Fatalf("personal write: user=%q proposed_by=%q visibility=%q; want alice/alice/private", got.UserID, got.ProposedBy, got.Visibility)
	}
}

func TestMemoryBrowseDoesNotRecordUse(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	p := qualityProject(t, s, "browse-no-use")
	m := qualityMemory(t, s, p.ID, "ops/deploy", "deploy with migrations applied before switching traffic over", 0.9)
	before, err := s.Store.GetMemoryItem(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	tok := loginAs(t, s, "alice")
	ensureMembership(t, s, tok, p.ID)
	time.Sleep(5 * time.Millisecond)

	// The desktop Memory page: list pages, a filter query, a level filter.
	for _, q := range []string{
		"&limit=50&offset=0",
		"&limit=50&offset=0&q=deploy",
		"&limit=50&offset=0&level=project",
	} {
		rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id="+p.ID+q, tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("browse %s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	got, err := s.Store.GetMemoryItem(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UseCount != 0 || !got.LastUsedAt.IsZero() {
		t.Fatalf("browsing recorded use: count=%d last=%v", got.UseCount, got.LastUsedAt)
	}
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("browsing moved updated_at: %v -> %v", before.UpdatedAt, got.UpdatedAt)
	}

	// Agent retrieval over REST (mcp.HTTPStore) opts in and is counted.
	rec := doJSON(t, s, http.MethodGet, "/memory/search?project_id="+p.ID+"&q=deploy&record_use=1", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent search: %d %s", rec.Code, rec.Body.String())
	}
	got, _ = s.Store.GetMemoryItem(ctx, m.ID)
	if got.UseCount != 1 {
		t.Fatalf("record_use=1 search: use_count=%d, want 1", got.UseCount)
	}
}
