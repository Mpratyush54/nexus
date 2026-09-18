package migrate

import (
	"testing"
	"time"
)

func TestAuditSessionsRowShape(t *testing.T) {
	line := `{"session_id":"s1","project":"proj","started":"2026-09-01T10:00:00Z","ended":"2026-09-01T11:00:00Z","was":"oldproj"}`
	evs, skipped := ParseSessionsJSONLReport(line+"\n", "cursor")
	if skipped != 0 || len(evs) != 2 {
		t.Fatalf("one row -> STARTED+ENDED pair: evs=%d skipped=%d", len(evs), skipped)
	}
	if evs[0].EventType != "SESSION_STARTED" || evs[1].EventType != "SESSION_ENDED" {
		t.Errorf("pair types wrong: %+v", evs)
	}
	if evs[0].SessionID != "s1" || evs[0].Agent != "cursor" || evs[0].Project != "proj" || evs[0].Note != "moved from oldproj" {
		t.Errorf("row fields wrong: %+v", evs[0])
	}
	if evs[0].At.IsZero() || evs[1].At.IsZero() {
		t.Errorf("timestamps should parse: %+v", evs)
	}
	// Row-level agent overrides directory.
	evs2, _ := ParseSessionsJSONLReport(`{"session_id":"s2","agent":"claude"}`+"\n", "cursor")
	if len(evs2) != 2 || evs2[0].Agent != "claude" {
		t.Errorf("agent override failed: %+v", evs2)
	}
	// Missing timestamps -> zero time (INSERT binding defaults).
	evs3, _ := ParseSessionsJSONLReport(`{"session_id":"s3"}`+"\n", "a")
	if len(evs3) != 2 || !evs3[0].At.IsZero() {
		t.Errorf("missing times must be zero: %+v", evs3)
	}
	// Alternate keys + layouts.
	for _, l := range []string{
		`{"sessionId":"sx","created":"2026-09-01 15:04","updated":"2026-09-02"}`,
		`{"id":"sy","started":"2026-09-01 15:04:05","ended":"2026-09-01"}`,
	} {
		if evs, skip := ParseSessionsJSONLReport(l+"\n", "a"); len(evs) != 2 || skip != 0 {
			t.Errorf("alt keys failed for %s: evs=%d skip=%d", l, len(evs), skip)
		}
	}
	_ = time.Now
}

func TestAuditSessionsSkipsAndDedup(t *testing.T) {
	data := "\n" +
		`{"session_id":"ok1"}` + "\n" +
		"not-json\n" +
		`{"project":"no-id"}` + "\n" +
		`{"session_id":"ok1"}` + "\n" + // dup (agent,session) within file
		`{"session_id":"  "}` + "\n" // blank id
	evs, skipped := ParseSessionsJSONLReport(data, "a")
	if len(evs) != 2 {
		t.Fatalf("only first ok1 pair survives: evs=%d", len(evs))
	}
	if skipped != 4 {
		t.Errorf("skipped = %d, want 4 (bad json, no id, dup, blank id)", skipped)
	}
	// Cross-file triple dedup: same session harvested under two agents keeps
	// both agent-scoped pairs (key includes agent); true dup triples collapse.
	dup := append(evs, evs...)
	if got := DeduplicateEvents(dup); len(got) != 2 {
		t.Errorf("triple dedup: got %d, want 2", len(got))
	}
	mixed := []HistoricalEvent{
		{EventType: "SESSION_STARTED", Agent: "a", SessionID: "s"},
		{EventType: "SESSION_ENDED", Agent: "a", SessionID: "s"},
		{EventType: "SESSION_STARTED", Agent: "b", SessionID: "s"},
	}
	if got := DeduplicateEvents(mixed); len(got) != 3 {
		t.Errorf("different agents must not collapse: %d", len(got))
	}
	// Order preserved, first wins.
	ordered := []HistoricalEvent{
		{EventType: "SESSION_STARTED", Agent: "a", SessionID: "s", Project: "first"},
		{EventType: "SESSION_STARTED", Agent: "a", SessionID: "s", Project: "second"},
	}
	if got := DeduplicateEvents(ordered); len(got) != 1 || got[0].Project != "first" {
		t.Errorf("first-wins: %+v", got)
	}
	// Blank lines ignored (not skipped).
	if _, skip := ParseSessionsJSONLReport("\n\n   \n", "a"); skip != 0 {
		t.Errorf("blank lines must be ignored, skipped=%d", skip)
	}
	// Unparseable time falls through to next key / zero.
	evs4, _ := ParseSessionsJSONLReport(`{"session_id":"s9","started":"not-a-time","created":"2026-09-03"}`+"\n", "a")
	if len(evs4) != 2 || evs4[0].At.IsZero() {
		t.Errorf("time fallback failed: %+v", evs4)
	}
}
