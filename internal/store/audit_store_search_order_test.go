package store

// Audit: SearchMemory / SearchEpisodes filtering, ordering, limits (MemStore).
//
// Postgres contracts under test:
//   - SearchMemory: tag filter `tags && $3`, ORDER BY confidence DESC,
//     created_at DESC, default limit 20.
//   - SearchEpisodes: ORDER BY opened_at DESC, default limit 20.
// MemStore implements none of the tag filtering, ordering, or limit
// defaults (map iteration order = nondeterministic results).

import (
	"context"
	"testing"
	"time"
)

func auditMemItem(project, key, content string, tags []string, conf float32, created time.Time) *MemoryItem {
	return &MemoryItem{
		ProjectID: project, Key: key, Content: content,
		Tags: tags, Confidence: conf, Status: StatusConfirmed,
		Level: LevelProject, Scope: "fact", CreatedAt: created,
	}
}

// FIXED(#110): the tags parameter filters (Postgres `tags && $3` parity) —
// only rows sharing a tag match.
func TestAuditSearchMemoryTagFilterRegression(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for _, m := range []*MemoryItem{
		auditMemItem("p1", "alpha-key", "alpha content about caching here", []string{"alpha"}, 0.9, now),
		auditMemItem("p1", "beta-key", "beta content about retries here", []string{"beta"}, 0.9, now),
	} {
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", []string{"alpha"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Key != "alpha-key" {
		t.Fatalf("tag filter: got %+v, want only [alpha-key]", res)
	}
}

func TestAuditSearchMemoryEmptyTagsMeansAll(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for _, m := range []*MemoryItem{
		auditMemItem("p1", "k1", "first content here with length", []string{"a"}, 0.5, now),
		auditMemItem("p1", "k2", "second content here with length", nil, 0.5, now),
	} {
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Errorf("got %d results, want 2 (empty tags = no filter)", len(res))
	}
}

// FIXED(#110): ORDER BY confidence DESC, created_at DESC like Postgres —
// result order is deterministic, not Go map iteration order.
func TestAuditSearchMemoryOrdering(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	base := time.Now().UTC()
	// Distinct confidences make the expected order unambiguous without
	// sleeping between writes.
	confs := map[string]float32{"c-high": 0.9, "c-mid2": 0.7, "c-mid1": 0.5, "c-low": 0.2}
	keys := []string{"c-low", "c-mid1", "c-mid2", "c-high"} // insert shuffled
	for i, key := range keys {
		m := auditMemItem("p1", key, "ordering content for "+key+" here", nil, confs[key], base.Add(time.Duration(i)*time.Second))
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"c-high", "c-mid2", "c-mid1", "c-low"}
	for run := 0; run < 10; run++ {
		res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != len(want) {
			t.Fatalf("got %d results, want %d", len(res), len(want))
		}
		for i, key := range want {
			if res[i].Key != key {
				t.Fatalf("run %d: position %d = %q, want %q (confidence DESC)", run, i, res[i].Key, key)
			}
		}
	}
}

func TestAuditSearchMemoryStatusVisibility(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	visible := map[string]bool{StatusConfirmed: true, StatusProposed: true}
	for _, st := range []string{StatusProposed, StatusConfirmed, StatusRejected, StatusSuperseded} {
		m := auditMemItem("p1", "k-"+st, "visibility content here ok", nil, 0.5, now)
		m.Status = st
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2 (only CONFIRMED/PROPOSED visible)", len(res))
	}
	for _, m := range res {
		if !visible[m.Status] {
			t.Errorf("status %q should be hidden from search", m.Status)
		}
	}
}

func TestAuditSearchMemoryOrgLevelVisible(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	m := auditMemItem("", "org-key", "org level content here ok", nil, 0.8, time.Now().UTC())
	m.Level = LevelOrganization
	if err := s.CreateMemoryItem(ctx, m); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchMemory(ctx, "any-project", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Key != "org-key" {
		t.Errorf("org-level (NULL project) memory should be visible to every project, got %+v", res)
	}
}

// FIXED(#102): a NULL-project row that is NOT organization-level stays
// invisible — personal/session rows never leak globally.
func TestAuditSearchMemoryNonOrgNullHidden(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	leak := auditMemItem("", "leak-key", "project null content here ok", nil, 0.8, now)
	leak.Level = LevelProject // NULL project but claims project scope
	if err := s.CreateMemoryItem(ctx, leak); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchMemory(ctx, "any-project", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res {
		if m.Key == "leak-key" {
			t.Errorf("non-organization NULL-project row leaked globally: %+v", res)
		}
	}
}

func TestAuditSearchMemoryProjectIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	if err := s.CreateMemoryItem(ctx, auditMemItem("pa", "k-a", "project a content here ok", nil, 0.5, now)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMemoryItem(ctx, auditMemItem("pb", "k-b", "project b content here ok", nil, 0.5, now)); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchMemory(ctx, "pa", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Key != "k-a" {
		t.Errorf("project isolation broken, got %+v", res)
	}
}

// FIXED(#110): MemStore clamps limit<=0 to the 20-row default like
// Postgres instead of treating 0 as uncapped.
func TestAuditSearchMemoryLimitDefault(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		m := auditMemItem("p1", "bulk-key", "bulk content number here ok", nil, 0.5, now)
		m.Key = "bulk-key-" + string(rune('a'+i/10)) + string(rune('0'+i%10))
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 20 {
		t.Errorf("limit<=0 must default to 20 rows: got %d", len(res))
	}
}

func TestAuditSearchMemoryLimitHonored(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for _, key := range []string{"l1", "l2", "l3"} {
		if err := s.CreateMemoryItem(ctx, auditMemItem("p1", key, "limit content here with length", nil, 0.5, now)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Errorf("got %d results, want limit cap of 2", len(res))
	}
}

// FIXED(#110): SearchEpisodes orders by opened_at DESC (id tie-break) like
// Postgres — newest first, deterministic.
func TestAuditSearchEpisodesOrdering(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	titles := []string{"oldest ep", "middle ep", "newest ep"}
	for _, title := range titles {
		ep := &Episode{ProjectID: "p1", Title: title, EpisodeType: "bug_fix"}
		if err := s.CreateEpisode(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"newest ep", "middle ep", "oldest ep"}
	for run := 0; run < 10; run++ {
		res, err := s.SearchEpisodes(ctx, "p1", "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 3 {
			t.Fatalf("got %d episodes, want 3", len(res))
		}
		for i, title := range want {
			if res[i].Title != title {
				t.Fatalf("run %d: position %d = %q, want %q (opened_at DESC)", run, i, res[i].Title, title)
			}
		}
	}
}

// FIXED(#110): same default-limit-20 as SearchMemory.
func TestAuditSearchEpisodesLimitDefault(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	for i := 0; i < 25; i++ {
		ep := &Episode{ProjectID: "p1", Title: "bulk episode content", EpisodeType: "feature"}
		if err := s.CreateEpisode(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchEpisodes(ctx, "p1", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 20 {
		t.Errorf("limit<=0 must default to 20 rows: got %d", len(res))
	}
}

func TestAuditSearchEpisodesEmptyFiltersList(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	for _, title := range []string{"ep one", "ep two"} {
		if err := s.CreateEpisode(ctx, &Episode{ProjectID: "p1", Title: title, EpisodeType: "feature"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateEpisode(ctx, &Episode{ProjectID: "other", Title: "ep other", EpisodeType: "feature"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchEpisodes(ctx, "p1", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Errorf("empty filters should list all project episodes, got %d", len(res))
	}
}
