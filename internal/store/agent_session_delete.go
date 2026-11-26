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

// Turn clean-up rules. Each removes only undated turns (source_ts NULL);
// dated turns are never touched.
const (
	// DedupeRuleExact: an exact copy (role, text, tool_calls) of a dated turn.
	DedupeRuleExact = "exact"
	// DedupeRulePartialCopy: same role and trimmed text as a dated turn, and
	// every tool call it carries (possibly none) is already stored on a dated
	// turn of that role, matched by id, or by value when the call has no id.
	// Old captures stored an assistant message once undated with all its
	// tool calls, and again dated as a text turn plus one turn per call.
	DedupeRulePartialCopy = "partial_copy"
	// DedupeRuleEnvelope: a user turn with no tool calls whose text is
	// nothing but harness envelopes (<system-reminder>, <task-notification>,
	// <agent-message> blocks). The capture no longer stores these.
	DedupeRuleEnvelope = "envelope"
)

// Modes for DedupeSessionTurns: which rules run.
const (
	DedupeModeAll       = "all"       // exact, partial_copy and envelope (default)
	DedupeModeExact     = "exact"     // exact copies only
	DedupeModeCopies    = "copies"    // exact and partial_copy
	DedupeModeEnvelopes = "envelopes" // envelope only
)

// ErrBadDedupeMode is returned for an unknown mode.
var ErrBadDedupeMode = errors.New("mode must be all, exact, copies or envelopes")

// TurnDedupeOptions selects the rules and whether to only count.
type TurnDedupeOptions struct {
	Mode   string
	DryRun bool
}

// NormalizeDedupeMode maps "" to DedupeModeAll and rejects unknown modes.
func NormalizeDedupeMode(mode string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(mode)); m {
	case "":
		return DedupeModeAll, nil
	case DedupeModeAll, DedupeModeExact, DedupeModeCopies, DedupeModeEnvelopes:
		return m, nil
	}
	return "", ErrBadDedupeMode
}

// TurnDedupeResult is the outcome of DedupeSessionTurns. Removed is the
// number of turns removed (or that would be, on a dry run); Kept is the
// number of turns the session has afterwards. RemovedIdx lists them and
// Rules counts them per rule.
type TurnDedupeResult struct {
	Mode       string         `json:"mode"`
	Removed    int            `json:"removed"`
	Kept       int            `json:"kept"`
	RemovedIdx []int          `json:"removed_idx"`
	Rules      map[string]int `json:"rules"`
	DryRun     bool           `json:"dry_run"`
}

// AgentSessionDeleter removes a chat or its duplicate turns.
type AgentSessionDeleter interface {
	// DeleteAgentSession removes the session with its turns, versions,
	// grants, usage, guest links, teleports, code changes, snapshots and
	// provenance. Memories made from it stay. ErrNotFound when missing.
	DeleteAgentSession(ctx context.Context, sessionID string) (*SessionDeleteResult, error)
	// DedupeSessionTurns removes undated turns (source_ts NULL) matched by
	// the rules opts.Mode selects (see DedupeRule*): copies of dated turns,
	// which Claude Code api-request-blob lines used to be stored as, and
	// harness-only user envelopes. ErrBadDedupeMode for an unknown mode.
	DedupeSessionTurns(ctx context.Context, sessionID string, opts TurnDedupeOptions) (*TurnDedupeResult, error)
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

// toolCallKeys returns one key per tool call: its id when it has one,
// otherwise its JSON value. ok is false when the JSON is not an array.
func toolCallKeys(raw json.RawMessage) (keys []string, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, true
	}
	var calls []json.RawMessage
	if err := json.Unmarshal(trimmed, &calls); err != nil {
		return nil, false
	}
	for _, c := range calls {
		var head struct {
			ID any `json:"id"`
		}
		if err := json.Unmarshal(c, &head); err == nil {
			if id, isStr := head.ID.(string); isStr && strings.TrimSpace(id) != "" {
				keys = append(keys, "id:"+strings.TrimSpace(id))
				continue
			}
		}
		var v any
		if err := json.Unmarshal(c, &v); err != nil {
			return nil, false
		}
		norm, _ := json.Marshal(v)
		keys = append(keys, "v:"+string(norm))
	}
	return keys, true
}

// envelopeTags are the harness wrappers a user turn may consist of.
var envelopeTags = []string{"system-reminder", "task-notification", "agent-message"}

// isHarnessEnvelope reports whether text, trimmed, is one or more whole
// envelope blocks and nothing else. Truncated text (no closing tag) and
// text with anything outside the blocks are not envelopes.
func isHarnessEnvelope(text string) bool {
	s := strings.TrimSpace(text)
	if s == "" {
		return false
	}
	for s != "" {
		matched := false
		for _, tag := range envelopeTags {
			open := "<" + tag
			if !strings.HasPrefix(s, open) || len(s) == len(open) {
				continue
			}
			if next := s[len(open)]; next != '>' && next != ' ' && next != '\t' && next != '\n' && next != '\r' {
				continue
			}
			closeTag := "</" + tag + ">"
			end := strings.Index(s, closeTag)
			if end < 0 {
				return false
			}
			s = strings.TrimSpace(s[end+len(closeTag):])
			matched = true
			break
		}
		if !matched {
			return false
		}
	}
	return true
}

// duplicateTurns classifies, by idx, the undated turns that the rules of
// mode (already normalized) remove.
func duplicateTurns(turns []SessionTurn, mode string) map[int]string {
	exact := mode == DedupeModeAll || mode == DedupeModeExact || mode == DedupeModeCopies
	partial := mode == DedupeModeAll || mode == DedupeModeCopies
	envelope := mode == DedupeModeAll || mode == DedupeModeEnvelopes
	// Trimmed texts and tool calls of dated turns, per role.
	datedText := map[string]bool{}
	datedCalls := map[string]map[string]bool{}
	if partial {
		for _, o := range turns {
			if o.SourceAt == nil {
				continue
			}
			datedText[o.Role+"\x00"+strings.TrimSpace(o.TextPreview)] = true
			keys, _ := toolCallKeys(o.ToolCalls)
			if len(keys) > 0 && datedCalls[o.Role] == nil {
				datedCalls[o.Role] = map[string]bool{}
			}
			for _, k := range keys {
				datedCalls[o.Role][k] = true
			}
		}
	}
	out := map[int]string{}
	for _, t := range turns {
		if t.SourceAt != nil {
			continue
		}
		if exact {
			found := false
			for _, o := range turns {
				if o.SourceAt != nil && o.Role == t.Role && o.TextPreview == t.TextPreview && sameToolCalls(o.ToolCalls, t.ToolCalls) {
					found = true
					break
				}
			}
			if found {
				out[t.Idx] = DedupeRuleExact
				continue
			}
		}
		if partial && datedText[t.Role+"\x00"+strings.TrimSpace(t.TextPreview)] {
			keys, covered := toolCallKeys(t.ToolCalls)
			for _, k := range keys {
				if !datedCalls[t.Role][k] {
					covered = false
					break
				}
			}
			if covered {
				out[t.Idx] = DedupeRulePartialCopy
				continue
			}
		}
		if envelope && t.Role == "user" && isHarnessEnvelope(t.TextPreview) {
			if keys, ok := toolCallKeys(t.ToolCalls); ok && len(keys) == 0 {
				out[t.Idx] = DedupeRuleEnvelope
			}
		}
	}
	return out
}

// newDedupeResult builds the result for the classified turns.
func newDedupeResult(found map[int]string, total int, opts TurnDedupeOptions) *TurnDedupeResult {
	res := &TurnDedupeResult{
		Mode: opts.Mode, Removed: len(found), Kept: total - len(found), DryRun: opts.DryRun,
		RemovedIdx: make([]int, 0, len(found)),
		Rules:      map[string]int{DedupeRuleExact: 0, DedupeRulePartialCopy: 0, DedupeRuleEnvelope: 0},
	}
	for idx, rule := range found {
		res.RemovedIdx = append(res.RemovedIdx, idx)
		res.Rules[rule]++
	}
	sort.Ints(res.RemovedIdx)
	return res
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
	m.deleteSessionUserStates(sessionID)
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

func (m *MemStore) DedupeSessionTurns(ctx context.Context, sessionID string, opts TurnDedupeOptions) (*TurnDedupeResult, error) {
	_ = ctx
	mode, err := NormalizeDedupeMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	opts.Mode = mode
	sessionID = strings.TrimSpace(sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureAgentMaps()
	row := m.agentSessions[sessionID]
	if row == nil {
		return nil, ErrNotFound
	}
	turns := m.sessionTurns[sessionID]
	drop := duplicateTurns(turns, mode)
	res := newDedupeResult(drop, len(turns), opts)
	if opts.DryRun || len(drop) == 0 {
		return res, nil
	}
	kept := make([]SessionTurn, 0, len(turns)-len(drop))
	for _, t := range turns {
		if _, gone := drop[t.Idx]; !gone {
			kept = append(kept, t)
		}
	}
	m.sessionTurns[sessionID] = kept
	// The removed turns were stamped with upload time; once every turn left
	// is dated, the chat's activity is its newest source time.
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
	// Everyone's pins, archive marks and custom titles (migration 055).
	if _, err := tx.Exec(ctx, `DELETE FROM agent_session_user_state WHERE session_id = $1`, id); err != nil {
		return nil, fmt.Errorf("delete agent_session_user_state: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM agent_sessions WHERE id = $1::uuid`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *PostgresStore) DedupeSessionTurns(ctx context.Context, sessionID string, opts TurnDedupeOptions) (*TurnDedupeResult, error) {
	mode, err := NormalizeDedupeMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	opts.Mode = mode
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
	// The rules run in Go (duplicateTurns) so both stores agree. Only the
	// fields they read are loaded; a dated turn gets a placeholder time.
	// text_preview is stored NULLIF(...,''), so COALESCE makes empty and
	// missing text equal.
	rows, err := tx.Query(ctx, `
		SELECT idx, role, source_ts IS NOT NULL, COALESCE(text_preview, ''), COALESCE(tool_calls, '[]'::jsonb)
		FROM session_turns WHERE session_id = $1::uuid ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	var turns []SessionTurn
	dated := time.Unix(0, 0).UTC()
	for rows.Next() {
		var (
			t       SessionTurn
			isDated bool
			calls   []byte
		)
		if err := rows.Scan(&t.Idx, &t.Role, &isDated, &t.TextPreview, &calls); err != nil {
			rows.Close()
			return nil, err
		}
		if isDated {
			t.SourceAt = &dated
		}
		t.ToolCalls = json.RawMessage(calls)
		turns = append(turns, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	drop := duplicateTurns(turns, mode)
	res := newDedupeResult(drop, len(turns), opts)
	if opts.DryRun || len(drop) == 0 {
		return res, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_turns WHERE session_id = $1::uuid AND source_ts IS NULL AND idx = ANY($2::bigint[])`, id, res.RemovedIdx); err != nil {
		return nil, err
	}
	// Same rule as AppendSessionTurn's repair: a fully dated history dates
	// the chat (the removed turns carried upload time).
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
