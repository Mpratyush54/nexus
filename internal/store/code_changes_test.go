package store

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func chg(id, hash string, at time.Time, start, removed, added int) CodeChangeHit {
	end := start + added - 1
	if end < start {
		end = start
	}
	return CodeChangeHit{CodeChange: CodeChange{ID: id, EntityID: "e1", SessionID: "s", ToolCallID: id,
		StartLine: start, EndLine: end, RemovedLines: removed, AddedLines: added, AfterHash: hash,
		ChangedAt: at, ObservedAt: at}, Path: "a.go"}
}

func placedByID(p []PlacedCodeChange) map[string]PlacedCodeChange {
	out := map[string]PlacedCodeChange{}
	for _, x := range p {
		out[x.ID] = x
	}
	return out
}

func TestPlaceCodeChangesShiftsOlderRanges(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	hits := []CodeChangeHit{
		chg("x", "a", t0, 10, 1, 3), // lines 10-12
		// Next report: 2 lines inserted at 5, and lines 20-22 (old 18-20)
		// collapsed to one line below x.
		chg("y", "b", t0.Add(time.Minute), 5, 0, 2),
		chg("w", "b", t0.Add(time.Minute), 20, 3, 1),
	}
	for i := range hits {
		hits[i].CurrentHash = "b"
	}
	got := placedByID(PlaceCodeChanges(hits))
	if x := got["x"]; x.CurStart != 12 || x.CurEnd != 14 || x.Approximate || x.Overwritten {
		t.Fatalf("x after insert above: %+v", x)
	}
	if y := got["y"]; y.CurStart != 5 || y.CurEnd != 6 || y.Approximate {
		t.Fatalf("y: %+v", y)
	}

	// A third report rewrites line 13 (inside x) and deletes lines 1-2.
	hits = append(hits,
		chg("z", "c", t0.Add(2*time.Minute), 11, 1, 1), // old 13 after the deletion above
		chg("d", "c", t0.Add(2*time.Minute), 1, 2, 0),
	)
	for i := range hits {
		hits[i].CurrentHash = "c"
	}
	got = placedByID(PlaceCodeChanges(hits))
	if x := got["x"]; x.CurStart != 10 || x.CurEnd != 12 || !x.Overwritten || !x.Approximate {
		t.Fatalf("x after rewrite inside: %+v", x)
	}
	if y := got["y"]; y.CurStart != 3 || y.CurEnd != 4 || y.Approximate {
		t.Fatalf("y after delete above: %+v", y)
	}
	if w := got["w"]; w.CurStart != 18 || w.Approximate {
		t.Fatalf("w: %+v", w)
	}
	if z := got["z"]; z.CurStart != 11 || z.CurEnd != 11 || z.Approximate {
		t.Fatalf("z: %+v", z)
	}

	// The file changed outside captured edits: everything is approximate.
	for i := range hits {
		hits[i].CurrentHash = "outside"
	}
	for _, p := range PlaceCodeChanges(hits) {
		if !p.Approximate {
			t.Fatalf("outside change must make %s approximate", p.ID)
		}
	}
	// An unhashed file's ranges are approximate.
	un := chg("u", "", t0, 3, 1, 1)
	if p := PlaceCodeChanges([]CodeChangeHit{un}); len(p) != 1 || !p[0].Approximate || p[0].CurStart != 3 {
		t.Fatalf("unhashed: %+v", p)
	}
}

func TestClipSnippet(t *testing.T) {
	long := strings.Repeat("é", 200)
	got := ClipSnippet("a\r\nb\nc\nd\n\n")
	if got != "a\nb\nc" {
		t.Fatalf("lines: %q", got)
	}
	if got := ClipSnippet(long); len(got) > maxSnippetLineBytes+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("long line: %d", len(got))
	}
}

func TestMemStoreCodeChanges(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	pid, sid, _, _ := codeLinkFixture(t, s)
	e, err := s.UpsertCodeEntity(ctx, pid, CodeKindFile, "internal/mcp/tools.go", "")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	in := []*CodeChange{
		{EntityID: e.ID, SessionID: sid, ToolCallID: "toolu_1", StartLine: 4, EndLine: 5, RemovedLines: 1, AddedLines: 2,
			BeforeSnippet: "old", AfterSnippet: "new1\nnew2\nnew3\nnew4", AfterHash: "sha256:H1", ChangedAt: at},
		{EntityID: e.ID, SessionID: sid, StartLine: 0, EndLine: 1},                // bad range
		{EntityID: "cent_nope", SessionID: sid, StartLine: 1, EndLine: 1},         // unknown entity
		{EntityID: e.ID, SessionID: "nope", StartLine: 1, EndLine: 1},             // unknown session
		{EntityID: e.ID, SessionID: sid, StartLine: 9, EndLine: 9, AddedLines: 1}, // no tool call id
	}
	if n, err := s.AddCodeChanges(ctx, pid, in); err != nil || n != 2 {
		t.Fatalf("add: %d %v", n, err)
	}
	if in[0].AfterSnippet != "new1\nnew2\nnew3" || in[0].AfterHash != "h1" || !strings.HasPrefix(in[4].ToolCallID, "h:") {
		t.Fatalf("normalised: %+v / %+v", in[0], in[4])
	}
	// A resend with the same hash keeps observed_at; a new hash moves it.
	first := in[0].ObservedAt
	again := []*CodeChange{{EntityID: e.ID, SessionID: sid, ToolCallID: "toolu_1", StartLine: 4, EndLine: 5, RemovedLines: 1, AddedLines: 2, AfterHash: "h1", ChangedAt: at}}
	if n, _ := s.AddCodeChanges(ctx, pid, again); n != 1 || !again[0].ObservedAt.Equal(first) {
		t.Fatalf("resend: %+v", again[0])
	}
	hits, err := s.ListCodeChangesByPath(ctx, pid, "mcp/tools.go")
	if err != nil || len(hits) != 2 || hits[0].Session == nil || hits[0].Session.Title != "Add links" || hits[0].Path != "internal/mcp/tools.go" {
		t.Fatalf("list: %+v %v", hits, err)
	}
	if other, _ := s.ListCodeChangesByPath(ctx, pid, "ools.go"); len(other) != 0 {
		t.Fatalf("suffix must stop at a segment: %+v", other)
	}
}

func TestPostgresCodeChanges(t *testing.T) {
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
	name := fmt.Sprintf("codechanges-%d", time.Now().UnixNano())
	var owner string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID) }()
	other, err := s.ResolveProject(ctx, "", "", name+"-other")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, other.ID) }()
	sess, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: "claude", NativeID: name, Title: "Shift lines"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: other.ID, OwnerUserID: owner, Harness: "codex", NativeID: name + "-f"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.UpsertCodeEntity(ctx, project.ID, CodeKindFile, "internal/server/routes.go", "")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).UTC()
	anchors := struct{ added, before, after []string }{
		added: hashes("b", "c", "d"), before: hashes("x", "y"), after: hashes("}"),
	}
	in := []*CodeChange{
		{EntityID: e.ID, SessionID: sess.ID, ToolCallID: "call_1", HunkIndex: 0, StartLine: 10, EndLine: 12, RemovedLines: 1, AddedLines: 3,
			BeforeSnippet: "a", AfterSnippet: "b\nc\nd", AfterHash: "h1", ChangedAt: at,
			AddedHashes: anchors.added, ContextBefore: anchors.before, ContextAfter: anchors.after},
		{EntityID: e.ID, SessionID: foreign.ID, ToolCallID: "call_x", StartLine: 1, EndLine: 1, AddedLines: 1, ChangedAt: at}, // other project's session
	}
	if n, err := s.AddCodeChanges(ctx, project.ID, in); err != nil || n != 1 || in[0].ID == "" {
		t.Fatalf("add: %d %v %+v", n, err, in[0])
	}
	first := in[0].ObservedAt
	same := []*CodeChange{{EntityID: e.ID, SessionID: sess.ID, ToolCallID: "call_1", StartLine: 10, EndLine: 12, RemovedLines: 1, AddedLines: 3, AfterHash: "h1", ChangedAt: at,
		AddedHashes: anchors.added, ContextBefore: anchors.before, ContextAfter: anchors.after}}
	if n, err := s.AddCodeChanges(ctx, project.ID, same); err != nil || n != 1 || same[0].ID != in[0].ID || !same[0].ObservedAt.Equal(first) {
		t.Fatalf("idempotent resend: %d %v %+v", n, err, same[0])
	}
	time.Sleep(5 * time.Millisecond)
	later := []*CodeChange{{EntityID: e.ID, SessionID: sess.ID, ToolCallID: "call_2", StartLine: 2, EndLine: 3, AddedLines: 2, AfterHash: "h2", ChangedAt: at.Add(time.Minute)}}
	if n, err := s.AddCodeChanges(ctx, project.ID, later); err != nil || n != 1 {
		t.Fatalf("later: %d %v", n, err)
	}
	if _, err := s.SetCodeEntityHash(ctx, project.ID, e.Path, "h2", time.Now()); err != nil {
		t.Fatal(err)
	}
	hits, err := s.ListCodeChangesByPath(ctx, project.ID, "routes.go")
	if err != nil || len(hits) != 2 || hits[0].ToolCallID != "call_1" || hits[0].Session == nil || hits[0].Session.Title != "Shift lines" || hits[1].CurrentHash != "h2" {
		t.Fatalf("list: %+v %v", hits, err)
	}
	got := placedByID(PlaceCodeChanges(hits))
	if x := got[in[0].ID]; x.CurStart != 12 || x.CurEnd != 14 || x.Approximate {
		t.Fatalf("placed: %+v", x)
	}
	// The text[] anchors round-trip; a hunk without them reads back empty.
	if !reflect.DeepEqual(hits[0].AddedHashes, anchors.added) || !reflect.DeepEqual(hits[0].ContextBefore, anchors.before) ||
		!reflect.DeepEqual(hits[0].ContextAfter, anchors.after) {
		t.Fatalf("anchors: %+v", hits[0].CodeChange)
	}
	if len(hits[1].AddedHashes) != 0 || hits[1].ContextBefore == nil {
		t.Fatalf("empty anchors: %+v", hits[1].CodeChange)
	}
	if r, ok := hits[0].MatchProbes(ProbesForLines([]string{"c"}, strp("b"), strp("d"))); !ok || r != MatchExact {
		t.Fatalf("match from Postgres row: %d %v", r, ok)
	}
}
