package store

// Tests for the episodic engine extras (issue #11): exact pattern search,
// file search, timeline ordering, cosine semantics, auto-detection, XML shape.
// All on MemStore — no database required.

import (
	"context"
	"strings"
	"testing"
)

func mustEpisode(t *testing.T, ctx context.Context, s *MemStore, proj string, title string, patterns, files []string, emb []float32) *Episode {
	t.Helper()
	ep := &Episode{
		ProjectID:     proj,
		Title:         title,
		EpisodeType:   "bug_fix",
		Trigger:       "trigger for " + title,
		Investigation: "read code",
		RootCause:     "root cause of " + title,
		ErrorPatterns: patterns,
		FilesInvolved: files,
		Embedding:     emb,
	}
	if err := s.CreateEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	return ep
}

func appendEvent(t *testing.T, ctx context.Context, s *MemStore, proj, typ string, payload map[string]any) *Event {
	t.Helper()
	ev := &Event{ProjectID: proj, EventType: typ, Payload: payload}
	if err := s.AppendEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestSearchByErrorPatternExact(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	mustEpisode(t, ctx, s, "p1", "pool timeout", []string{"connection refused"}, []string{"db.go"}, nil)
	mustEpisode(t, ctx, s, "p1", "auth bug", []string{"unauthorized"}, []string{"auth.go"}, nil)

	got, err := s.SearchEpisodesByErrorPattern(ctx, "p1", "connection refused")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "pool timeout" {
		t.Fatalf("exact match: got %+v, want [pool timeout]", got)
	}

	// Substring must NOT match — that is SearchEpisodes' (ILIKE) job.
	got, err = s.SearchEpisodesByErrorPattern(ctx, "p1", "connection")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("substring must not exact-match: got %d", len(got))
	}

	// Project isolation.
	got, _ = s.SearchEpisodesByErrorPattern(ctx, "other", "connection refused")
	if len(got) != 0 {
		t.Fatalf("project leak: got %d", len(got))
	}
}

func TestSearchByFile(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	mustEpisode(t, ctx, s, "p1", "ws fix", nil, []string{"internal/server/ws.go", "internal/store/db.go"}, nil)
	mustEpisode(t, ctx, s, "p1", "unrelated", nil, []string{"main.go"}, nil)

	got, err := s.SearchEpisodesByFile(ctx, "p1", "internal/store/db.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "ws fix" {
		t.Fatalf("file search: got %+v, want [ws fix]", got)
	}

	// Partial path must NOT match (exact ANY semantics).
	got, _ = s.SearchEpisodesByFile(ctx, "p1", "db.go")
	if len(got) != 0 {
		t.Fatalf("partial path must not match: got %d", len(got))
	}
}

func TestLinkAndTimelineOrdering(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	ep := mustEpisode(t, ctx, s, "p1", "timeline ep", nil, nil, nil)

	e1 := appendEvent(t, ctx, s, "p1", "COMMAND_EXECUTED", map[string]any{"command": "go test", "exit_code": 1})
	e2 := appendEvent(t, ctx, s, "p1", "FILE_READ", map[string]any{"path": "a.go"})
	e3 := appendEvent(t, ctx, s, "p1", "COMMAND_EXECUTED", map[string]any{"command": "go test", "exit_code": 0})

	// Link out of order — timeline must still come back oldest-first.
	for _, link := range []struct {
		ev   *Event
		role string
	}{{e3, EpisodeRoleVerification}, {e1, EpisodeRoleTrigger}, {e2, EpisodeRoleInvestigation}} {
		if _, err := s.LinkEpisodeEvent(ctx, ep.ID, link.ev.ID, link.role, ""); err != nil {
			t.Fatal(err)
		}
	}

	tl, err := s.GetEpisodeTimeline(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl) != 3 || tl[0].ID != e1.ID || tl[1].ID != e2.ID || tl[2].ID != e3.ID {
		t.Fatalf("timeline out of order: %+v", tl)
	}
	if tl[0].Payload[payloadEpisodeRole] != EpisodeRoleTrigger {
		t.Errorf("role not stamped on event payload: %+v", tl[0].Payload)
	}

	// Guards.
	if _, err := s.LinkEpisodeEvent(ctx, ep.ID, e1.ID, "bogus", ""); err == nil {
		t.Error("invalid role must fail")
	}
	if _, err := s.LinkEpisodeEvent(ctx, "ep_missing", e1.ID, EpisodeRoleTrigger, ""); err != ErrNotFound {
		t.Errorf("missing episode: got %v, want ErrNotFound", err)
	}
	if _, err := s.LinkEpisodeEvent(ctx, ep.ID, 99999, EpisodeRoleTrigger, ""); err != ErrNotFound {
		t.Errorf("missing event: got %v, want ErrNotFound", err)
	}
	if _, err := s.GetEpisodeTimeline(ctx, "ep_missing"); err != ErrNotFound {
		t.Errorf("missing timeline: got %v, want ErrNotFound", err)
	}
}

func TestCosineSimilarity(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0}, []float32{1, 0}); got < 0.999 {
		t.Errorf("identical = %v, want ~1", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal = %v, want 0", got)
	}
	if got := CosineSimilarity(nil, []float32{1}); got != 0 {
		t.Errorf("nil = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{1, 2}, []float32{1}); got != 0 {
		t.Errorf("dim mismatch = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero norm = %v, want 0 (never NaN)", got)
	}
}

func TestSearchSemanticRanking(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	// Full-width sparse vectors (issue #105: schema is vector(1536)).
	// Sparse so unrelated episodes score exactly 0 and stay filtered.
	xVec := make([]float32, EmbeddingDim)
	xVec[0] = 1
	yVec := make([]float32, EmbeddingDim)
	yVec[1] = 1
	mustEpisode(t, ctx, s, "p1", "x-match", nil, nil, xVec)
	mustEpisode(t, ctx, s, "p1", "y-other", nil, nil, yVec)
	mustEpisode(t, ctx, s, "p1", "no-embedding", nil, nil, nil)

	got, err := s.SearchEpisodesSemantic(ctx, "p1", xVec, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "x-match" {
		t.Fatalf("semantic ranking: got %+v, want [x-match]", got)
	}

	// Limit respected.
	xVec2 := make([]float32, EmbeddingDim)
	xVec2[0] = 1
	xVec2[1] = 0.1
	mustEpisode(t, ctx, s, "p1", "x-match-2", nil, nil, xVec2)
	got, _ = s.SearchEpisodesSemantic(ctx, "p1", xVec, 1)
	if len(got) != 1 {
		t.Fatalf("limit: got %d, want 1", len(got))
	}
}

func TestAutoDetectEpisode(t *testing.T) {
	window := []*Event{
		{ID: 1, ProjectID: "p1", EventType: "COMMAND_EXECUTED",
			Payload: map[string]any{"command": "go test ./...", "exit_code": 1, "stderr": "FAIL: TestAuth\nconnection refused on :5432"}},
		{ID: 2, ProjectID: "p1", EventType: "FILE_READ", Payload: map[string]any{"path": "internal/store/db.go"}},
		{ID: 3, ProjectID: "p1", EventType: "FILE_READ", Payload: map[string]any{"path": "internal/server/ws.go"}},
		{ID: 4, ProjectID: "p1", EventType: "FILE_MODIFIED", Payload: map[string]any{"path": "internal/store/db.go"}},
		{ID: 5, ProjectID: "p1", EventType: "COMMAND_EXECUTED",
			Payload: map[string]any{"command": "go test ./...", "exit_code": 0}},
		{ID: 6, ProjectID: "p1", EventType: "GIT_COMMITTED",
			Payload: map[string]any{"sha": "abc123", "message": "fix pool timeout"}},
	}

	draft, ok := AutoDetectEpisode(window, "p1")
	if !ok || draft == nil {
		t.Fatal("expected a draft episode")
	}
	if draft.EpisodeType != "bug_fix" || draft.Status != "OPEN" {
		t.Errorf("draft identity: %+v", draft)
	}
	if !strings.Contains(draft.Trigger, "go test") {
		t.Errorf("trigger missing command: %q", draft.Trigger)
	}
	if !strings.Contains(draft.Investigation, "internal/store/db.go") {
		t.Errorf("investigation missing reads: %q", draft.Investigation)
	}
	if len(draft.FilesInvolved) != 1 || draft.FilesInvolved[0] != "internal/store/db.go" {
		t.Errorf("files_involved: %+v, want [internal/store/db.go]", draft.FilesInvolved)
	}
	if len(draft.ErrorPatterns) == 0 {
		t.Error("error_patterns must be extracted from stderr")
	}
	if !strings.Contains(draft.Verification, "go test") {
		t.Errorf("verification missing passing run: %q", draft.Verification)
	}

	// No failure -> no episode.
	clean := []*Event{{ID: 1, ProjectID: "p1", EventType: "COMMAND_EXECUTED",
		Payload: map[string]any{"command": "go test", "exit_code": 0}}}
	if _, ok := AutoDetectEpisode(clean, "p1"); ok {
		t.Error("clean window must not produce a draft")
	}
	if _, ok := AutoDetectEpisode(nil, "p1"); ok {
		t.Error("empty window must not produce a draft")
	}
}

func TestEpisodeToXMLShape(t *testing.T) {
	ep := &Episode{
		ID: "ep_1", Title: "Auth timeout <urgent> & fix",
		EpisodeType: "bug_fix", Status: "RESOLVED",
		Trigger: "ConnectionTimeout in ws.go:142", Investigation: "read pool config",
		RootCause: "lifetime too short", Resolution: "bumped to 30m",
		Verification:  "load test green",
		FilesInvolved: []string{"internal/store/db.go"},
	}
	out := ep.ToXML()
	for _, want := range []string{
		`<episode type="bug_fix"`, `title=`, `status="RESOLVED"`,
		"<trigger", "<investigation>", "<root_cause>", "<resolution",
		"<verification>", `<events count="0">`, "</episode>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("XML missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<urgent>") {
		t.Errorf("XML must escape title:\n%s", out)
	}
	if !strings.Contains(out, `files="internal/store/db.go"`) {
		t.Errorf("XML missing files attr:\n%s", out)
	}
}
