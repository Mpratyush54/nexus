package server

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

func TestProjectGraphRoute(t *testing.T) {
	s, mem, pid := briefFixture(t)
	ctx := t.Context()
	owner := loginAs(t, s, "owner")
	sess, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: pid, OwnerUserID: "owner", Harness: "claude", NativeID: "native-g", Title: "Draw the graph"})
	if err != nil {
		t.Fatal(err)
	}
	m := addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "ui/graph", Content: "The desktop draws the memory graph natively.", Tags: []string{"session:" + sess.ID}})
	priv := addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "note/private", Content: "SECRET-PRIVATE note visible to its author only.", Visibility: store.VisibilityPrivate, ProposedBy: "editor"})
	e, err := mem.UpsertCodeEntity(ctx, pid, store.CodeKindFile, "desktop/GraphPage.cs", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []*store.EntityLink{
		{ProjectID: pid, EntityID: e.ID, SrcKind: store.LinkSrcSession, SrcID: sess.ID, Relation: store.RelationTouched, Source: store.LinkSourceToolCall},
		{ProjectID: pid, EntityID: e.ID, SrcKind: store.LinkSrcMemory, SrcID: m.ID, Relation: store.RelationDecidedAbout, Source: store.LinkSourceLLM},
	} {
		if err := mem.AddEntityLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	url := "/v1/projects/" + pid + "/graph?limit=50"
	if rec := doJSON(t, s, http.MethodGet, url, loginAs(t, s, "stranger"), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/graph?limit=x", owner, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	rec := doJSON(t, s, http.MethodGet, url, owner, nil)
	var g store.ProjectGraph
	decodeBody(t, rec, &g)
	if rec.Code != http.StatusOK || g.ProjectID != pid {
		t.Fatalf("graph: %d %+v", rec.Code, g)
	}
	kinds := map[string]string{}
	for _, n := range g.Nodes {
		kinds[n.ID] = n.Kind
	}
	if kinds["memory:"+m.ID] != "memory" || kinds["session:"+sess.ID] != "session" || kinds["file:desktop/graphpage.cs"] != "file" {
		t.Fatalf("nodes: %+v", g.Nodes)
	}
	if _, ok := kinds["memory:"+priv.ID]; ok {
		t.Fatal("private memory of another user leaked")
	}
	if len(g.Edges) != 3 {
		t.Fatalf("edges: %+v", g.Edges)
	}
	// The author sees their private memory; not the owner's private chat.
	rec = doJSON(t, s, http.MethodGet, url, loginAs(t, s, "editor"), nil)
	decodeBody(t, rec, &g)
	kinds = map[string]string{}
	for _, n := range g.Nodes {
		kinds[n.ID] = n.Kind
	}
	if _, ok := kinds["memory:"+priv.ID]; !ok || rec.Code != http.StatusOK {
		t.Fatalf("author's private memory missing: %d %+v", rec.Code, g.Nodes)
	}
	if _, ok := kinds["session:"+sess.ID]; ok {
		t.Fatal("owner's private session leaked to editor")
	}
}
