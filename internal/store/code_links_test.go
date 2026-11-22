package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestCleanCodePath(t *testing.T) {
	for in, want := range map[string]string{
		`internal\mcp\tools.go`: "internal/mcp/tools.go",
		"./a//b.go":             "a/b.go",
		"Docs/README.md/":       "Docs/README.md",
		"../outside.go":         "",
		"a/../b.go":             "",
		"/abs/x.go":             "",
		`D:\repo\x.go`:          "",
		"":                      "",
	} {
		got, ok := CleanCodePath(in)
		if got != want || ok != (want != "") {
			t.Errorf("CleanCodePath(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

// codeLinkFixture: a project, an owned private session, a CONFIRMED
// memory from it, and a private memory of another user.
func codeLinkFixture(t *testing.T, s *MemStore) (projectID, sessionID, memID, privateID string) {
	t.Helper()
	ctx := context.Background()
	p, err := s.ResolveProject(ctx, "", "", "codelinks-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimProject(ctx, p.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: p.ID, OwnerUserID: "owner", Harness: "claude", NativeID: "native-1", Title: "Add links"})
	if err != nil {
		t.Fatal(err)
	}
	m := &MemoryItem{ProjectID: p.ID, Key: "mcp/tools", Content: "Tools are listed in tools.go and dispatched by CallTool.", Status: StatusConfirmed,
		Tags: []string{"session:" + sess.ID}, FilesAffected: []string{"internal/mcp/tools.go"}}
	if err := s.CreateMemoryItem(ctx, m); err != nil {
		t.Fatal(err)
	}
	priv := &MemoryItem{ProjectID: p.ID, Key: "mcp/private", Content: "A private note about tools.go for its author only.", Status: StatusConfirmed, Visibility: VisibilityPrivate, ProposedBy: "someone"}
	if err := s.CreateMemoryItem(ctx, priv); err != nil {
		t.Fatal(err)
	}
	return p.ID, sess.ID, m.ID, priv.ID
}

func TestMemStoreCodeLinksLifecycle(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	pid, sid, memID, privID := codeLinkFixture(t, s)

	e, err := s.UpsertCodeEntity(ctx, pid, CodeKindFile, `internal\mcp\tools.go`, "")
	if err != nil || e.Path != "internal/mcp/tools.go" {
		t.Fatalf("upsert: %+v %v", e, err)
	}
	again, err := s.UpsertCodeEntity(ctx, pid, CodeKindFile, "Internal/MCP/tools.go", "")
	if err != nil || again.ID != e.ID {
		t.Fatalf("case-insensitive reuse: %+v %v", again, err)
	}
	if _, err := s.UpsertCodeEntity(ctx, pid, CodeKindFile, "../x.go", ""); err == nil {
		t.Fatal("outside path accepted")
	}
	if err := s.AddEntityLink(ctx, &EntityLink{ProjectID: pid, EntityID: e.ID, SrcKind: "bogus", SrcID: sid, Relation: RelationTouched, Source: LinkSourceToolCall}); err == nil {
		t.Fatal("bad src kind accepted")
	}
	link := func(kind, id, rel, src, hash string) {
		t.Helper()
		if err := s.AddEntityLink(ctx, &EntityLink{ProjectID: pid, EntityID: e.ID, SrcKind: kind, SrcID: id, Relation: rel, Source: src, ObservedHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	link(LinkSrcSession, sid, RelationTouched, LinkSourceToolCall, "h1")
	link(LinkSrcSession, sid, RelationTouched, LinkSourceToolCall, "") // repeat keeps h1
	link(LinkSrcMemory, memID, RelationTouched, LinkSourceToolCall, "h1")
	link(LinkSrcMemory, privID, RelationDecidedAbout, LinkSourceLLM, "")

	src, err := s.ListCodeLinksForSource(ctx, pid, LinkSrcSession, sid)
	if err != nil || len(src) != 1 || src[0].Path != "internal/mcp/tools.go" || src[0].ObservedHash != "h1" {
		t.Fatalf("for source: %+v %v", src, err)
	}

	// Viewer "owner" sees the project memory and the session, not the
	// other user's private memory.
	hits, err := s.ListCodeLinksByPath(WithViewer(ctx, "owner"), pid, "mcp/tools.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawMem, sawSess bool
	for _, h := range hits {
		if h.Memory != nil && h.Memory.ID == privID {
			t.Fatal("private memory leaked")
		}
		sawMem = sawMem || (h.Memory != nil && h.Memory.ID == memID)
		sawSess = sawSess || (h.Session != nil && h.Session.ID == sid && h.Session.Title == "Add links")
	}
	if !sawMem || !sawSess || len(hits) != 2 {
		t.Fatalf("by path: %+v", hits)
	}
	if h, _ := s.ListCodeLinksByPath(ctx, pid, "ools.go", 10); len(h) != 0 {
		t.Fatal("partial segment matched")
	}

	// Same hash: the memory is fresh and the path rule spares it.
	at := time.Now().Add(time.Minute)
	if n, _ := s.SetCodeEntityHash(ctx, pid, "internal/mcp/TOOLS.go", "h1", at); n != 1 {
		t.Fatalf("set hash n=%d", n)
	}
	marked, fresh, err := s.MarkStaleByCodeHash(ctx, pid, []string{"internal/mcp/tools.go"}, at)
	if err != nil || len(marked) != 0 || len(fresh) != 1 || fresh[0] != memID {
		t.Fatalf("same hash: marked=%v fresh=%v %v", marked, fresh, err)
	}
	byPath, _ := s.MarkMemoriesStaleForFilesExcept(ctx, pid, []string{"internal/mcp/tools.go"}, at, map[string]bool{memID: true})
	if len(byPath) != 0 {
		t.Fatalf("fresh memory marked by path rule: %v", byPath)
	}

	// New hash: the memory is stale.
	_, _ = s.SetCodeEntityHash(ctx, pid, "internal/mcp/tools.go", "h2", at)
	marked, _, err = s.MarkStaleByCodeHash(ctx, pid, []string{"internal/mcp/tools.go"}, at)
	if err != nil || len(marked) != 1 || marked[0] != memID {
		t.Fatalf("new hash: %v %v", marked, err)
	}
	if got, _ := s.GetMemoryItem(ctx, memID); got.StaleSince == nil {
		t.Fatal("stale_since not set")
	}
	hits, _ = s.ListCodeLinksByPath(WithViewer(ctx, "owner"), pid, "tools.go", 10)
	for _, h := range hits {
		if h.Memory != nil && !h.CodeChanged() {
			t.Fatalf("memory link must report code changed: %+v", h)
		}
	}

	// The memory's own session re-edits the file: its link follows the
	// new hash and, with no link left disagreeing, it is no longer stale.
	_, _ = s.SetCodeEntityHash(ctx, pid, "internal/mcp/tools.go", "h3", at)
	refreshed, unstaled, err := s.RefreshMemoryLinkHashes(ctx, pid, e.ID, "h3", []string{memID, "unrelated"})
	if err != nil || len(refreshed) != 1 || refreshed[0] != memID || len(unstaled) != 1 {
		t.Fatalf("refresh: %v %v %v", refreshed, unstaled, err)
	}
	if got, _ := s.GetMemoryItem(ctx, memID); got.StaleSince != nil {
		t.Fatal("refresh must un-stale")
	}
	if marked, _, _ := s.MarkStaleByCodeHash(ctx, pid, []string{"internal/mcp/tools.go"}, at); len(marked) != 0 {
		t.Fatalf("refreshed memory re-marked: %v", marked)
	}

	if id, err := s.FindAgentSessionByNativeID(ctx, pid, "native-1"); err != nil || id != sid {
		t.Fatalf("native lookup: %q %v", id, err)
	}
	if _, err := s.FindAgentSessionByNativeID(ctx, pid, "missing"); err == nil {
		t.Fatal("missing native id resolved")
	}
}

func TestPostgresCodeLinks(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("codelinks-%d", time.Now().UnixNano())
	var owner string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_versions WHERE memory_id IN (SELECT id FROM memory_items WHERE project_id=$1::uuid)`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM memory_items WHERE project_id=$1::uuid`, project.ID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID)
	}()
	sess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: "codex", NativeID: name, Title: "Link files"})
	if err != nil {
		t.Fatal(err)
	}
	mem := &MemoryItem{ProjectID: project.ID, Key: "mcp/tools", Content: "Tools are listed in tools.go and dispatched by CallTool.", Status: StatusConfirmed,
		FilesAffected: []string{"internal/mcp/tools.go"}}
	if err := s.CreateMemoryItem(ctx, mem); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE memory_items SET created_at = now() - interval '1 hour' WHERE id=$1::uuid`, mem.ID); err != nil {
		t.Fatal(err)
	}

	e, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindFile, `internal\mcp\tools.go`, "")
	if err != nil || e.Path != "internal/mcp/tools.go" {
		t.Fatalf("upsert: %+v %v", e, err)
	}
	again, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindFile, "Internal/MCP/tools.go", "")
	if err != nil || again.ID != e.ID {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	sym, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindSymbol, "internal/mcp/tools.go", "CallTool")
	if err != nil || sym.ID == e.ID || sym.Symbol != "CallTool" {
		t.Fatalf("symbol entity: %+v %v", sym, err)
	}
	// The insert's ON CONFLICT on the expression index (a concurrent insert)
	// returns the existing row, for files (NULL symbol) and symbols alike.
	for _, want := range []*CodeEntity{e, sym} {
		got, err := s.insertCodeEntity(ctx, project.ID, want.Kind, want.Path, want.Symbol)
		if err != nil || got.ID != want.ID {
			t.Fatalf("on conflict %s/%s: %+v %v", want.Path, want.Symbol, got, err)
		}
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM code_entities WHERE project_id=$1::uuid`, project.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("entities = %d %v", n, err)
	}
	// INSERT…SELECT refuses an entity of another project (no row -> ErrNotFound).
	if err := s.AddEntityLink(ctx, &EntityLink{ProjectID: project.ID, EntityID: "00000000-0000-0000-0000-000000000000", SrcKind: LinkSrcSession,
		SrcID: sess.ID, Relation: RelationTouched, Source: LinkSourceToolCall}); err != ErrNotFound {
		t.Fatalf("foreign entity: %v", err)
	}
	for _, l := range []*EntityLink{
		{ProjectID: project.ID, EntityID: e.ID, SrcKind: LinkSrcSession, SrcID: sess.ID, Relation: RelationTouched, Source: LinkSourceToolCall, ObservedHash: "h1"},
		{ProjectID: project.ID, EntityID: e.ID, SrcKind: LinkSrcSession, SrcID: sess.ID, Relation: RelationTouched, Source: LinkSourceToolCall},
		{ProjectID: project.ID, EntityID: e.ID, SrcKind: LinkSrcMemory, SrcID: mem.ID, Relation: RelationTouched, Source: LinkSourceToolCall, ObservedHash: "h1"},
	} {
		if err := s.AddEntityLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	src, err := s.ListCodeLinksForSource(ctx, project.ID, LinkSrcSession, sess.ID)
	if err != nil || len(src) != 1 || src[0].ObservedHash != "h1" || src[0].Confidence != 1 {
		t.Fatalf("for source: %+v %v", src, err)
	}
	// A symbol link with a fractional confidence exercises the real/text casts.
	if err := s.AddEntityLink(ctx, &EntityLink{ProjectID: project.ID, EntityID: sym.ID, SrcKind: LinkSrcSession, SrcID: sess.ID,
		Relation: RelationMentions, Source: LinkSourceLLM, Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.ListCodeLinksByPath(WithViewer(ctx, owner), project.ID, "mcp/tools.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawMem, sawSess bool
	for _, h := range hits {
		sawMem = sawMem || (h.Memory != nil && h.Memory.ID == mem.ID)
		sawSess = sawSess || (h.Session != nil && h.Session.ID == sess.ID && h.Session.Title == "Link files")
	}
	if !sawMem || !sawSess {
		t.Fatalf("by path: %+v", hits)
	}

	at := time.Now().UTC()
	if n, err := s.SetCodeEntityHash(ctx, project.ID, "internal/mcp/tools.go", "h1", at); err != nil || n != 1 {
		t.Fatalf("set hash: %d %v", n, err)
	}
	marked, fresh, err := s.MarkStaleByCodeHash(ctx, project.ID, []string{"internal/mcp/tools.go"}, at)
	if err != nil || len(marked) != 0 || len(fresh) != 1 || fresh[0] != mem.ID {
		t.Fatalf("same hash: %v %v %v", marked, fresh, err)
	}
	byPath, err := s.MarkMemoriesStaleForFilesExcept(ctx, project.ID, []string{"internal/mcp/tools.go"}, at, map[string]bool{mem.ID: true})
	if err != nil || len(byPath) != 0 {
		t.Fatalf("except: %v %v", byPath, err)
	}
	if _, err := s.SetCodeEntityHash(ctx, project.ID, "internal/mcp/tools.go", "h2", at); err != nil {
		t.Fatal(err)
	}
	marked, _, err = s.MarkStaleByCodeHash(ctx, project.ID, []string{"internal/mcp/tools.go"}, at)
	if err != nil || len(marked) != 1 || marked[0] != mem.ID {
		t.Fatalf("new hash: %v %v", marked, err)
	}
	// Same-session refresh: link follows h3 and the memory is un-staled.
	if _, err := s.SetCodeEntityHash(ctx, project.ID, "internal/mcp/tools.go", "h3", at); err != nil {
		t.Fatal(err)
	}
	refreshed, unstaled, err := s.RefreshMemoryLinkHashes(ctx, project.ID, e.ID, "h3", []string{mem.ID})
	if err != nil || len(refreshed) != 1 || len(unstaled) != 1 || unstaled[0] != mem.ID {
		t.Fatalf("refresh: %v %v %v", refreshed, unstaled, err)
	}
	if got, _ := s.GetMemoryItem(ctx, mem.ID); got.StaleSince != nil {
		t.Fatal("refresh must un-stale")
	}
	// A link to a file without a known hash keeps a stale memory stale.
	docs, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindFile, "docs/tools.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEntityLink(ctx, &EntityLink{ProjectID: project.ID, EntityID: docs.ID, SrcKind: LinkSrcMemory, SrcID: mem.ID,
		Relation: RelationDecidedAbout, Source: LinkSourceLLM}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE memory_items SET stale_since = now() WHERE id=$1::uuid`, mem.ID); err != nil {
		t.Fatal(err)
	}
	if _, unstaled, err := s.RefreshMemoryLinkHashes(ctx, project.ID, e.ID, "h3", []string{mem.ID}); err != nil || len(unstaled) != 0 {
		t.Fatalf("unhashed link must block un-stale: %v %v", unstaled, err)
	}
	if id, err := s.FindAgentSessionByNativeID(ctx, project.ID, name); err != nil || id != sess.ID {
		t.Fatalf("native: %q %v", id, err)
	}
}

func TestSafeToUnstale(t *testing.T) {
	ok := linkHashState{Path: "internal/a.go", Observed: "h", Current: "h"}
	cases := []struct {
		name  string
		files []string
		links []linkHashState
		want  bool
	}{
		{"all hashed and equal, files covered", []string{`D:\repo\internal\a.go`}, []linkHashState{ok}, true},
		{"no links", nil, nil, false},
		{"observed missing", nil, []linkHashState{ok, {Path: "b.go", Current: "x"}}, false},
		{"current missing", nil, []linkHashState{ok, {Path: "b.go", Observed: "x"}}, false},
		{"mismatch", nil, []linkHashState{{Path: "a.go", Observed: "x", Current: "y"}}, false},
		{"files_affected path without a link", []string{"internal/a.go", "docs/b.md"}, []linkHashState{ok}, false},
	}
	for _, c := range cases {
		if got := safeToUnstale(c.files, c.links); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
