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

// BUG(#110) (headline regression): the tags parameter is silently ignored —
// MemStore never filters on tags, unlike Postgres `tags && $3`. Regression
// documents current MemStore behavior (no tag filtering).
func TestAuditSearchMemoryTagFilterRegression(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for _, m := range []*MemoryItem{
		auditMemItem("p1", "alpha-key", "alpha content about caching", []string{"alpha"}, 0.9, now),
		auditMemItem("p1", "beta-key", "beta content about retries", []string{"beta"}, 0.9, now),
	} {
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", []string{"alpha"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("MemStore ignores tag filter: got %d results, want 2 (both items)", len(res))
	}
	keys := map[string]bool{}
	for _, m := range res {
		keys[m.Key] = true
	}
	if !keys["alpha-key"] || !keys["beta-key"] {
		t.Errorf("want both alpha-key and beta-key in unfiltered results, got %v", keys)
	}
}

func TestAuditSearchMemoryEmptyTagsMeansAll(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for _, m := range []*MemoryItem{
		auditMemItem("p1", "k1", "first content here", []string{"a"}, 0.5, now),
		auditMemItem("p1", "k2", "second content here", nil, 0.5, now),
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

// BUG(#110): no ORDER BY confidence DESC, created_at DESC — result order is
// Go map iteration order (nondeterministic). Regression documents the
// current MemStore property weakly but deterministically: assert result SET
// membership, not order.
func TestAuditSearchMemoryOrdering(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	base := time.Now().UTC()
	wantKeys := map[string]bool{"c-high": true, "c-mid2": true, "c-mid1": true, "c-low": true}
	confs := map[string]float32{"c-high": 0.9, "c-mid2": 0.7, "c-mid1": 0.5, "c-low": 0.2}
	keys := []string{"c-high", "c-mid2", "c-mid1", "c-low"}
	for i, key := range keys {
		m := auditMemItem("p1", key, "ordering content for "+key, nil, confs[key], base.Add(time.Duration(i)*time.Second))
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		// Pin CreatedAt: MemStore stamps now() on write; override the stored
		// row directly so ties cannot mask an ordering bug.
		stored, _ := s.GetMemoryItem(ctx, m.ID)
		stored.CreatedAt = base.Add(time.Duration(i) * time.Second)
	}
	for run := 0; run < 10; run++ {
		res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != len(wantKeys) {
			t.Fatalf("got %d results, want %d", len(res), len(wantKeys))
		}
		seen := map[string]bool{}
		for _, m := range res {
			seen[m.Key] = true
		}
		for k := range wantKeys {
			if !seen[k] {
				t.Fatalf("run %d: key %q missing from results %+v (want full set; order is nondeterministic map order)", run, k, seen)
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
		m := auditMemItem("p1", "k-"+st, "visibility content here", nil, 0.5, now)
		m.Status = ""
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		stored, _ := s.GetMemoryItem(ctx, m.ID)
		stored.Status = st
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
	m := auditMemItem("", "org-key", "org level content here", nil, 0.8, time.Now().UTC())
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

func TestAuditSearchMemoryProjectIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	if err := s.CreateMemoryItem(ctx, auditMemItem("pa", "k-a", "project a content", nil, 0.5, now)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMemoryItem(ctx, auditMemItem("pb", "k-b", "project b content", nil, 0.5, now)); err != nil {
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

// BUG(#110): Postgres clamps limit<=0 to 20; MemStore treats 0 as uncapped.
// Regression documents current MemStore behavior.
func TestAuditSearchMemoryLimitDefault(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		m := auditMemItem("p1", "bulk-key", "bulk content number here", nil, 0.5, now)
		m.Key = "bulk-key-" + string(rune('a'+i/10)) + string(rune('0'+i%10))
		if err := s.CreateMemoryItem(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.SearchMemory(ctx, "p1", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 25 {
		t.Errorf("MemStore treats limit<=0 as uncapped: got %d rows, want 25", len(res))
	}
}

func TestAuditSearchMemoryLimitHonored(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	now := time.Now().UTC()
	for _, key := range []string{"l1", "l2", "l3"} {
		if err := s.CreateMemoryItem(ctx, auditMemItem("p1", key, "limit content here", nil, 0.5, now)); err != nil {
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

// BUG(#110): SearchEpisodes has no ORDER BY opened_at DESC (map order).
// Regression documents the current MemStore property weakly but
// deterministically: assert result SET membership, not order.
func TestAuditSearchEpisodesOrdering(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	base := time.Now().UTC()
	titles := []string{"oldest ep", "middle ep", "newest ep"}
	for i, title := range titles {
		ep := &Episode{ProjectID: "p1", Title: title, EpisodeType: "bug_fix"}
		if err := s.CreateEpisode(ctx, ep); err != nil {
			t.Fatal(err)
		}
		stored, _ := s.GetEpisode(ctx, ep.ID)
		stored.OpenedAt = base.Add(time.Duration(i) * time.Hour)
	}
	want := map[string]bool{"newest ep": true, "middle ep": true, "oldest ep": true}
	for run := 0; run < 10; run++ {
		res, err := s.SearchEpisodes(ctx, "p1", "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 3 {
			t.Fatalf("got %d episodes, want 3", len(res))
		}
		seen := map[string]bool{}
		for _, ep := range res {
			seen[ep.Title] = true
		}
		for title := range want {
			if !seen[title] {
				t.Fatalf("run %d: title %q missing from results %+v (want full set; order is nondeterministic map order)", run, title, seen)
			}
		}
	}
}

// BUG(#110): same missing default-limit-20 as SearchMemory. Regression
// documents current MemStore behavior (uncapped).
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
	if len(res) != 25 {
		t.Errorf("MemStore treats limit<=0 as uncapped: got %d rows, want 25", len(res))
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
