// Session transcript import (Phase 1b): legacy
// agents/<agent>/normalized/sessions.jsonl rows become historical
// SESSION_STARTED / SESSION_ENDED events.
//
// Row shape (v1 cmdSessions): one JSON object per line with at least
// "session_id" plus "project" and "updated", optionally "was" (the session
// moved projects), "started"/"ended", and "agent" (overrides the directory
// the file was found in). Harvesters wrote whatever they had, so every
// field except session_id is best-effort: missing timestamps become the
// zero time (the INSERT binding defaults to now()), missing projects stay
// empty (the event still orders the session lifecycle).
//
// One row yields exactly the STARTED+ENDED pair: the old system kept only
// session summaries, not per-turn transcripts, so lifecycle endpoints are
// all the history there is. Duplicate (agent, session_id) rows — re-harvests
// wrote the same session twice — collapse to the first pair seen.
package migrate

import (
	"encoding/json"
	"strings"
	"time"
)

// HistoricalEvent is the migration-local shape of an events row: the
// lifecycle endpoints of one legacy session.
type HistoricalEvent struct {
	EventType string // SESSION_STARTED | SESSION_ENDED
	Project   string
	SessionID string
	Agent     string
	At        time.Time
	Note      string
}

// ParseSessionsJSONL parses one sessions.jsonl file. agent is the directory
// the file came from (agents/<agent>/...) and is the default attribution;
// a row-level "agent" field overrides it. Blank lines are ignored; malformed
// JSON, rows without a session_id, and duplicate (agent, session_id) rows
// are skipped, never fatal.
func ParseSessionsJSONL(data, agent string) []HistoricalEvent {
	events, _ := ParseSessionsJSONLReport(data, agent)
	return events
}

// ParseSessionsJSONLReport is ParseSessionsJSONL plus the skip count, for
// Run's accounting.
func ParseSessionsJSONLReport(data, agent string) ([]HistoricalEvent, int) {
	seen := map[string]bool{}
	var out []HistoricalEvent
	skipped := 0
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			skipped++
			continue
		}
		sid := strField(row, "session_id", "sessionId", "id")
		if sid == "" {
			skipped++
			continue
		}
		ag := agent
		if a := strField(row, "agent"); a != "" {
			ag = a
		}
		key := ag + "\x00" + sid
		if seen[key] {
			skipped++
			continue
		}
		seen[key] = true

		proj := strField(row, "project")
		note := ""
		if was := strField(row, "was"); was != "" {
			note = "moved from " + was
		}
		out = append(out,
			HistoricalEvent{
				EventType: "SESSION_STARTED",
				Project:   proj,
				SessionID: sid,
				Agent:     ag,
				At:        timeField(row, "started", "created", "updated"),
				Note:      note,
			},
			HistoricalEvent{
				EventType: "SESSION_ENDED",
				Project:   proj,
				SessionID: sid,
				Agent:     ag,
				At:        timeField(row, "ended", "updated", "started"),
				Note:      note,
			},
		)
	}
	return out, skipped
}

// sessionTimeLayouts covers what harvesters wrote ("updated" was usually
// RFC3339; the v1 writer's "2006-01-02 15:04" and bare dates appear in
// hand-touched files).
var sessionTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// strField returns the first non-blank string value found under any of the
// given keys.
func strField(row map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := row[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// timeField returns the first parseable timestamp under any of the keys, or
// the zero time when none is present (the INSERT binding defaults it).
func timeField(row map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		if v, ok := row[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				if t, ok := parseSessionTime(strings.TrimSpace(s)); ok {
					return t
				}
			}
		}
	}
	return time.Time{}
}

// parseSessionTime tries the known harvester layouts in order.
func parseSessionTime(s string) (time.Time, bool) {
	for _, layout := range sessionTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// DeduplicateEvents collapses repeated (event type, agent, session) triples
// across files — the same session was often harvested under two agents or
// re-exported. First event wins; order is preserved.
func DeduplicateEvents(events []HistoricalEvent) []HistoricalEvent {
	seen := make(map[string]bool, len(events))
	out := make([]HistoricalEvent, 0, len(events))
	for _, ev := range events {
		key := ev.EventType + "\x00" + ev.Agent + "\x00" + ev.SessionID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ev)
	}
	return out
}
