package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// AgentActivityRollup counts in SQL what ListAgentActivity returned as
// fingerprints: the per-day totals must match exactly, including messages
// captured twice from two machines, undated messages, and conversations
// that also exist as archived transcripts (returned as Twins).
func TestAgentActivityRollupMatchesFingerprintRows(t *testing.T) {
	dsn := testPostgresDSN(t)
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	name := fmt.Sprintf("rollup-%d", time.Now().UnixNano())
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

	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3).Add(20 * time.Hour)
	session := func(native, machine string) string {
		t.Helper()
		row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: "codex", NativeID: native, OriginMachineID: machine})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	turn := func(session string, idx int, role, text string, at *time.Time) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_turns(session_id, idx, role, text_preview, source_ts) VALUES ($1::uuid, $2, $3, $4, $5)`,
			session, idx, role, text, at); err != nil {
			t.Fatal(err)
		}
	}
	at := func(minutes int) *time.Time { v := day.Add(time.Duration(minutes) * time.Minute); return &v }
	// One conversation captured on two machines: the shared messages count once.
	a1, a2 := session(name+"-a", "pc1"), session(name+"-a", "pc2")
	turn(a1, 0, "user", "hello", at(0))
	turn(a1, 1, "assistant", "hi there", at(1))
	turn(a1, 2, "tool", "ignored", at(2))
	turn(a1, 3, "user", "undated", nil)
	turn(a2, 0, "user", "hello", at(0))
	turn(a2, 1, "assistant", "hi there", at(1))
	turn(a2, 2, "assistant", "only on pc2", at(24*60)) // the next day
	turn(a2, 3, "assistant", "same time, other text", at(1))
	// A conversation that also has an archived transcript.
	b := session(name+"-b", "pc1")
	turn(b, 0, "user", "archived too", at(5))
	turn(b, 1, "user", "undated twin", nil)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id, project_id, harness, conversation_id, transcript_payload, owner_user_id)
		VALUES (gen_random_uuid(), $1::uuid, 'codex', $2, '\x00'::bytea, $3::uuid)`, project.ID, name+"-b", owner); err != nil {
		t.Fatal(err)
	}

	from, until := day.AddDate(0, 0, -1), time.Now().AddDate(0, 0, 1)
	rows, err := s.ListAgentActivity(ctx, project.ID, owner, from, until, 330)
	if err != nil {
		t.Fatal(err)
	}
	wantDays, wantUndated := map[string]int{}, 0
	var wantTwins []string
	for _, r := range rows {
		if r.NativeID == name+"-b" {
			wantTwins = append(wantTwins, fmt.Sprintf("%s|%d|%d", r.Date, r.Count, len(r.Fingerprints)))
			continue
		}
		if r.Date == "" {
			wantUndated += r.Count
			continue
		}
		wantDays[r.Date] += len(r.Fingerprints)
	}
	roll, err := s.AgentActivityRollup(ctx, project.ID, owner, from, until, 330)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(roll.Days) != fmt.Sprint(wantDays) || roll.Undated != wantUndated {
		t.Fatalf("rollup days=%v undated=%d, want %v / %d", roll.Days, roll.Undated, wantDays, wantUndated)
	}
	if total := roll.Days[day.Add(330*time.Minute).Format("2006-01-02")]; total != 3 {
		t.Fatalf("first day = %d messages, want 3 (hello, hi there, same-time other text)", total)
	}
	var gotTwins []string
	for _, r := range roll.Twins {
		if r.NativeID != name+"-b" {
			t.Fatalf("unexpected twin %+v", r)
		}
		gotTwins = append(gotTwins, fmt.Sprintf("%s|%d|%d", r.Date, r.Count, len(r.Fingerprints)))
	}
	sort.Strings(wantTwins)
	sort.Strings(gotTwins)
	if fmt.Sprint(gotTwins) != fmt.Sprint(wantTwins) || len(gotTwins) != 2 {
		t.Fatalf("twins = %v, want %v", gotTwins, wantTwins)
	}

	// Memory and event counts.
	for i, status := range []string{"PROPOSED", "CONFIRMED", "CONFIRMED", "REJECTED"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO memory_items(project_id, key, content, status, visibility) VALUES ($1::uuid, $2, 'long enough memory content here', $3, $4)`,
			project.ID, fmt.Sprintf("k%d", i), status, map[bool]string{true: "private", false: "project"}[i == 2]); err != nil {
			t.Fatal(err)
		}
	}
	mem, err := s.DashboardMemories(ctx, project.ID)
	if err != nil || mem != (DashboardMemoryCounts{Total: 2, Proposed: 1, Confirmed: 1}) {
		t.Fatalf("memories = %+v %v", mem, err)
	}
	big := map[string]any{"output": strings.Repeat("x", 8192)}
	for i, ev := range []*Event{
		{ProjectID: project.ID, EventType: "MCP_TOOL_CALL", Payload: map[string]any{"n": 1}},
		{ProjectID: project.ID, EventType: "MEMORY_PROPOSED", Payload: big},
		{ProjectID: project.ID, EventType: "MEMORY_PROPOSED", Payload: map[string]any{"n": 3}},
	} {
		if err := s.AppendEvent(ctx, ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	now := time.Now().UTC()
	stats, err := s.DashboardEvents(ctx, project.ID, now.AddDate(0, 0, -364), now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Week != 3 || stats.AgentCalls != 1 || stats.Days[now.Format("2006-01-02")] != 3 || len(stats.Recent) != 3 ||
		stats.ByType[0] != (DashboardTypeCount{Type: "MEMORY_PROPOSED", Count: 2}) {
		t.Fatalf("event stats = %+v", stats)
	}
	if stats.Recent[1].Payload["truncated"] != true || stats.Recent[0].Payload["n"] != float64(3) {
		t.Fatalf("recent payloads: %+v / %+v", stats.Recent[0].Payload, stats.Recent[1].Payload)
	}
}
