package store

import (
	"context"
	"crypto/md5"
	"fmt"
	"time"
)

// AgentActivityDay is scoped to an owner and project. Empty Date means the
// original harness timestamp is unknown; ts/receipt time is not a substitute.
type AgentActivityDay struct {
	Harness, NativeID, Date string
	Count                   int
	Fingerprints            []string
}

// A non-security content identity for deduplicating legacy/mirrored copies.
func ActivityFingerprint(at time.Time, role, text string) string {
	chars := []rune(text)
	if len(chars) > 512 {
		chars = chars[:512]
	}
	return fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%d|%s|%s", at.UnixMicro(), role, string(chars)))))
}

type AgentActivityStore interface {
	ListAgentActivity(context.Context, string, string, time.Time, time.Time, int) ([]AgentActivityDay, error)
}

func (m *MemStore) ListAgentActivity(ctx context.Context, project, owner string, from, until time.Time, offset int) ([]AgentActivityDay, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	counts := map[[3]string]int{}
	identities := map[[3]string]map[string]bool{}
	zone := time.FixedZone("activity", offset*60)
	for id, session := range m.agentSessions {
		if session.ProjectID != project || session.OwnerUserID != owner {
			continue
		}
		for _, turn := range m.sessionTurns[id] {
			if turn.Role != "user" && turn.Role != "assistant" {
				continue
			}
			date := ""
			if turn.SourceAt != nil {
				if turn.SourceAt.Before(from) || !turn.SourceAt.Before(until) {
					continue
				}
				date = turn.SourceAt.In(zone).Format("2006-01-02")
			}
			key := [3]string{session.Harness, session.NativeID, date}
			if date == "" {
				counts[key]++
				continue
			}
			if identities[key] == nil {
				identities[key] = map[string]bool{}
			}
			identities[key][ActivityFingerprint(*turn.SourceAt, turn.Role, turn.TextPreview)] = true
		}
	}
	out := make([]AgentActivityDay, 0, len(counts))
	for key, count := range counts {
		out = append(out, AgentActivityDay{Harness: key[0], NativeID: key[1], Date: key[2], Count: count})
	}
	for key, values := range identities {
		row := AgentActivityDay{Harness: key[0], NativeID: key[1], Date: key[2], Count: len(values)}
		for value := range values {
			row.Fingerprints = append(row.Fingerprints, value)
		}
		out = append(out, row)
	}
	return out, ctx.Err()
}

func (s *PostgresStore) ListAgentActivity(ctx context.Context, project, owner string, from, until time.Time, offset int) ([]AgentActivityDay, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.harness, a.native_id,
		  COALESCE(to_char((t.source_ts AT TIME ZONE 'UTC') + ($5::integer * INTERVAL '1 minute'), 'YYYY-MM-DD'), ''), COUNT(*),
		  COALESCE(array_agg(DISTINCT md5((trunc(EXTRACT(EPOCH FROM t.source_ts)*1000000)::bigint)::text || '|' || t.role || '|' || LEFT(COALESCE(t.text_preview,''),512))) FILTER (WHERE t.source_ts IS NOT NULL), ARRAY[]::text[])
		FROM session_turns t JOIN agent_sessions a ON a.id = t.session_id
		WHERE a.project_id = $1::uuid AND a.owner_user_id = $2::uuid
		  AND t.role IN ('user', 'assistant')
		  AND (t.source_ts IS NULL OR (t.source_ts >= $3 AND t.source_ts < $4))
		GROUP BY 1, 2, 3`, project, owner, from, until, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentActivityDay{}
	for rows.Next() {
		var day AgentActivityDay
		if err := rows.Scan(&day.Harness, &day.NativeID, &day.Date, &day.Count, &day.Fingerprints); err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}
