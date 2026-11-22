package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

func TestCodeLinksRouteHarvestStalenessAndAgentTools(t *testing.T) {
	s, mem, pid := briefFixture(t)
	ctx := t.Context()
	owner := loginAs(t, s, "owner")
	sess, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: pid, OwnerUserID: "owner", Harness: "claude",
		NativeID: "native-42", Title: "Wire code links", WorkspaceRootHint: `D:\repo`})
	if err != nil {
		t.Fatal(err)
	}
	url := "/v1/projects/" + pid + "/code-links"
	links := []map[string]any{
		{"path": "internal/mcp/tools.go", "hash": "t1"},
		{"path": "internal/server/routes.go", "hash": "r1"},
		{"path": "../outside.go"},
	}

	if rec := doJSON(t, s, http.MethodPost, url, loginAs(t, s, "stranger"), map[string]any{"session_native_id": "native-42", "links": links}); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, url, owner, map[string]any{"links": links}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no session: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, url, owner, map[string]any{"session_native_id": "nope", "links": links}); rec.Code != http.StatusConflict {
		t.Fatalf("unknown session: %d", rec.Code)
	}
	rec := doJSON(t, s, http.MethodPost, url, owner, map[string]any{"session_native_id": "native-42", "links": links})
	var res struct {
		SessionID string `json:"session_id"`
		Linked    int    `json:"linked"`
		Skipped   int    `json:"skipped"`
	}
	decodeBody(t, rec, &res)
	if rec.Code != http.StatusOK || res.SessionID != sess.ID || res.Linked != 2 || res.Skipped != 1 {
		t.Fatalf("code-links: %d %+v", rec.Code, res)
	}

	// A memory harvested from that session links to its files_affected
	// (absolute path made workspace-relative) and to the touched files.
	item, err := s.persistHarvestMemory(ctx, &store.HarvestJob{ProjectID: pid}, extract.Proposal{
		Key: "arch/routes", Content: "Every HTTP route is registered from registerRoutes in routes.go.",
		Level: "project", Scope: "decision", Confidence: 0.9,
		FilesAffected: []string{`D:\repo\internal\server\routes.go`, `D:\repo\docs\routing.md`},
	}, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mem.ListCodeLinksForSource(ctx, pid, store.LinkSrcMemory, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	rel := map[string]string{}
	for _, h := range got {
		rel[h.Path+"|"+h.Relation] = h.ObservedHash
	}
	for key, hash := range map[string]string{
		"internal/server/routes.go|decided_about": "r1",
		"docs/routing.md|decided_about":           "",
		"internal/server/routes.go|touched":       "r1",
		"internal/mcp/tools.go|touched":           "t1",
	} {
		if h, ok := rel[key]; !ok || h != hash {
			t.Fatalf("memory link %s: %v (all %v)", key, h, rel)
		}
	}

	// Same hash: known fresh even though the path rule would mark it.
	time.Sleep(5 * time.Millisecond)
	fc := "/v1/projects/" + pid + "/files-changed"
	rec = doJSON(t, s, http.MethodPost, fc, owner, map[string]any{"paths": []string{"internal/server/routes.go"}, "hashes": map[string]string{"internal/server/routes.go": "r1"}})
	var fcRes struct {
		Marked int `json:"marked"`
		Fresh  int `json:"fresh"`
	}
	decodeBody(t, rec, &fcRes)
	if rec.Code != http.StatusOK || fcRes.Marked != 0 || fcRes.Fresh != 1 {
		t.Fatalf("same hash: %d %+v", rec.Code, fcRes)
	}
	// New hash: stale.
	rec = doJSON(t, s, http.MethodPost, fc, owner, map[string]any{"hashes": map[string]string{"internal/server/routes.go": "r2"}})
	decodeBody(t, rec, &fcRes)
	if rec.Code != http.StatusOK || fcRes.Marked != 1 {
		t.Fatalf("new hash: %d %+v", rec.Code, fcRes)
	}
	if m, _ := mem.GetMemoryItem(ctx, item.ID); m.StaleSince == nil {
		t.Fatal("memory not stale after hash change")
	}

	call := func(token, name string, args map[string]any) map[string]any {
		return mcpCall(t, s, token, pid, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args}})
	}
	out := call(owner, "related_to", map[string]any{"file": "tools.go"})
	items, _ := out["items"].([]any)
	var sawMem, sawSess bool
	for _, it := range items {
		row, _ := it.(map[string]any)
		switch row["kind"] {
		case "memory":
			sawMem = row["id"] == item.ID && row["relation"] == "touched" && row["stale"] == true && row["source"] == "session:"+sess.ID
		case "session":
			sawSess = row["session_id"] == sess.ID && row["title"] == "Wire code links"
		}
	}
	if !sawMem || !sawSess || out["match"] != "links" {
		t.Fatalf("related_to: %v", out)
	}

	hist := call(owner, "file_history", map[string]any{"file": "internal/mcp/tools.go"})
	sessions, _ := hist["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("file_history: %v", hist)
	}
	if first, _ := sessions[0].(map[string]any); first["session_id"] != sess.ID || first["title"] != "Wire code links" || first["started"] == "" {
		t.Fatalf("file_history row: %v", first)
	}
	// The editor sees the project memory but not the owner's private chat.
	hist = call(loginAs(t, s, "editor"), "file_history", map[string]any{"file": "tools.go"})
	if n, _ := hist["count"].(float64); n != 0 {
		t.Fatalf("editor sees private session: %v", hist)
	}

	// memory_search file= prefers links: tools.go is not in files_affected.
	res2 := call(owner, "memory_search", map[string]any{"query": "zzzz-nothing", "file": "internal/mcp/tools.go"})
	if ctxXML, _ := res2["context"].(string); !strings.Contains(ctxXML, `key="arch/routes"`) {
		t.Fatalf("memory_search file via links:\n%v", res2)
	}
}
