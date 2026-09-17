package migrate

import (
	"testing"
)

const sessionsFixture = `{"project": "shop", "session_id": "s1", "updated": "2026-09-10T15:04:00Z"}
{"project": "shop", "session_id": "s2", "started": "2026-09-11 10:00", "ended": "2026-09-11T12:00:00Z", "was": "old-shop"}

{"project": "shop", "session_id": "s1", "updated": "2026-09-10T15:04:00Z"}
not json at all
{"project": "shop", "updated": "2026-09-10T15:04:00Z"}
{"project": "blog", "session_id": "s3", "agent": "cursor", "updated": "2026-09-12"}
`

func TestParseSessionsJSONL(t *testing.T) {
	events, skipped := ParseSessionsJSONLReport(sessionsFixture, "claude")
	// s1 (pair) + s2 (pair) + s3 (pair); dup s1, invalid line,
	// missing session_id skipped.
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6", len(events))
	}
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}

	started, ended := events[0], events[1]
	if started.EventType != "SESSION_STARTED" || ended.EventType != "SESSION_ENDED" {
		t.Fatalf("first pair = %q/%q, want SESSION_STARTED/SESSION_ENDED",
			started.EventType, ended.EventType)
	}
	if started.SessionID != "s1" || started.Agent != "claude" || started.Project != "shop" {
		t.Errorf("s1 attribution = %+v", started)
	}
	if started.At.Format("2006-01-02") != "2026-09-10" {
		t.Errorf("s1 start = %v, want 2026-09-10", started.At)
	}

	// s2 carries the moved-from note and distinct start/end.
	s2start, s2end := events[2], events[3]
	if s2start.Note != "moved from old-shop" || s2end.Note != "moved from old-shop" {
		t.Errorf("s2 notes = %q/%q, want moved-from preserved", s2start.Note, s2end.Note)
	}
	if !s2start.At.Before(s2end.At) {
		t.Errorf("s2 start %v should precede end %v", s2start.At, s2end.At)
	}

	// Row-level agent overrides the directory default.
	s3 := events[4]
	if s3.Agent != "cursor" || s3.Project != "blog" {
		t.Errorf("s3 = %+v, want agent cursor project blog", s3)
	}
}

func TestDeduplicateEvents(t *testing.T) {
	a, _ := ParseSessionsJSONLReport("{\"session_id\": \"s1\", \"project\": \"p\", \"updated\": \"2026-09-10T15:04:00Z\"}\n", "claude")
	b, _ := ParseSessionsJSONLReport("{\"session_id\": \"s1\", \"project\": \"p\", \"updated\": \"2026-09-10T15:04:00Z\"}\n", "claude")
	merged := DeduplicateEvents(append(a, b...))
	if len(merged) != 2 { // one STARTED + one ENDED
		t.Fatalf("merged = %d events, want 2", len(merged))
	}
	// Same session under a different agent is a different history.
	c, _ := ParseSessionsJSONLReport("{\"session_id\": \"s1\", \"project\": \"p\", \"updated\": \"2026-09-10T15:04:00Z\"}\n", "cursor")
	merged = DeduplicateEvents(append(merged, c...))
	if len(merged) != 4 {
		t.Fatalf("merged = %d events, want 4 (agent is part of identity)", len(merged))
	}
}
