package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"central-memory/internal/usage"
)

// exerciseUsageStore runs the same contract against MemStore and Postgres.
// owner owns s1 (claude) and s2 (codex, project B); other owns s3, shared
// with owner by grant; s4 belongs to other and is private.
func exerciseUsageStore(t *testing.T, st UsageStore, owner, s1, s2, s3, s4, projectA string) {
	t.Helper()
	ctx := t.Context()
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2).Add(10 * time.Hour)
	at := func(h int) *time.Time { v := day.Add(time.Duration(h) * time.Hour); return &v }
	idx := int64(7)
	recs := []SessionUsage{
		// One Claude API call repeated over several transcript lines.
		{SessionID: s1, Key: "claude:msg_1", TurnIdx: &idx, SourceAt: at(0), Entry: usage.Entry{Model: "claude-opus-5", Input: 1000, Output: 100, CacheRead: 5000}},
		{SessionID: s1, Key: "claude:msg_1", SourceAt: at(0), Entry: usage.Entry{Model: "claude-opus-5", Input: 1000, Output: 200, CacheRead: 5000}},
		{SessionID: s1, Key: "live:1", SourceAt: at(1), Entry: usage.Entry{Model: "claude-opus-5", Input: 10, Output: 10, CostUSD: usage.Cost(0.5)}},
		{SessionID: s2, Key: "codex-total:42", SourceAt: at(30), Entry: usage.Entry{Model: "gpt-6.1-sol", Input: 40, Output: 2}},
		{SessionID: s3, Key: "opencode:m1", SourceAt: at(2), Entry: usage.Entry{Model: "big-pickle", Input: 7, Output: 3}},
		{SessionID: s4, Key: "claude:secret", SourceAt: at(2), Entry: usage.Entry{Model: "claude-opus-5", Input: 999999}},
	}
	if err := st.RecordSessionUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSessionUsage(ctx, []SessionUsage{{SessionID: s1, Key: "bad", Entry: usage.Entry{Input: -1}}}); err == nil {
		t.Fatal("negative tokens must be rejected")
	}

	byModel, err := st.SessionUsageByModel(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	if len(byModel) != 1 || byModel[0].Model != "claude-opus-5" || byModel[0].Input != 1010 || byModel[0].Output != 210 || byModel[0].Turns != 2 {
		t.Fatalf("session by model (dedup by key, keep larger counts): %+v", byModel)
	}
	wantEst := (1000*5.0 + 200*25.0 + 5000*0.5) / 1e6
	if math.Abs(byModel[0].EstimatedUSD-wantEst) > 1e-9 || byModel[0].ReportedUSD != 0.5 || !byModel[0].Estimated {
		t.Fatalf("reported cost must be kept and only the rest estimated: %+v want est %v", byModel[0], wantEst)
	}

	readable, err := st.UsageSummary(ctx, UsageQuery{UserID: owner, GroupBy: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]usage.Total{}
	for _, r := range readable {
		got[r.Key] = r
	}
	if len(got) != 3 || got["claude"].Input != 1010 || got["codex"].Input != 40 || got["opencode"].Input != 7 {
		t.Fatalf("readable by agent (s4 must be hidden): %+v", readable)
	}
	if got["codex"].HasCost || got["codex"].UnpricedTokens != 42 {
		t.Fatalf("unknown models get tokens only, never a price: %+v", got["codex"])
	}

	mine, err := st.UsageSummary(ctx, UsageQuery{UserID: owner, GroupBy: "model", OwnerOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 {
		t.Fatalf("owner-only by model: %+v", mine)
	}

	inA, err := st.UsageSummary(ctx, UsageQuery{UserID: owner, GroupBy: "project", ProjectID: projectA, OwnerOnly: true})
	if err != nil || len(inA) != 1 || inA[0].Key != projectA || inA[0].Input != 1010 {
		t.Fatalf("project filter: %+v %v", inA, err)
	}

	days, err := st.UsageSummary(ctx, UsageQuery{UserID: owner, GroupBy: "day", OwnerOnly: true, From: day.Add(-time.Hour), To: day.Add(48 * time.Hour), TZOffset: 330})
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Key != day.Add(330*time.Minute).Format("2006-01-02") || days[1].Input != 40 {
		t.Fatalf("day buckets in the caller's zone: %+v", days)
	}

	window, err := st.UsageSummary(ctx, UsageQuery{UserID: owner, OwnerOnly: true, From: day.Add(20 * time.Hour)})
	if err != nil || len(window) != 1 || window[0].Key != "codex" {
		t.Fatalf("from filter: %+v %v", window, err)
	}
	if _, err := st.UsageSummary(ctx, UsageQuery{UserID: owner, GroupBy: "weekday"}); err == nil {
		t.Fatal("unknown group_by must fail")
	}
}

func TestUsageStoreMem(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	pa, err := m.ResolveProject(ctx, "", "", "usage-a")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := m.ResolveProject(ctx, "", "", "usage-b")
	if err != nil {
		t.Fatal(err)
	}
	owner, other := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	mk := func(project, ownerID, harness, native string) string {
		row, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project, OwnerUserID: ownerID, Harness: harness, NativeID: native})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	s1, s2 := mk(pa.ID, owner, "claude", "n1"), mk(pb.ID, owner, "codex", "n2")
	s3, s4 := mk(pa.ID, other, "opencode", "n3"), mk(pa.ID, other, "claude", "n4")
	if err := m.GrantAgentSession(ctx, s3, owner, other); err != nil {
		t.Fatal(err)
	}
	exerciseUsageStore(t, m, owner, s1, s2, s3, s4, pa.ID)
}

func TestUsageStorePostgres(t *testing.T) {
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
	stamp := time.Now().UnixNano()
	user := func(name string) string {
		var id string
		if err := s.pool.QueryRow(t.Context(), `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, id) })
		return id
	}
	owner, other := user(fmt.Sprintf("usage-owner-%d", stamp)), user(fmt.Sprintf("usage-other-%d", stamp))
	project := func(name string) string {
		p, err := s.ResolveProject(t.Context(), "", "", name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, p.ID) })
		return p.ID
	}
	pa, pb := project(fmt.Sprintf("usage-a-%d", stamp)), project(fmt.Sprintf("usage-b-%d", stamp))
	mk := func(projectID, ownerID, harness, native string) string {
		row, err := s.UpsertAgentSession(t.Context(), &AgentSession{ProjectID: projectID, OwnerUserID: ownerID, Harness: harness, NativeID: fmt.Sprintf("%s-%d", native, stamp)})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	s1, s2 := mk(pa, owner, "claude", "n1"), mk(pb, owner, "codex", "n2")
	s3, s4 := mk(pa, other, "opencode", "n3"), mk(pa, other, "claude", "n4")
	if err := s.GrantAgentSession(t.Context(), s3, owner, other); err != nil {
		t.Fatal(err)
	}
	exerciseUsageStore(t, s, owner, s1, s2, s3, s4, pa)
	if err := s.RecordSessionUsage(t.Context(), []SessionUsage{{SessionID: "33333333-3333-4333-8333-333333333333", Key: "k", Entry: usage.Entry{Input: 1}}}); err != ErrNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}
