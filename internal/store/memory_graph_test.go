package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// graphIndex maps a graph's nodes by id and its edges by from|to|relation.
func graphIndex(g *ProjectGraph) (map[string]GraphNode, map[string]bool) {
	nodes := map[string]GraphNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[e.From+"|"+e.To+"|"+e.Relation] = true
	}
	return nodes, edges
}

// checkGraphEdgesClosed fails when an edge names a node not in the graph.
func checkGraphEdgesClosed(t *testing.T, g *ProjectGraph) {
	t.Helper()
	nodes, _ := graphIndex(g)
	for _, e := range g.Edges {
		if _, ok := nodes[e.From]; !ok {
			t.Fatalf("edge from missing node: %+v", e)
		}
		if _, ok := nodes[e.To]; !ok {
			t.Fatalf("edge to missing node: %+v", e)
		}
	}
}

func TestMemStoreProjectGraph(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	pid, sid, memID, privID := codeLinkFixture(t, s)
	// Another user's private session (not readable by owner) that touched a
	// secret file, and a brief citing the owner's session.
	if err := s.GrantMember(ctx, pid, "other", "owner"); err != nil {
		t.Fatal(err)
	}
	otherSess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: pid, OwnerUserID: "other", Harness: "codex", NativeID: "native-9", Title: "Secret chat"})
	if err != nil {
		t.Fatal(err)
	}
	brief := &MemoryItem{ProjectID: pid, Key: ProjectBriefKey, Content: "What the project is and how it is built.", Status: StatusConfirmed, Confidence: 0.2,
		Tags: []string{ProjectBriefTag, pinnedTag, ProjectBriefSourceTag + sid}}
	if err := s.CreateMemoryItem(ctx, brief); err != nil {
		t.Fatal(err)
	}
	tools, _ := s.UpsertCodeEntity(ctx, pid, CodeKindFile, "internal/mcp/tools.go", "")
	secret, _ := s.UpsertCodeEntity(ctx, pid, CodeKindFile, "internal/secret/plan.go", "")
	privFile, _ := s.UpsertCodeEntity(ctx, pid, CodeKindFile, "internal/private/note.go", "")
	for _, l := range []*EntityLink{
		{ProjectID: pid, EntityID: tools.ID, SrcKind: LinkSrcSession, SrcID: sid, Relation: RelationTouched, Source: LinkSourceToolCall},
		{ProjectID: pid, EntityID: tools.ID, SrcKind: LinkSrcMemory, SrcID: memID, Relation: RelationDecidedAbout, Source: LinkSourceLLM, ObservedHash: "h1"},
		{ProjectID: pid, EntityID: secret.ID, SrcKind: LinkSrcSession, SrcID: otherSess.ID, Relation: RelationTouched, Source: LinkSourceToolCall},
		{ProjectID: pid, EntityID: privFile.ID, SrcKind: LinkSrcMemory, SrcID: privID, Relation: RelationDecidedAbout, Source: LinkSourceLLM},
	} {
		if err := s.AddEntityLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	// The file changed since the memory's link: the memory is stale.
	_, _ = s.SetCodeEntityHash(ctx, pid, "internal/mcp/tools.go", "h2", time.Now())
	if marked, _, _ := s.MarkStaleByCodeHash(ctx, pid, []string{"internal/mcp/tools.go"}, time.Now()); len(marked) != 1 {
		t.Fatalf("marked: %v", marked)
	}

	links, err := s.ListProjectCodeLinks(ctx, pid, 0)
	if err != nil || len(links) != 4 {
		t.Fatalf("project links: %d %v", len(links), err)
	}

	src := GraphSources{Memories: s, Sessions: s, Links: s}
	g, err := LoadProjectGraph(ctx, src, "owner", pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphEdgesClosed(t, g)
	nodes, edges := graphIndex(g)
	m := nodes["memory:"+memID]
	if m.Kind != GraphKindMemory || !m.Stale || m.Label != "mcp/tools" {
		t.Fatalf("memory node: %+v", m)
	}
	if n := nodes["session:"+sid]; n.Kind != GraphKindSession || n.Label != "Add links" || n.Ref != sid {
		t.Fatalf("session node: %+v", n)
	}
	if n := nodes["file:internal/mcp/tools.go"]; n.Kind != GraphKindFile || n.Label != "tools.go" || n.Sub != "internal/mcp/tools.go" || !n.Stale {
		t.Fatalf("file node: %+v", n)
	}
	if b := nodes["memory:"+brief.ID]; !b.Pinned {
		t.Fatalf("brief node: %+v", b)
	}
	for _, id := range []string{"memory:" + privID, "session:" + otherSess.ID, "file:internal/secret/plan.go", "file:internal/private/note.go"} {
		if _, ok := nodes[id]; ok {
			t.Fatalf("leaked %s", id)
		}
	}
	for _, e := range []string{
		"memory:" + memID + "|session:" + sid + "|from",
		"memory:" + brief.ID + "|session:" + sid + "|cites",
		"memory:" + memID + "|file:internal/mcp/tools.go|decided_about",
		"session:" + sid + "|file:internal/mcp/tools.go|touched",
	} {
		if !edges[e] {
			t.Fatalf("missing edge %s in %+v", e, g.Edges)
		}
	}
	if len(g.Nodes) != 4 || g.Truncated {
		t.Fatalf("nodes=%d truncated=%v: %+v", len(g.Nodes), g.Truncated, g.Nodes)
	}

	// "other" reads their own session and its file, but not owner's
	// private session.
	og, err := LoadProjectGraph(ctx, src, "other", pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphEdgesClosed(t, og)
	on, _ := graphIndex(og)
	if _, ok := on["session:"+otherSess.ID]; !ok {
		t.Fatalf("own session missing: %+v", og.Nodes)
	}
	if _, ok := on["file:internal/secret/plan.go"]; !ok {
		t.Fatalf("own session's file missing: %+v", og.Nodes)
	}
	if _, ok := on["session:"+sid]; ok {
		t.Fatal("other user's private session leaked")
	}

	// The cap keeps pinned memories first.
	for i := 0; i < 30; i++ {
		if err := s.CreateMemoryItem(ctx, &MemoryItem{ProjectID: pid, Key: fmt.Sprintf("bulk/%02d", i), Content: "Bulk memory used to fill the graph cap.", Status: StatusConfirmed, Confidence: 0.9}); err != nil {
			t.Fatal(err)
		}
	}
	small, err := LoadProjectGraph(ctx, src, "owner", pid, 10)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphEdgesClosed(t, small)
	sn, _ := graphIndex(small)
	if len(small.Nodes) > 10 || !small.Truncated {
		t.Fatalf("cap: %d %v", len(small.Nodes), small.Truncated)
	}
	if _, ok := sn["memory:"+brief.ID]; !ok {
		t.Fatal("pinned memory dropped by the cap")
	}
	if ClampGraphLimit(0) != DefaultGraphNodes || ClampGraphLimit(5000) != MaxGraphNodes || ClampGraphLimit(3) != 10 {
		t.Fatal("clamp")
	}
}

func TestPostgresProjectGraph(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	name := fmt.Sprintf("graph-%d", time.Now().UnixNano())
	newUser := func(n string) string {
		var id string
		if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, id) })
		return id
	}
	owner, other := newUser(name), newUser(name+"-other")
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_versions WHERE memory_id IN (SELECT id FROM memory_items WHERE project_id=$1::uuid)`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_items WHERE project_id=$1::uuid`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID)
	}()
	if _, err := s.ClaimProject(ctx, project.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantMember(ctx, project.ID, other, owner); err != nil {
		t.Fatal(err)
	}
	sess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: "codex", NativeID: name, Title: "Graph chat"})
	if err != nil {
		t.Fatal(err)
	}
	otherSess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: other, Harness: "claude", NativeID: name + "-o", Title: "Secret chat"})
	if err != nil {
		t.Fatal(err)
	}
	mem := &MemoryItem{ProjectID: project.ID, Key: "graph/decision", Content: "Graph nodes come from memories, chats and files.", Status: StatusConfirmed,
		Confidence: 0.9, Tags: []string{"session:" + sess.ID}}
	priv := &MemoryItem{ProjectID: project.ID, Key: "graph/private", Content: "Private to the other user.", Status: StatusConfirmed,
		Visibility: VisibilityPrivate, ProposedBy: other, UserID: other}
	for _, m := range []*MemoryItem{mem, priv} {
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	file, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindFile, "web/graph.go", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindFile, "web/secret.go", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []*EntityLink{
		{ProjectID: project.ID, EntityID: file.ID, SrcKind: LinkSrcSession, SrcID: sess.ID, Relation: RelationTouched, Source: LinkSourceToolCall},
		{ProjectID: project.ID, EntityID: file.ID, SrcKind: LinkSrcMemory, SrcID: mem.ID, Relation: RelationDecidedAbout, Source: LinkSourceLLM},
		{ProjectID: project.ID, EntityID: secret.ID, SrcKind: LinkSrcSession, SrcID: otherSess.ID, Relation: RelationTouched, Source: LinkSourceToolCall},
		{ProjectID: project.ID, EntityID: secret.ID, SrcKind: LinkSrcMemory, SrcID: priv.ID, Relation: RelationDecidedAbout, Source: LinkSourceLLM},
	} {
		if err := s.AddEntityLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	links, err := s.ListProjectCodeLinks(ctx, project.ID, 0)
	if err != nil || len(links) != 4 {
		t.Fatalf("project links: %d %v", len(links), err)
	}
	g, err := LoadProjectGraph(ctx, GraphSources{Memories: s, Sessions: s, Links: s}, owner, project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkGraphEdgesClosed(t, g)
	nodes, edges := graphIndex(g)
	for _, id := range []string{"memory:" + mem.ID, "session:" + sess.ID, "file:web/graph.go"} {
		if _, ok := nodes[id]; !ok {
			t.Fatalf("missing %s: %+v", id, g.Nodes)
		}
	}
	for _, id := range []string{"memory:" + priv.ID, "session:" + otherSess.ID, "file:web/secret.go"} {
		if _, ok := nodes[id]; ok {
			t.Fatalf("leaked %s", id)
		}
	}
	for _, e := range []string{
		"memory:" + mem.ID + "|session:" + sess.ID + "|from",
		"memory:" + mem.ID + "|file:web/graph.go|decided_about",
		"session:" + sess.ID + "|file:web/graph.go|touched",
	} {
		if !edges[e] {
			t.Fatalf("missing edge %s: %+v", e, g.Edges)
		}
	}
}
