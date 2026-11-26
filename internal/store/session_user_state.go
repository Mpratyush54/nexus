package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// Per-person chat state (migration 055): a custom title, pinned and
// archived. The state belongs to the viewer, not the chat, so the stored
// agent_sessions.title (overwritten by every capture) is never changed.

// SessionUserStateTitleMax is the longest custom title kept, in runes.
const SessionUserStateTitleMax = 200

// SessionUserState is one person's state for one chat. An empty Title means
// the chat is not renamed.
type SessionUserState struct {
	SessionID  string     `json:"session_id"`
	Title      string     `json:"title,omitempty"`
	PinnedAt   *time.Time `json:"pinned_at,omitempty"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

// Empty reports whether the state holds nothing (its row is removed).
func (s SessionUserState) Empty() bool {
	return s.Title == "" && s.PinnedAt == nil && s.ArchivedAt == nil
}

// SessionUserStatePatch changes some of the state; nil fields are kept.
// An empty Title clears the rename. Pinning un-archives; archiving unpins.
type SessionUserStatePatch struct {
	Title    *string
	Pinned   *bool
	Archived *bool
}

// SessionUserStateStore keeps per-person chat state.
type SessionUserStateStore interface {
	// SetSessionUserState applies p and returns the resulting state (the
	// zero state with SessionID set when everything is cleared).
	SetSessionUserState(ctx context.Context, userID, sessionID string, p SessionUserStatePatch) (*SessionUserState, error)
	// ListSessionUserStates returns the caller's non-empty states by
	// session id.
	ListSessionUserStates(ctx context.Context, userID string) (map[string]SessionUserState, error)
	// SessionTurnCounts returns the timeline turn count (the larger of the
	// latest complete version's count and the stored turns) per session id.
	// Ids without an agent session are absent.
	SessionTurnCounts(ctx context.Context, sessionIDs []string) (map[string]int, error)
}

var (
	_ SessionUserStateStore = (*MemStore)(nil)
	_ SessionUserStateStore = (*PostgresStore)(nil)
)

// ErrBadSessionID is returned for an empty session id.
var ErrBadSessionID = errors.New("session id is required")

// CleanSessionTitle trims a custom title, drops control characters
// (newlines and tabs become spaces) and cuts it to SessionUserStateTitleMax
// runes.
func CleanSessionTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(title, "") {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		} else if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '‍' {
			continue // C0/C1 controls and invisible format characters (bidi overrides)
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(out); len(runes) > SessionUserStateTitleMax {
		out = strings.TrimSpace(string(runes[:SessionUserStateTitleMax]))
	}
	return out
}

// applySessionUserStatePatch applies p to cur at now.
func applySessionUserStatePatch(cur SessionUserState, p SessionUserStatePatch, now time.Time) SessionUserState {
	if p.Title != nil {
		cur.Title = CleanSessionTitle(*p.Title)
	}
	if p.Archived != nil {
		if *p.Archived {
			if cur.ArchivedAt == nil {
				t := now
				cur.ArchivedAt = &t
			}
			cur.PinnedAt = nil
		} else {
			cur.ArchivedAt = nil
		}
	}
	if p.Pinned != nil {
		if *p.Pinned {
			if cur.PinnedAt == nil {
				t := now
				cur.PinnedAt = &t
			}
			cur.ArchivedAt = nil
		} else {
			cur.PinnedAt = nil
		}
	}
	return cur
}

func (m *MemStore) SetSessionUserState(ctx context.Context, userID, sessionID string, p SessionUserStatePatch) (*SessionUserState, error) {
	_ = ctx
	userID, sessionID = strings.TrimSpace(userID), strings.TrimSpace(sessionID)
	if sessionID == "" || userID == "" {
		return nil, ErrBadSessionID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionUserStates == nil {
		m.sessionUserStates = map[string]map[string]SessionUserState{}
	}
	cur := m.sessionUserStates[userID][sessionID]
	cur.SessionID = sessionID
	next := applySessionUserStatePatch(cur, p, time.Now().UTC())
	if next.Empty() {
		delete(m.sessionUserStates[userID], sessionID)
	} else {
		if m.sessionUserStates[userID] == nil {
			m.sessionUserStates[userID] = map[string]SessionUserState{}
		}
		m.sessionUserStates[userID][sessionID] = next
	}
	return &next, nil
}

func (m *MemStore) ListSessionUserStates(ctx context.Context, userID string) (map[string]SessionUserState, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]SessionUserState, len(m.sessionUserStates[strings.TrimSpace(userID)]))
	for id, st := range m.sessionUserStates[strings.TrimSpace(userID)] {
		out[id] = st
	}
	return out, nil
}

func (m *MemStore) SessionTurnCounts(ctx context.Context, sessionIDs []string) (map[string]int, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int, len(sessionIDs))
	for _, id := range sessionIDs {
		if m.agentSessions[id] == nil {
			continue
		}
		n, best := 0, -1
		for _, v := range m.sessionVersions[id] {
			if (v.State == "complete" || v.State == "transcript_only") && v.Version > best {
				best, n = v.Version, v.TurnCount
			}
		}
		if c := len(m.sessionTurns[id]); c > n {
			n = c
		}
		out[id] = n
	}
	return out, nil
}

// deleteSessionUserStates drops every person's state for a chat. Caller
// holds m.mu.
func (m *MemStore) deleteSessionUserStates(sessionID string) {
	for _, states := range m.sessionUserStates {
		delete(states, sessionID)
	}
}

func (s *PostgresStore) SetSessionUserState(ctx context.Context, userID, sessionID string, p SessionUserStatePatch) (*SessionUserState, error) {
	userID, sessionID = strings.TrimSpace(userID), strings.TrimSpace(sessionID)
	if sessionID == "" || userID == "" {
		return nil, ErrBadSessionID
	}
	if !looksLikeUUID(userID) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cur := SessionUserState{SessionID: sessionID}
	var title *string
	err = tx.QueryRow(ctx, `SELECT title, pinned_at, archived_at FROM agent_session_user_state
		WHERE user_id = $1::uuid AND session_id = $2 FOR UPDATE`, userID, sessionID).
		Scan(&title, &cur.PinnedAt, &cur.ArchivedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if title != nil {
		cur.Title = *title
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, err
	}
	next := applySessionUserStatePatch(cur, p, now.UTC())
	if next.Empty() {
		if _, err := tx.Exec(ctx, `DELETE FROM agent_session_user_state WHERE user_id = $1::uuid AND session_id = $2`, userID, sessionID); err != nil {
			return nil, err
		}
	} else if _, err := tx.Exec(ctx, `INSERT INTO agent_session_user_state (user_id, session_id, title, pinned_at, archived_at, updated_at)
		VALUES ($1::uuid, $2, NULLIF($3, ''), $4, $5, now())
		ON CONFLICT (user_id, session_id) DO UPDATE SET
		  title = EXCLUDED.title, pinned_at = EXCLUDED.pinned_at,
		  archived_at = EXCLUDED.archived_at, updated_at = now()`,
		userID, sessionID, next.Title, next.PinnedAt, next.ArchivedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &next, nil
}

func (s *PostgresStore) ListSessionUserStates(ctx context.Context, userID string) (map[string]SessionUserState, error) {
	userID = strings.TrimSpace(userID)
	out := map[string]SessionUserState{}
	if !looksLikeUUID(userID) {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id, COALESCE(title, ''), pinned_at, archived_at
		FROM agent_session_user_state WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st SessionUserState
		if err := rows.Scan(&st.SessionID, &st.Title, &st.PinnedAt, &st.ArchivedAt); err != nil {
			return nil, err
		}
		if !st.Empty() {
			out[st.SessionID] = st
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) SessionTurnCounts(ctx context.Context, sessionIDs []string) (map[string]int, error) {
	out := map[string]int{}
	ids := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		if looksLikeUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT s.id::text, GREATEST(COALESCE((
		         SELECT v.turn_count FROM session_versions v
		         WHERE v.session_id = s.id AND v.state IN ('complete','transcript_only')
		         ORDER BY v.version DESC LIMIT 1
		       ), 0), (SELECT COUNT(*) FROM session_turns t WHERE t.session_id = s.id)::int)
		FROM agent_sessions s WHERE s.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
