package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// What the person's desktop agents are doing in each chat (migration 056),
// so the web chat list shows the desktop sidebar's status dots. Nexus
// Desktop reports; the web lists and marks chats read.

// Chat statuses a desktop reports (internal/agentrun).
const (
	ChatLiveRunning     = "running"
	ChatLiveApproval    = "approval"
	ChatLiveQueued      = "queued"
	ChatLiveDone        = "done"
	ChatLiveError       = "error"
	ChatLiveInterrupted = "interrupted"
	ChatLiveIdle        = "idle"
)

// ChatLiveStale is how long a working status (running, approval, queued)
// holds without a report: a desktop that closed or lost its connection
// stops showing as working. Desktops repeat working statuses every minute.
const ChatLiveStale = 3 * time.Minute

// ChatLiveMaxReport bounds one report.
const ChatLiveMaxReport = 200

// ChatLiveReport is one chat's status as a desktop reports it.
type ChatLiveReport struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Unread    bool   `json:"unread"`
}

// ChatLiveStatus is one chat's status as the web lists it.
type ChatLiveStatus struct {
	SessionID string    `json:"session_id"`
	Status    string    `json:"status"`
	Unread    bool      `json:"unread"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatLiveStatusStore keeps the statuses.
type ChatLiveStatusStore interface {
	// PutChatLiveStatus applies a desktop's report and returns the chats it
	// still calls unread that the person has read on the web since.
	PutChatLiveStatus(ctx context.Context, userID string, reports []ChatLiveReport) (read []string, err error)
	// ListChatLiveStatus returns the chats worth a status dot: working
	// (and fresh), or unread.
	ListChatLiveStatus(ctx context.Context, userID string) ([]ChatLiveStatus, error)
	// MarkChatRead clears a chat's unread mark: the person opened it.
	MarkChatRead(ctx context.Context, userID, sessionID string) error
}

var (
	_ ChatLiveStatusStore = (*MemStore)(nil)
	_ ChatLiveStatusStore = (*PostgresStore)(nil)
)

// ErrBadChatLiveReport is returned for a report that isn't valid.
var ErrBadChatLiveReport = errors.New("each status needs a chat id and a known status")

// ValidChatLiveReport reports whether r can be stored.
func ValidChatLiveReport(r ChatLiveReport) bool {
	id := r.SessionID
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	switch r.Status {
	case ChatLiveRunning, ChatLiveApproval, ChatLiveQueued, ChatLiveDone, ChatLiveError, ChatLiveInterrupted, ChatLiveIdle:
		return true
	}
	return false
}

func chatLiveWorking(status string) bool {
	return status == ChatLiveRunning || status == ChatLiveApproval || status == ChatLiveQueued
}

// chatLiveRow is the stored state of one chat.
type chatLiveRow struct {
	Status    string
	Unread    bool
	StatusAt  time.Time
	ReadAt    *time.Time
	UpdatedAt time.Time
}

// applyChatLiveReport merges a report into the stored row. A new status
// takes the report's unread mark. The same status reported unread again is
// kept read when the person read the chat after that status began, and
// read says so, so the desktop clears its own mark too.
func applyChatLiveReport(cur chatLiveRow, exists bool, r ChatLiveReport, now time.Time) (next chatLiveRow, read bool) {
	next = cur
	next.UpdatedAt = now
	if !exists || r.Status != cur.Status {
		next.Status, next.StatusAt, next.Unread = r.Status, now, r.Unread
		return next, false
	}
	if r.Unread && cur.ReadAt != nil && !cur.ReadAt.Before(cur.StatusAt) {
		next.Unread = false
		return next, true
	}
	next.Unread = r.Unread
	return next, false
}

// keepChatLiveRow reports whether a row is worth keeping.
func keepChatLiveRow(row chatLiveRow) bool {
	return row.Status != ChatLiveIdle || row.Unread
}

// shownChatLive is what the web shows for a row at now, if anything.
func shownChatLive(row chatLiveRow, now time.Time) (ChatLiveStatus, bool) {
	status := row.Status
	if chatLiveWorking(status) && now.Sub(row.UpdatedAt) > ChatLiveStale {
		status = ChatLiveIdle
	}
	if status == ChatLiveIdle && !row.Unread {
		return ChatLiveStatus{}, false
	}
	if status == ChatLiveIdle {
		status = ChatLiveDone
	}
	return ChatLiveStatus{Status: status, Unread: row.Unread, UpdatedAt: row.UpdatedAt}, true
}

func cleanChatLiveReports(reports []ChatLiveReport) ([]ChatLiveReport, error) {
	if len(reports) > ChatLiveMaxReport {
		return nil, ErrBadChatLiveReport
	}
	// The last report for a chat wins.
	seen := map[string]int{}
	out := make([]ChatLiveReport, 0, len(reports))
	for _, r := range reports {
		if !ValidChatLiveReport(r) {
			return nil, ErrBadChatLiveReport
		}
		if i, ok := seen[r.SessionID]; ok {
			out[i] = r
			continue
		}
		seen[r.SessionID] = len(out)
		out = append(out, r)
	}
	return out, nil
}

func (m *MemStore) PutChatLiveStatus(ctx context.Context, userID string, reports []ChatLiveReport) ([]string, error) {
	_ = ctx
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrBadSessionID
	}
	reports, err := cleanChatLiveReports(reports)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chatLiveStatus == nil {
		m.chatLiveStatus = map[string]map[string]chatLiveRow{}
	}
	rows := m.chatLiveStatus[userID]
	if rows == nil {
		rows = map[string]chatLiveRow{}
		m.chatLiveStatus[userID] = rows
	}
	read := []string{}
	for _, r := range reports {
		cur, ok := rows[r.SessionID]
		next, wasRead := applyChatLiveReport(cur, ok, r, now)
		if wasRead {
			read = append(read, r.SessionID)
		}
		if keepChatLiveRow(next) {
			rows[r.SessionID] = next
		} else {
			delete(rows, r.SessionID)
		}
	}
	return read, nil
}

func (m *MemStore) ListChatLiveStatus(ctx context.Context, userID string) ([]ChatLiveStatus, error) {
	_ = ctx
	now := time.Now().UTC()
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []ChatLiveStatus{}
	for id, row := range m.chatLiveStatus[strings.TrimSpace(userID)] {
		if st, ok := shownChatLive(row, now); ok {
			st.SessionID = id
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}

func (m *MemStore) MarkChatRead(ctx context.Context, userID, sessionID string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.chatLiveStatus[strings.TrimSpace(userID)]
	row, ok := rows[sessionID]
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	row.Unread, row.ReadAt = false, &now
	if keepChatLiveRow(row) {
		rows[sessionID] = row
	} else {
		delete(rows, sessionID)
	}
	return nil
}

func (s *PostgresStore) PutChatLiveStatus(ctx context.Context, userID string, reports []ChatLiveReport) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if !looksLikeUUID(userID) {
		return nil, ErrNotFound
	}
	reports, err := cleanChatLiveReports(reports)
	if err != nil {
		return nil, err
	}
	read := []string{}
	if len(reports) == 0 {
		return read, nil
	}
	ids := make([]string, len(reports))
	for i, r := range reports {
		ids[i] = r.SessionID
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT session_id, status, unread, status_at, read_at, updated_at
		FROM agent_session_live_status WHERE user_id = $1::uuid AND session_id = ANY($2::text[]) FOR UPDATE`, userID, ids)
	if err != nil {
		return nil, err
	}
	current := map[string]chatLiveRow{}
	for rows.Next() {
		var id string
		var row chatLiveRow
		if err := rows.Scan(&id, &row.Status, &row.Unread, &row.StatusAt, &row.ReadAt, &row.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		current[id] = row
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	batch := &pgx.Batch{}
	for _, r := range reports {
		cur, ok := current[r.SessionID]
		next, wasRead := applyChatLiveReport(cur, ok, r, now)
		if wasRead {
			read = append(read, r.SessionID)
		}
		if !keepChatLiveRow(next) {
			if ok {
				batch.Queue(`DELETE FROM agent_session_live_status WHERE user_id = $1::uuid AND session_id = $2`, userID, r.SessionID)
			}
			continue
		}
		batch.Queue(`INSERT INTO agent_session_live_status (user_id, session_id, status, unread, status_at, read_at, updated_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id, session_id) DO UPDATE SET status = EXCLUDED.status, unread = EXCLUDED.unread,
			  status_at = EXCLUDED.status_at, read_at = EXCLUDED.read_at, updated_at = EXCLUDED.updated_at`,
			userID, r.SessionID, next.Status, next.Unread, next.StatusAt, next.ReadAt, next.UpdatedAt)
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return read, nil
}

func (s *PostgresStore) ListChatLiveStatus(ctx context.Context, userID string) ([]ChatLiveStatus, error) {
	userID = strings.TrimSpace(userID)
	out := []ChatLiveStatus{}
	if !looksLikeUUID(userID) {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id, status, unread, status_at, read_at, updated_at, now()
		FROM agent_session_live_status WHERE user_id = $1::uuid ORDER BY session_id LIMIT 1000`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var row chatLiveRow
		var now time.Time
		if err := rows.Scan(&id, &row.Status, &row.Unread, &row.StatusAt, &row.ReadAt, &row.UpdatedAt, &now); err != nil {
			return nil, err
		}
		if st, ok := shownChatLive(row, now); ok {
			st.SessionID = id
			out = append(out, st)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkChatRead(ctx context.Context, userID, sessionID string) error {
	userID = strings.TrimSpace(userID)
	if !looksLikeUUID(userID) || sessionID == "" {
		return nil
	}
	// An idle chat read is nothing to keep; any other keeps its status.
	if _, err := s.pool.Exec(ctx, `DELETE FROM agent_session_live_status
		WHERE user_id = $1::uuid AND session_id = $2 AND status = 'idle'`, userID, sessionID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_session_live_status SET unread = false, read_at = now()
		WHERE user_id = $1::uuid AND session_id = $2`, userID, sessionID)
	return err
}
