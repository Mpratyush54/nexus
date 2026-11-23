package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Deleting a chat and cleaning up duplicate turns. Both are owner-only
// actions; the server checks ownership before calling these.

// SessionDeleteResult counts what DeleteAgentSession removed.
type SessionDeleteResult struct {
	SessionID string `json:"session_id"`
	Turns     int    `json:"turns"`
	Versions  int    `json:"versions"`
	Grants    int    `json:"grants"`
	Usage     int    `json:"usage"`
	Snapshots int    `json:"snapshots"`
}

// TurnDedupeResult is the outcome of DedupeSessionTurns. Removed is the
// number of copies removed (or that would be, on a dry run); Kept is the
// number of turns the session has afterwards. RemovedIdx lists the copies.
type TurnDedupeResult struct {
	Removed    int   `json:"removed"`
	Kept       int   `json:"kept"`
	RemovedIdx []int `json:"removed_idx"`
	DryRun     bool  `json:"dry_run"`
}

// AgentSessionDeleter removes a chat or its duplicate turns.
type AgentSessionDeleter interface {
	// DeleteAgentSession removes the session with its turns, versions,
	// grants, usage, guest links, teleports, code changes, snapshots and
	// provenance. Memories made from it stay. ErrNotFound when missing.
	DeleteAgentSession(ctx context.Context, sessionID string) (*SessionDeleteResult, error)
	// DedupeSessionTurns removes undated turns (source_ts NULL) that are an
	// exact copy (role, text, tool_calls) of a dated turn in the same
	// session. Claude Code api-request-blob lines used to be stored this way.
	DedupeSessionTurns(ctx context.Context, sessionID string, dryRun bool) (*TurnDedupeResult, error)
}

var (
	_ AgentSessionDeleter = (*MemStore)(nil)
	_ AgentSessionDeleter = (*PostgresStore)(nil)
)

// sameToolCalls compares tool_calls as JSON values (jsonb equality), so
// formatting differences do not matter.
func sameToolCalls(a, b json.RawMessage) bool {
	norm := func(raw json.RawMessage) []byte {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = json.RawMessage("[]")
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return bytes.TrimSpace(raw)
		}
		out, _ := json.Marshal(v)
		return out
	}
	return bytes.Equal(norm(a), norm(b))
}

// duplicateTurnIdx returns the idx of each undated turn that copies a dated one.
func duplicateTurnIdx(turns []SessionTurn) []int {
	var out []int
	for _, t := range turns {
		if t.SourceAt != nil {
			continue
		}
		for _, o := range turns {
			if o.SourceAt != nil && o.Role == t.Role && o.TextPreview == t.TextPreview && sameToolCalls(o.ToolCalls, t.ToolCalls) {
				out = append(out, t.Idx)
				break
			}
		}
	}
	sort.Ints(out)
	return out
}

func (m *MemStore) DeleteAgentSession(ctx context.Context, sessionID string) (*SessionDeleteResult, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	m.ensureAgentMaps()
	row := m.agentSessions[sessionID]
	if row == nil {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	res := &SessionDeleteResult{
		SessionID: sessionID,
		Turns:     len(m.sessionTurns[sessionID]),
		Versions:  len(m.sessionVersions[sessionID]),
		Grants:    len(m.agentGrants[sessionID]),
		Usage:     len(m.sessionUsage[sessionID]),
	}
	delete(m.agentSessions, sessionID)
	delete(m.agentByNative, nativeKey(row.ProjectID, row.Harness, row.NativeID, row.OriginMachineID))
	delete(m.sessionTurns, sessionID)
	delete(m.sessionVersions, sessionID)
	delete(m.agentGrants, sessionID)
	delete(m.sessionUsage, sessionID)
	for _, other := range m.agentSessions {
		if other.ParentSessionID == sessionID {
			other.ParentSessionID = ""
		}
	}
	for id, tp := range m.teleports {
		if tp.SessionID == sessionID {
			delete(m.teleports, id)
		}
	}
	keptChanges := m.codeChanges[:0]
	for _, c := range m.codeChanges {
		if c.SessionID != sessionID {
			keptChanges = append(keptChanges, c)
		}
	}
	m.codeChanges = keptChanges
	keptSnaps := make([]SessionSnapshot, 0, len(m.snapshots))
	for _, s := range m.snapshots {
		if s.SessionID == sessionID {
			res.Snapshots++
			continue
		}
		keptSnaps = append(keptSnaps, s)
	}
	m.snapshots = keptSnaps
	keptOps := make([]FileOperation, 0, len(m.fileOps))
	for _, op := range m.fileOps {
		if op.SessionID != sessionID {
			keptOps = append(keptOps, op)
		}
	}
	m.fileOps = keptOps
	keptExecs := make([]ToolExecution, 0, len(m.toolExecs))
	for _, te := range m.toolExecs {
		if te.SessionID != sessionID {
			keptExecs = append(keptExecs, te)
		}
	}
	m.toolExecs = keptExecs
	delete(m.sessionOwners, sessionID)
	delete(m.sessionGrants, sessionID)
	m.mu.Unlock()

	book := guestBook(m)
	book.mu.Lock()
	for id, g := range book.byID {
		if g.SessionID == sessionID {
			delete(book.byID, id)
			delete(book.byToken, g.Token)
		}
	}
	book.mu.Unlock()
	return res, nil
}

func (m *MemStore) DedupeSessionTurns(ctx context.Context, sessionID string, dryRun bool) (*TurnDedupeResult, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	row := m.agentSessions[sessionID]
	if row == nil {
		return nil, ErrNotFound
	}
	turns := m.sessionTurns[sessionID]
	dups := duplicateTurnIdx(turns)
	res := &TurnDedupeResult{Removed: len(dups), Kept: len(turns) - len(dups), RemovedIdx: dups, DryRun: dryRun}
	if res.RemovedIdx == nil {
		res.RemovedIdx = []int{}
	}
	if dryRun || len(dups) == 0 {
		return res, nil
	}
	drop := make(map[int]bool, len(dups))
	for _, idx := range dups {
		drop[idx] = true
	}
	kept := make([]SessionTurn, 0, len(turns)-len(dups))
	for _, t := range turns {
		if !drop[t.Idx] {
			kept = append(kept, t)
		}
	}
	m.sessionTurns[sessionID] = kept
	// The copies were stamped with upload time; once every turn left is
	// dated, the chat's activity is its newest source time.
	if latest, ok := latestSourceTime(kept); ok {
		row.LastActiveAt = latest
	}
	versions := m.sessionVersions[sessionID]
	for i := range versions {
		if versions[i].TurnCount > len(kept) {
			versions[i].TurnCount = len(kept)
		}
	}
	return res, nil
}

func latestSourceTime(turns []SessionTurn) (time.Time, bool) {
	var latest time.Time
	for _, t := range turns {
		if t.SourceAt == nil {
			return time.Time{}, false
		}
		if t.SourceAt.After(latest) {
			latest = *t.SourceAt
		}
	}
	return latest, !latest.IsZero()
}

func (s *PostgresStore) DeleteAgentSession(ctx context.Context, sessionID string) (*SessionDeleteResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM agent_sessions WHERE id = $1::uuid FOR UPDATE`, sessionID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	res := &SessionDeleteResult{SessionID: id}
	if err := tx.QueryRow(ctx, `SELECT
			(SELECT COUNT(*) FROM session_turns WHERE session_id = $1::uuid),
			(SELECT COUNT(*) FROM session_versions WHERE session_id = $1::uuid),
			(SELECT COUNT(*) FROM session_grants WHERE session_id = $1::uuid),
			(SELECT COUNT(*) FROM session_usage WHERE session_id = $1::uuid)`, id).
		Scan(&res.Turns, &res.Versions, &res.Grants, &res.Usage); err != nil {
		return nil, err
	}
	// Tables keyed by session id without a foreign key to agent_sessions
	// (migrations 028/029). Everything else cascades from agent_sessions:
	// turns, versions, grants, usage, guest links, teleports, code changes;
	// forks keep their rows with parent_session_id set to NULL. Memories
	// are not touched.
	tag, err := tx.Exec(ctx, `DELETE FROM session_snapshots WHERE session_id = $1::uuid`, id)
	if err != nil {
		return nil, fmt.Errorf("delete snapshots: %w", err)
	}
	res.Snapshots = int(tag.RowsAffected())
	for _, table := range []string{"session_file_operations", "session_tool_executions", "session_content_grants", "session_content_owners"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE session_id = $1::uuid`, id); err != nil {
			return nil, fmt.Errorf("delete %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM agent_sessions WHERE id = $1::uuid`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *PostgresStore) DedupeSessionTurns(ctx context.Context, sessionID string, dryRun bool) (*TurnDedupeResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM agent_sessions WHERE id = $1::uuid FOR UPDATE`, sessionID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// text_preview is stored NULLIF(...,''), so COALESCE makes empty and
	// missing text equal; jsonb = compares tool_calls as values.
	rows, err := tx.Query(ctx, `
		SELECT t.idx FROM session_turns t
		WHERE t.session_id = $1::uuid AND t.source_ts IS NULL
		  AND EXISTS (SELECT 1 FROM session_turns o
		              WHERE o.session_id = t.session_id AND o.source_ts IS NOT NULL
		                AND o.role = t.role
		                AND COALESCE(o.text_preview, '') = COALESCE(t.text_preview, '')
		                AND o.tool_calls = t.tool_calls)
		ORDER BY t.idx`, id)
	if err != nil {
		return nil, err
	}
	dups := []int{}
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			rows.Close()
			return nil, err
		}
		dups = append(dups, idx)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM session_turns WHERE session_id = $1::uuid`, id).Scan(&total); err != nil {
		return nil, err
	}
	res := &TurnDedupeResult{Removed: len(dups), Kept: total - len(dups), RemovedIdx: dups, DryRun: dryRun}
	if dryRun || len(dups) == 0 {
		return res, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_turns WHERE session_id = $1::uuid AND idx = ANY($2::bigint[])`, id, dups); err != nil {
		return nil, err
	}
	// Same rule as AppendSessionTurn's repair: a fully dated history dates
	// the chat (the copies carried upload time).
	if _, err := tx.Exec(ctx, `UPDATE agent_sessions s
		SET last_active_at = t.latest
		FROM (SELECT MAX(source_ts) AS latest FROM session_turns WHERE session_id = $1::uuid) t
		WHERE s.id = $1::uuid AND t.latest IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM session_turns WHERE session_id = $1::uuid AND source_ts IS NULL)`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE session_versions SET turn_count = $2
		WHERE session_id = $1::uuid AND turn_count > $2`, id, res.Kept); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
