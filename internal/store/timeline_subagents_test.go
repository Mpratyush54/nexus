package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestIsSubagentRun(t *testing.T) {
	cases := []struct {
		native, title, parent string
		want                  bool
	}{
		{"agent-a1b2c3d4e5f6a7b8c", "", "", true},
		{"AGENT-A1B2C3D4", "", "", true},
		{"0f8e2c1a-6d2b-4f6e-9a51-3b7c2d1e0f9a", "", "", false},
		{"agent-short", "", "", false},
		{"agent-a1b2", "", "", false}, // under 8 hex digits
		{"ses_abc", "Explore the repo (@general subagent)", "", true},
		{"ses_abc", "Review (sub-agent)", "", true},
		{"ses_abc", "How do subagents work?", "", false},
		{"run-subagent-7", "", "", true},
		{"C:/x/subagent/y", "", "", false}, // a path is never a native-id marker
		{"plain", "", "parent-native", true},
		{"rollout-2026-01-01", "Captured codex session", "", false},
	}
	for _, c := range cases {
		if got := IsSubagentRun(c.native, c.title, c.parent); got != c.want {
			t.Errorf("IsSubagentRun(%q, %q, %q) = %v, want %v", c.native, c.title, c.parent, got, c.want)
		}
	}
}

// subagentTimelineScenario: the newest rows are sub-agent runs (as on the
// account that reported it). The default timeline pages through the three
// real chats only; include_subagents lists every row and links the runs to
// their parent; a later upsert backfills a missing parent link.
func subagentTimelineScenario(t *testing.T, s AgentCloudStore, project, owner, machine string, setActive func(id string, at time.Time)) {
	t.Helper()
	ctx := t.Context()
	upsert := func(harness, native, title, parent string) *AgentSession {
		t.Helper()
		row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project, OwnerUserID: owner, Harness: harness,
			NativeID: native, OriginMachineID: machine, Title: title, ParentNativeID: parent})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	parentNative := "0f8e2c1a-6d2b-4f6e-9a51-3b7c2d1e0f9a"
	// One run is captured before its parent: the link resolves at read time.
	early := upsert("claude", "agent-00000000000000001", "", parentNative)
	parent := upsert("claude", parentNative, "Fix the build", "")
	other := upsert("claude", "5b1f0e7d-2a3c-4d5e-8f90-1a2b3c4d5e6f", "Write the docs", "")
	third := upsert("codex", "rollout-third", "Plan the release", "")
	linked := []*AgentSession{early,
		upsert("claude", "agent-00000000000000002", "", parentNative),
		upsert("claude", "agent-00000000000000003", "", parentNative)}
	old := upsert("claude", "agent-00000000000000004", "", "") // captured before parent links
	titled := upsert("opencode", "ses_sub", "Explore the repo (@general subagent)", "")

	// Turns first: appending one can move last_active_at.
	for _, row := range []*AgentSession{parent, other, third, old} {
		if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: row.ID, Idx: 1, Role: "user", TextPreview: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	order := []*AgentSession{linked[0], linked[1], linked[2], old, titled, parent, other, third}
	for i, row := range order { // newest first
		setActive(row.ID, base.Add(-time.Duration(i)*time.Minute))
	}

	q := TimelineQuery{UserID: owner, ProjectID: project, Limit: 2}
	page1, next, err := s.ListTimeline(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].SessionID != parent.ID || page1[1].SessionID != other.ID || next == "" {
		t.Fatalf("page 1 = %+v next=%q", page1, next)
	}
	if page1[0].SubagentCount != 3 || page1[0].LineageKind != "" || page1[0].ParentSessionID != "" {
		t.Fatalf("parent card = %+v", page1[0])
	}
	q.Cursor = next
	page2, next2, err := s.ListTimeline(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].SessionID != third.ID || next2 != "" {
		t.Fatalf("page 2 = %+v next=%q", page2, next2)
	}

	all, _, err := s.ListTimeline(ctx, TimelineQuery{UserID: owner, ProjectID: project, Limit: 100, IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(order) {
		t.Fatalf("include_subagents listed %d rows, want %d", len(all), len(order))
	}
	byID := map[string]TimelineItem{}
	for _, it := range all {
		byID[it.SessionID] = it
	}
	for i, row := range order {
		if all[i].SessionID != row.ID {
			t.Fatalf("row %d = %s, want %s", i, all[i].SessionID, row.ID)
		}
	}
	for _, row := range linked {
		if it := byID[row.ID]; it.LineageKind != "subagent" || it.ParentSessionID != parent.ID {
			t.Fatalf("linked run = %+v", it)
		}
	}
	if it := byID[old.ID]; it.LineageKind != "subagent" || it.ParentSessionID != "" {
		t.Fatalf("unlinked run = %+v", it)
	}
	if it := byID[titled.ID]; it.LineageKind != "subagent" {
		t.Fatalf("titled run = %+v", it)
	}

	// Lazy backfill: the next mirror of the old run sends its parent; a
	// later upsert without one keeps it.
	upsert("claude", old.NativeID, "", parentNative)
	upsert("claude", old.NativeID, "", "")
	all, _, err = s.ListTimeline(ctx, TimelineQuery{UserID: owner, ProjectID: project, Limit: 100, IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range all {
		if it.SessionID == old.ID && it.ParentSessionID != parent.ID {
			t.Fatalf("backfilled run = %+v", it)
		}
		if it.SessionID == parent.ID && it.SubagentCount != 4 {
			t.Fatalf("parent after backfill = %+v", it)
		}
	}
}

func TestMemTimelineSubagents(t *testing.T) {
	m := NewMemStore()
	subagentTimelineScenario(t, m, "p1", "owner", "m1", func(id string, at time.Time) {
		m.mu.Lock()
		m.agentSessions[id].LastActiveAt = at
		m.mu.Unlock()
	})
}

func TestPostgresTimelineSubagents(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(ctx, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var owner string
	name := fmt.Sprintf("subagent-runs-%d", time.Now().UnixNano())
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID) }()
	subagentTimelineScenario(t, s, project.ID, owner, name, func(id string, at time.Time) {
		if _, err := s.pool.Exec(ctx, `UPDATE agent_sessions SET last_active_at=$2 WHERE id=$1::uuid`, id, at); err != nil {
			t.Fatal(err)
		}
	})
}
