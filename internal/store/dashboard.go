package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Dashboard reads (GET /projects/{id}/dashboard). Every number is counted
// in SQL over a bounded window; no event payloads, memory rows or turn texts
// are loaded to be counted in Go. A heavy project (tens of thousands of
// turns, thousands of memories with 1536-dimension embeddings) used to move
// ~30 MB per request and scan every turn in the database.

// DashboardTypeCount is one row of the events-by-type breakdown.
type DashboardTypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// DashboardEventStats counts a project's events since a cutoff.
type DashboardEventStats struct {
	Days       map[string]int // UTC date (YYYY-MM-DD) -> events that day
	Week       int            // events in the last 7 days
	AgentCalls int            // MCP tool calls or agent-attributed events in the window
	ByType     []DashboardTypeCount
	Recent     []*Event // newest first; oversized payloads replaced by a stub
}

// DashboardMemoryCounts counts active (CONFIRMED or PROPOSED, not superseded)
// memories with project or public visibility, the rows the dashboard has
// always counted.
type DashboardMemoryCounts struct {
	Total, Proposed, Confirmed int
}

// AgentActivityRollup is a project owner's agent messages per day.
// Conversations that also exist as archived (legacy) transcripts must be
// deduplicated against those archives in Go, so only they come back as
// per-conversation rows with message fingerprints (Twins). Everything else
// is already summed per day.
type AgentActivityRollup struct {
	Days    map[string]int // date in the caller's offset -> distinct messages
	Undated int            // messages without an original timestamp
	Twins   []AgentActivityDay
}

// DashboardStore is implemented by stores that can answer the dashboard
// with aggregate queries.
type DashboardStore interface {
	DashboardEvents(ctx context.Context, projectID string, since, now time.Time) (DashboardEventStats, error)
	DashboardMemories(ctx context.Context, projectID string) (DashboardMemoryCounts, error)
	AgentActivityRollup(ctx context.Context, projectID, owner string, from, until time.Time, offset int) (AgentActivityRollup, error)
}

// TranscriptStore reads only an archived session's transcript bytes (the
// latest version), without its artifacts bundle or diff.
type TranscriptStore interface {
	LatestTranscript(ctx context.Context, sessionID string) ([]byte, error)
}

// dashboardRecentLimit is how many recent events the dashboard lists, and
// dashboardPayloadMax the largest payload (bytes, as stored) sent with one.
const (
	dashboardRecentLimit = 12
	dashboardPayloadMax  = 4096
)

func topTypes(byType map[string]int) []DashboardTypeCount {
	out := make([]DashboardTypeCount, 0, len(byType))
	for t, n := range byType {
		out = append(out, DashboardTypeCount{Type: t, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func (s *PostgresStore) DashboardEvents(ctx context.Context, projectID string, since, now time.Time) (DashboardEventStats, error) {
	out := DashboardEventStats{Days: map[string]int{}}
	if strings.TrimSpace(projectID) == "" {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT (created_at AT TIME ZONE 'UTC')::date, event_type, COUNT(*)::int,
		       COUNT(*) FILTER (WHERE event_type = 'MCP_TOOL_CALL' OR agent_id IS NOT NULL)::int,
		       COUNT(*) FILTER (WHERE created_at > $3)::int
		  FROM events
		 WHERE project_id = $1::uuid AND created_at >= $2
		 GROUP BY 1, 2`, projectID, since, now.AddDate(0, 0, -7))
	if err != nil {
		return out, err
	}
	byType := map[string]int{}
	for rows.Next() {
		var day time.Time
		var typ string
		var n, agent, week int
		if err := rows.Scan(&day, &typ, &n, &agent, &week); err != nil {
			rows.Close()
			return out, err
		}
		out.Days[day.Format("2006-01-02")] += n
		byType[typ] += n
		out.AgentCalls += agent
		out.Week += week
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.ByType = topTypes(byType)

	// Recent events walk the (project_id, created_at) index backwards. A
	// payload can hold captured tool output (hundreds of KB): only small
	// ones are sent. pg_column_size (stored, maybe compressed, size) is
	// checked first so a large payload is never detoasted.
	recent, err := s.pool.Query(ctx, `
		SELECT id, project_id, session_id, user_id, agent_id, workspace_id, episode_id, event_type,
		       CASE WHEN pg_column_size(payload) > $3 OR octet_length(payload::text) > $3
		            THEN '{"truncated":true}'::jsonb ELSE payload END,
		       created_at
		  FROM events
		 WHERE project_id = $1::uuid
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2`, projectID, dashboardRecentLimit, dashboardPayloadMax)
	if err != nil {
		return out, err
	}
	defer recent.Close()
	for recent.Next() {
		ev, err := scanEvent(recent)
		if err != nil {
			return out, err
		}
		out.Recent = append(out.Recent, ev)
	}
	return out, recent.Err()
}

func (s *PostgresStore) DashboardMemories(ctx context.Context, projectID string) (DashboardMemoryCounts, error) {
	var out DashboardMemoryCounts
	if strings.TrimSpace(projectID) == "" {
		return out, nil
	}
	// Same rows SearchMemory shows a caller without a viewer: project or
	// organization level, active, project/public visibility (NormalizeVisibility
	// treats "" as project).
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE status = 'PROPOSED')::int,
		       COUNT(*) FILTER (WHERE status = 'CONFIRMED')::int
		  FROM memory_items
		 WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))
		   AND status IN ('CONFIRMED', 'PROPOSED')
		   AND superseded_by IS NULL
		   AND lower(btrim(visibility)) IN ('', 'project', 'public')`, projectID).
		Scan(&out.Total, &out.Proposed, &out.Confirmed)
	return out, err
}

func (s *PostgresStore) AgentActivityRollup(ctx context.Context, projectID, owner string, from, until time.Time, offset int) (AgentActivityRollup, error) {
	out := AgentActivityRollup{Days: map[string]int{}}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(owner) == "" {
		return out, nil
	}
	// The owner's conversations in the project, and whether each also has an
	// archived transcript (its "twin", deduplicated in the server). Turns
	// are then read per session through the session_turns primary key, so
	// the cost follows this project's turns, not the whole table.
	rows, err := s.pool.Query(ctx, `
		SELECT a.id::text, EXISTS (
		         SELECT 1 FROM session_snapshots s
		          WHERE s.project_id = a.project_id AND s.owner_user_id = a.owner_user_id
		            AND s.harness = a.harness AND s.conversation_id = a.native_id)
		  FROM agent_sessions a
		 WHERE a.project_id = $1::uuid AND a.owner_user_id = $2::uuid`, projectID, owner)
	if err != nil {
		return out, err
	}
	var plain, twinned []string
	for rows.Next() {
		var id string
		var twin bool
		if err := rows.Scan(&id, &twin); err != nil {
			rows.Close()
			return out, err
		}
		if twin {
			twinned = append(twinned, id)
		} else {
			plain = append(plain, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// A message's identity is (conversation, original time, role, text): the
	// same message captured twice, or from two machines, counts once, as
	// with ActivityFingerprint. The text is hashed so the grouping stays
	// narrow and in memory; undated messages are summed, not deduplicated.
	if len(plain) > 0 {
		days, err := s.pool.Query(ctx, `
			SELECT COALESCE(to_char(day, 'YYYY-MM-DD'), ''),
			       (COUNT(*) FILTER (WHERE day IS NOT NULL))::int,
			       COALESCE(SUM(n) FILTER (WHERE day IS NULL), 0)::int
			  FROM (
			    SELECT ((t.source_ts AT TIME ZONE 'UTC') + ($5::integer * INTERVAL '1 minute'))::date AS day, COUNT(*) AS n
			      FROM session_turns t
			      JOIN agent_sessions a ON a.id = t.session_id
			     WHERE t.session_id = ANY($1::uuid[])
			       AND t.role IN ('user', 'assistant')
			       AND (t.source_ts IS NULL OR (t.source_ts >= $3 AND t.source_ts < $4))
			       AND a.owner_user_id = $2::uuid
			     GROUP BY 1, a.harness, a.native_id, t.source_ts, t.role, hashtextextended(LEFT(COALESCE(t.text_preview, ''), 512), 0)
			  ) messages
			 GROUP BY day`, plain, owner, from, until, offset)
		if err != nil {
			return out, err
		}
		for days.Next() {
			var day string
			var dated, undated int
			if err := days.Scan(&day, &dated, &undated); err != nil {
				days.Close()
				return out, err
			}
			if day == "" {
				out.Undated += undated
				continue
			}
			out.Days[day] += dated
		}
		days.Close()
		if err := days.Err(); err != nil {
			return out, err
		}
	}
	if len(twinned) == 0 {
		return out, nil
	}

	// Conversations with an archived twin: per-conversation rows with
	// fingerprints, merged with the archive's in the server.
	twins, err := s.pool.Query(ctx, `
		SELECT a.harness, a.native_id,
		       COALESCE(to_char((t.source_ts AT TIME ZONE 'UTC') + ($5::integer * INTERVAL '1 minute'), 'YYYY-MM-DD'), ''), COUNT(*)::int,
		       COALESCE(array_agg(DISTINCT md5((trunc(EXTRACT(EPOCH FROM t.source_ts)*1000000)::bigint)::text || '|' || t.role || '|' || LEFT(COALESCE(t.text_preview,''),512)))
		         FILTER (WHERE t.source_ts IS NOT NULL), ARRAY[]::text[])
		  FROM session_turns t
		  JOIN agent_sessions a ON a.id = t.session_id
		 WHERE t.session_id = ANY($1::uuid[])
		   AND t.role IN ('user', 'assistant')
		   AND (t.source_ts IS NULL OR (t.source_ts >= $3 AND t.source_ts < $4))
		   AND a.owner_user_id = $2::uuid
		 GROUP BY 1, 2, 3`, twinned, owner, from, until, offset)
	if err != nil {
		return out, err
	}
	defer twins.Close()
	for twins.Next() {
		var day AgentActivityDay
		if err := twins.Scan(&day.Harness, &day.NativeID, &day.Date, &day.Count, &day.Fingerprints); err != nil {
			return out, err
		}
		out.Twins = append(out.Twins, day)
	}
	return out, twins.Err()
}

func (s *PostgresStore) LatestTranscript(ctx context.Context, sessionID string) ([]byte, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT transcript_payload FROM session_snapshots
		 WHERE session_id = $1::uuid
		 ORDER BY snapshot_version DESC
		 LIMIT 1`, sessionID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return payload, err
}

// ---- MemStore ----

func (m *MemStore) DashboardEvents(ctx context.Context, projectID string, since, now time.Time) (DashboardEventStats, error) {
	out := DashboardEventStats{Days: map[string]int{}}
	byType := map[string]int{}
	week := now.AddDate(0, 0, -7)
	m.mu.RLock()
	var mine []*Event
	for _, ev := range m.events {
		if ev.ProjectID == projectID {
			mine = append(mine, ev)
		}
	}
	for _, ev := range mine {
		if ev.CreatedAt.Before(since) {
			continue
		}
		out.Days[ev.CreatedAt.UTC().Format("2006-01-02")]++
		byType[ev.EventType]++
		if ev.EventType == "MCP_TOOL_CALL" || strings.TrimSpace(ev.AgentID) != "" {
			out.AgentCalls++
		}
		if ev.CreatedAt.After(week) {
			out.Week++
		}
	}
	for i := len(mine) - 1; i >= 0 && len(out.Recent) < dashboardRecentLimit; i-- {
		out.Recent = append(out.Recent, cloneEvent(mine[i]))
	}
	m.mu.RUnlock()
	out.ByType = topTypes(byType)
	return out, ctx.Err()
}

func (m *MemStore) DashboardMemories(ctx context.Context, projectID string) (DashboardMemoryCounts, error) {
	var out DashboardMemoryCounts
	items, err := m.SearchMemory(ctx, projectID, "", nil, 1<<30)
	if err != nil {
		return out, err
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		out.Total++
		switch item.Status {
		case "PROPOSED":
			out.Proposed++
		case "CONFIRMED":
			out.Confirmed++
		}
	}
	return out, nil
}

// MemStore has no archived snapshots table of its own here, so every
// conversation is returned as a twin and deduplicated by the server.
func (m *MemStore) AgentActivityRollup(ctx context.Context, projectID, owner string, from, until time.Time, offset int) (AgentActivityRollup, error) {
	rows, err := m.ListAgentActivity(ctx, projectID, owner, from, until, offset)
	return AgentActivityRollup{Days: map[string]int{}, Twins: rows}, err
}
