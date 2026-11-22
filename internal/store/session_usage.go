package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"central-memory/internal/usage"
)

// SessionUsage is one usage record (migration 048). Key identifies the
// model call within the session (a Claude message id, a Codex cumulative
// total, "idx:<turn>"...); re-sending a key keeps the larger counts.
type SessionUsage struct {
	SessionID string
	Key       string
	TurnIdx   *int64
	SourceAt  *time.Time
	usage.Entry
}

// UsageQuery filters GET /v1/usage. GroupBy is agent, model, day or project.
// OwnerOnly limits to sessions the user owns; otherwise every session the
// user can read counts. TZOffset is minutes east of UTC for day buckets.
type UsageQuery struct {
	UserID    string
	From      time.Time
	To        time.Time
	ProjectID string
	GroupBy   string
	OwnerOnly bool
	TZOffset  int
}

// UsageStore is implemented by MemStore and PostgresStore.
type UsageStore interface {
	RecordSessionUsage(ctx context.Context, records []SessionUsage) error
	// SessionUsageByModel totals one session by model.
	SessionUsageByModel(ctx context.Context, sessionID string) ([]usage.Total, error)
	UsageSummary(ctx context.Context, q UsageQuery) ([]usage.Total, error)
}

// UsageKeyForTurn is the key for usage that arrived without its own.
func UsageKeyForTurn(idx int64) string { return fmt.Sprintf("idx:%d", idx) }

func validUsageRecord(r SessionUsage) error {
	if strings.TrimSpace(r.SessionID) == "" {
		return ErrNotFound
	}
	if strings.TrimSpace(r.Key) == "" || len(r.Key) > 200 {
		return errors.New("store: usage key is required (max 200 chars)")
	}
	if !r.Entry.Valid() {
		return errors.New("store: invalid usage values")
	}
	return nil
}

// usageAgg is one (group, model) partial sum. U* are tokens of records
// without a reported cost: only those are estimated from the price table.
type usageAgg struct {
	Group, Model        string
	In, Out, CR, CW     int64
	UIn, UOut, UCR, UCW int64
	Reported            float64
	HasReported         bool
	Turns               int64
	// Long marks records whose prompt is over usage.LongPromptTokens, so
	// tiered models (Haiku 5.5) are priced per request tier.
	Long bool
}

// foldUsage turns per-model partial sums into totals keyed by group,
// applying reported cost where present and the price table otherwise.
func foldUsage(rows []usageAgg, groupIsModel bool) []usage.Total {
	byKey := map[string]*usage.Total{}
	var order []string
	for _, r := range rows {
		t := byKey[r.Group]
		if t == nil {
			t = &usage.Total{Key: r.Group}
			if groupIsModel {
				t.Model = r.Model
			}
			byKey[r.Group] = t
			order = append(order, r.Group)
		}
		t.Input += r.In
		t.Output += r.Out
		t.CacheRead += r.CR
		t.CacheWrite += r.CW
		t.Turns += r.Turns
		if r.HasReported {
			t.ReportedUSD += r.Reported
			t.CostUSD += r.Reported
			t.HasCost = true
		}
		unreported := usage.Entry{Model: r.Model, Input: r.UIn, Output: r.UOut, CacheRead: r.UCR, CacheWrite: r.UCW}
		if unreported.Empty() {
			continue
		}
		if est, ok := usage.EstimateTier(unreported, r.Long); ok {
			t.EstimatedUSD += est
			t.CostUSD += est
			t.Estimated = true
			t.HasCost = true
		} else {
			t.UnpricedTokens += r.UIn + r.UOut + r.UCR + r.UCW
		}
	}
	out := make([]usage.Total, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func normalizeUsageGroup(g string) (string, error) {
	switch g = strings.ToLower(strings.TrimSpace(g)); g {
	case "", "agent":
		return "agent", nil
	case "model", "day", "project":
		return g, nil
	}
	return "", errors.New("group_by must be agent, model, day or project")
}

// ---- MemStore ----

func (m *MemStore) RecordSessionUsage(ctx context.Context, records []SessionUsage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		if err := validUsageRecord(r); err != nil {
			return err
		}
		if m.agentSessions[r.SessionID] == nil {
			return ErrNotFound
		}
		if m.sessionUsage == nil {
			m.sessionUsage = map[string]map[string]*SessionUsage{}
		}
		if m.sessionUsage[r.SessionID] == nil {
			m.sessionUsage[r.SessionID] = map[string]*SessionUsage{}
		}
		prev := m.sessionUsage[r.SessionID][r.Key]
		if prev == nil {
			cp := r
			if cp.SourceAt == nil {
				now := time.Now().UTC()
				cp.SourceAt = &now // MemStore keeps receipt time here
			}
			m.sessionUsage[r.SessionID][r.Key] = &cp
			continue
		}
		mergeUsage(prev, r)
	}
	return ctx.Err()
}

// mergeUsage mirrors the Postgres upsert: keep the larger counts so a
// partial (streaming) record never shrinks a complete one.
func mergeUsage(prev *SessionUsage, r SessionUsage) {
	prev.Input = max(prev.Input, r.Input)
	prev.Output = max(prev.Output, r.Output)
	prev.CacheRead = max(prev.CacheRead, r.CacheRead)
	prev.CacheWrite = max(prev.CacheWrite, r.CacheWrite)
	if r.CostUSD != nil && (prev.CostUSD == nil || *r.CostUSD > *prev.CostUSD) {
		c := *r.CostUSD
		prev.CostUSD = &c
	}
	if prev.Model == "" {
		prev.Model = r.Model
	}
	if prev.TurnIdx == nil {
		prev.TurnIdx = r.TurnIdx
	}
}

func memAgg(group string, r *SessionUsage) usageAgg {
	a := usageAgg{Group: group, Model: r.Model, In: r.Input, Out: r.Output, CR: r.CacheRead, CW: r.CacheWrite, Turns: 1,
		Long: usage.LongPrompt(r.Entry)}
	if r.CostUSD != nil {
		a.Reported, a.HasReported = *r.CostUSD, true
	} else {
		a.UIn, a.UOut, a.UCR, a.UCW = r.Input, r.Output, r.CacheRead, r.CacheWrite
	}
	return a
}

func (m *MemStore) SessionUsageByModel(ctx context.Context, sessionID string) ([]usage.Total, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var rows []usageAgg
	for _, r := range m.sessionUsage[sessionID] {
		rows = append(rows, memAgg(r.Model, r))
	}
	return foldUsage(rows, true), ctx.Err()
}

func (m *MemStore) UsageSummary(ctx context.Context, q UsageQuery) ([]usage.Total, error) {
	group, err := normalizeUsageGroup(q.GroupBy)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	zone := time.FixedZone("usage", q.TZOffset*60)
	var rows []usageAgg
	for sid, records := range m.sessionUsage {
		s := m.agentSessions[sid]
		if s == nil || (q.ProjectID != "" && s.ProjectID != q.ProjectID) {
			continue
		}
		if q.OwnerOnly && s.OwnerUserID != q.UserID {
			continue
		}
		if !q.OwnerOnly && !m.agentVisibleLocked(q.UserID, s) {
			continue
		}
		for _, r := range records {
			at := *r.SourceAt
			if (!q.From.IsZero() && at.Before(q.From)) || (!q.To.IsZero() && !at.Before(q.To)) {
				continue
			}
			key := s.Harness
			switch group {
			case "model":
				key = r.Model
			case "day":
				key = at.In(zone).Format("2006-01-02")
			case "project":
				key = s.ProjectID
			}
			rows = append(rows, memAgg(key, r))
		}
	}
	return foldUsage(rows, group == "model"), ctx.Err()
}

// ---- PostgresStore ----

func (s *PostgresStore) RecordSessionUsage(ctx context.Context, records []SessionUsage) error {
	for _, r := range records {
		if err := validUsageRecord(r); err != nil {
			return err
		}
		if !looksLikeUUID(r.SessionID) {
			return ErrNotFound
		}
	}
	for _, r := range records {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO session_usage (session_id, usage_key, turn_idx, model, input_tokens, output_tokens,
			  cache_read_tokens, cache_write_tokens, cost_usd, source_ts)
			SELECT $1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10
			WHERE EXISTS (SELECT 1 FROM agent_sessions WHERE id = $1::uuid)
			ON CONFLICT (session_id, usage_key) DO UPDATE SET
			  turn_idx = COALESCE(session_usage.turn_idx, EXCLUDED.turn_idx),
			  model = CASE WHEN session_usage.model = '' THEN EXCLUDED.model ELSE session_usage.model END,
			  input_tokens = GREATEST(session_usage.input_tokens, EXCLUDED.input_tokens),
			  output_tokens = GREATEST(session_usage.output_tokens, EXCLUDED.output_tokens),
			  cache_read_tokens = GREATEST(session_usage.cache_read_tokens, EXCLUDED.cache_read_tokens),
			  cache_write_tokens = GREATEST(session_usage.cache_write_tokens, EXCLUDED.cache_write_tokens),
			  cost_usd = CASE WHEN session_usage.cost_usd IS NULL THEN EXCLUDED.cost_usd
			                  WHEN EXCLUDED.cost_usd IS NULL THEN session_usage.cost_usd
			                  ELSE GREATEST(session_usage.cost_usd, EXCLUDED.cost_usd) END,
			  source_ts = COALESCE(session_usage.source_ts, EXCLUDED.source_ts)`,
			r.SessionID, r.Key, r.TurnIdx, r.Model, r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CostUSD, r.SourceAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	return nil
}

const usageAggColumns = `u.model,
	SUM(u.input_tokens)::bigint, SUM(u.output_tokens)::bigint,
	SUM(u.cache_read_tokens)::bigint, SUM(u.cache_write_tokens)::bigint,
	COALESCE(SUM(u.input_tokens) FILTER (WHERE u.cost_usd IS NULL), 0)::bigint,
	COALESCE(SUM(u.output_tokens) FILTER (WHERE u.cost_usd IS NULL), 0)::bigint,
	COALESCE(SUM(u.cache_read_tokens) FILTER (WHERE u.cost_usd IS NULL), 0)::bigint,
	COALESCE(SUM(u.cache_write_tokens) FILTER (WHERE u.cost_usd IS NULL), 0)::bigint,
	COALESCE(SUM(u.cost_usd), 0)::double precision, COUNT(u.cost_usd) > 0, COUNT(*),
	(u.input_tokens + u.cache_read_tokens + u.cache_write_tokens) > 100000`

// The literal above is usage.LongPromptTokens (checked by a test); the tier
// is column 14, which every usage query groups by.

func scanUsageAggs(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]usageAgg, error) {
	defer rows.Close()
	var out []usageAgg
	for rows.Next() {
		var a usageAgg
		if err := rows.Scan(&a.Group, &a.Model, &a.In, &a.Out, &a.CR, &a.CW, &a.UIn, &a.UOut, &a.UCR, &a.UCW,
			&a.Reported, &a.HasReported, &a.Turns, &a.Long); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SessionUsageByModel(ctx context.Context, sessionID string) ([]usage.Total, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT u.model, `+usageAggColumns+`
		FROM session_usage u WHERE u.session_id = $1::uuid GROUP BY 1, 2, 14`, sessionID)
	if err != nil {
		return nil, err
	}
	aggs, err := scanUsageAggs(rows)
	if err != nil {
		return nil, err
	}
	return foldUsage(aggs, true), nil
}

func (s *PostgresStore) UsageSummary(ctx context.Context, q UsageQuery) ([]usage.Total, error) {
	group, err := normalizeUsageGroup(q.GroupBy)
	if err != nil {
		return nil, err
	}
	if !looksLikeUUID(q.UserID) || (q.ProjectID != "" && !looksLikeUUID(q.ProjectID)) {
		return []usage.Total{}, nil
	}
	groupExpr := map[string]string{
		"agent":   "s.harness",
		"model":   "u.model",
		"project": "s.project_id::text",
		"day":     "to_char((COALESCE(u.source_ts, u.created_at) AT TIME ZONE 'UTC') + ($5::integer * INTERVAL '1 minute'), 'YYYY-MM-DD')",
	}[group]
	var from, to any
	if !q.From.IsZero() {
		from = q.From
	}
	if !q.To.IsZero() {
		to = q.To
	}
	var project any
	if q.ProjectID != "" {
		project = q.ProjectID
	}
	// Visibility matches CanReadAgentSession; OwnerOnly narrows it to the
	// caller's own chats.
	rows, err := s.pool.Query(ctx, `
		SELECT `+groupExpr+`, `+usageAggColumns+`
		FROM session_usage u JOIN agent_sessions s ON s.id = u.session_id
		WHERE ($2::timestamptz IS NULL OR COALESCE(u.source_ts, u.created_at) >= $2)
		  AND ($3::timestamptz IS NULL OR COALESCE(u.source_ts, u.created_at) < $3)
		  AND ($4::uuid IS NULL OR s.project_id = $4::uuid)
		  AND $5::integer IS NOT NULL
		  AND (s.owner_user_id = $1::uuid OR (NOT $6::boolean AND (
		    EXISTS (SELECT 1 FROM session_grants g
		            WHERE g.session_id = s.id AND g.revoked_at IS NULL AND g.grantee_user_id = $1::uuid)
		    OR (s.visibility = 'team' AND (
		      EXISTS (SELECT 1 FROM projects p WHERE p.id = s.project_id AND p.created_by = $1::uuid)
		      OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = $1::uuid)
		      OR EXISTS (SELECT 1 FROM projects p JOIN organization_members om ON om.org_id = p.org_id
		                 WHERE p.id = s.project_id AND om.user_id = $1::uuid AND om.role IN ('ADMIN','OWNER'))
		    ))
		  )))
		GROUP BY 1, 2, 14`, q.UserID, from, to, project, q.TZOffset, q.OwnerOnly)
	if err != nil {
		return nil, err
	}
	aggs, err := scanUsageAggs(rows)
	if err != nil {
		return nil, err
	}
	return foldUsage(aggs, group == "model"), nil
}
