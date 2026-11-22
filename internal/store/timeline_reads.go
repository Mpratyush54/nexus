package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Read paths behind the chat list and the chat page. They return pages and
// metadata only, so a request never loads every snapshot, every transcript
// or every turn of a long session to show one screen.

// LegacyTimelineQuery pages the latest snapshot of each archived session a
// viewer can read, newest first.
type LegacyTimelineQuery struct {
	UserID string
	// ProjectID limits the page to one project the caller already
	// authorized. Empty means every project the viewer can open.
	ProjectID string
	// Harness matches case-insensitively; empty means any.
	Harness string
	// CursorAt/CursorID exclude rows at or before (newer than or equal to)
	// the last row of the previous page. Zero CursorAt is the first page.
	CursorAt time.Time
	CursorID string
	Limit    int
}

// LegacyTimelineStore is implemented by MemStore and PostgresStore.
type LegacyTimelineStore interface {
	// ListLegacyTimeline returns metadata only (no payloads). A row is listed
	// when the viewer owns the snapshot or holds an active content grant on
	// it, and no agent session the viewer can read in those projects has the
	// same harness and native id (the chat was captured again).
	ListLegacyTimeline(ctx context.Context, q LegacyTimelineQuery) ([]SessionSnapshot, error)
	// LatestSnapshotTranscripts returns the latest transcript payload per
	// session id. Missing ids are absent from the map.
	LatestSnapshotTranscripts(ctx context.Context, sessionIDs []string) (map[string][]byte, error)
	// GetLatestSnapshotMeta is GetLatestSnapshot without the payload columns.
	GetLatestSnapshotMeta(ctx context.Context, sessionID string) (*SessionSnapshot, error)
}

// SessionTurnReader pages turns and reads the few turns a summary or the
// files and commands views need, without loading the whole session.
type SessionTurnReader interface {
	// ListSessionTurnsPage returns turns with idx > *after (all when nil) in
	// idx order, at most limit of them, and whether more remain.
	ListSessionTurnsPage(ctx context.Context, sessionID string, after *int, limit int) ([]SessionTurn, bool, error)
	// SessionTurnOverview returns the first user turn and the last assistant
	// turn whose text is not blank (zero values when there is none).
	SessionTurnOverview(ctx context.Context, sessionID string) (firstUser, lastAssistant SessionTurn, err error)
	// ListSessionToolCallTurns returns, in idx order, every turn that may
	// carry tool calls: a non-empty tool_calls array, or a text preview that
	// could hold a serialized envelope. Callers apply the exact rule.
	ListSessionToolCallTurns(ctx context.Context, sessionID string) ([]SessionTurn, error)
}

var (
	_ LegacyTimelineStore = (*MemStore)(nil)
	_ LegacyTimelineStore = (*PostgresStore)(nil)
	_ SessionTurnReader   = (*MemStore)(nil)
	_ SessionTurnReader   = (*PostgresStore)(nil)
)

func legacyBefore(at time.Time, id string, cursorAt time.Time, cursorID string) bool {
	if cursorAt.IsZero() {
		return true
	}
	return at.Before(cursorAt) || (at.Equal(cursorAt) && id < cursorID)
}

// ListLegacyTimeline: see LegacyTimelineStore.
func (m *MemStore) ListLegacyTimeline(ctx context.Context, q LegacyTimelineQuery) ([]SessionSnapshot, error) {
	userID := strings.TrimSpace(q.UserID)
	if userID == "" {
		return []SessionSnapshot{}, nil
	}
	projects := map[string]bool{}
	if q.ProjectID != "" {
		projects[q.ProjectID] = true
	} else {
		list, err := m.ListProjectsForUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			if p != nil && p.ID != "" {
				projects[p.ID] = true
			}
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	captured := map[string]bool{}
	for _, sess := range m.agentSessions {
		if sess != nil && projects[sess.ProjectID] && m.agentVisibleLocked(userID, sess) {
			captured[sess.Harness+"\x00"+sess.NativeID] = true
		}
	}
	latest := map[string]SessionSnapshot{}
	for _, s := range m.snapshots {
		if !projects[s.ProjectID] {
			continue
		}
		key := s.ProjectID + "\x00" + s.SessionID
		if cur, ok := latest[key]; !ok || s.SnapshotVersion > cur.SnapshotVersion {
			latest[key] = s
		}
	}
	out := make([]SessionSnapshot, 0, len(latest))
	for _, s := range latest {
		if s.OwnerUserID == "" {
			continue
		}
		if s.OwnerUserID != userID {
			g, ok := m.sessionGrants[s.SessionID][userID]
			if !ok || g.Revoked {
				continue
			}
		}
		if q.Harness != "" && !strings.EqualFold(q.Harness, s.Harness) {
			continue
		}
		if captured[s.Harness+"\x00"+s.ConversationID] {
			continue
		}
		if !legacyBefore(s.UpdatedAt, s.SessionID, q.CursorAt, q.CursorID) {
			continue
		}
		s.UncommittedDiff, s.TranscriptPayload, s.ArtifactsBundle = nil, nil, nil
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].SessionID > out[j].SessionID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// LatestSnapshotTranscripts: see LegacyTimelineStore.
func (m *MemStore) LatestSnapshotTranscripts(ctx context.Context, sessionIDs []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(sessionIDs))
	for _, id := range sessionIDs {
		snap, err := m.GetLatestSnapshot(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = snap.TranscriptPayload
	}
	return out, nil
}

// GetLatestSnapshotMeta: see LegacyTimelineStore.
func (m *MemStore) GetLatestSnapshotMeta(ctx context.Context, sessionID string) (*SessionSnapshot, error) {
	snap, err := m.GetLatestSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	snap.UncommittedDiff, snap.TranscriptPayload, snap.ArtifactsBundle = nil, nil, nil
	return snap, nil
}

// legacyReadableProjectsSQL lists the projects a viewer can open; it matches
// ListProjectsForUser.
const legacyReadableProjectsSQL = `
	SELECT p2.id FROM projects p2 WHERE p2.created_by = $1::uuid
	UNION
	SELECT pm.project_id FROM project_members pm WHERE pm.user_id = $1::uuid
	UNION
	SELECT p3.id FROM projects p3
	JOIN organization_members om ON om.org_id = p3.org_id
	WHERE om.user_id = $1::uuid AND UPPER(om.role) IN ('ADMIN', 'OWNER')`

// ListLegacyTimeline: see LegacyTimelineStore. One query replaces a
// per-project listing plus one grant lookup per snapshot.
func (s *PostgresStore) ListLegacyTimeline(ctx context.Context, q LegacyTimelineQuery) ([]SessionSnapshot, error) {
	if !looksLikeUUID(q.UserID) {
		return []SessionSnapshot{}, nil
	}
	// $2 is the project filter; the all-projects form references it only so
	// both forms take the same parameters.
	projects := `SELECT id FROM (` + legacyReadableProjectsSQL + `) up WHERE $2::text = ''`
	if q.ProjectID != "" {
		if !looksLikeUUID(q.ProjectID) {
			return []SessionSnapshot{}, nil
		}
		projects = `SELECT $2::uuid AS id`
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	var cursorAt any
	if !q.CursorAt.IsZero() {
		cursorAt = q.CursorAt
	}
	rows, err := s.pool.Query(ctx, `
		WITH projs AS (`+projects+`)
		SELECT ss.id::text, ss.session_id::text, ss.snapshot_version, ss.project_id::text, ss.harness,
		       ss.conversation_id, ss.turn_count, COALESCE(ss.source_machine_id,''),
		       ss.owner_user_id::text, ss.created_at, ss.updated_at
		FROM session_snapshots ss
		WHERE ss.project_id IN (SELECT projs.id FROM projs)
		  AND NOT EXISTS (
		    SELECT 1 FROM session_snapshots nv
		    WHERE nv.session_id = ss.session_id AND nv.project_id = ss.project_id
		      AND nv.snapshot_version > ss.snapshot_version
		  )
		  AND ss.owner_user_id IS NOT NULL
		  AND (
		    ss.owner_user_id = $1::uuid
		    OR EXISTS (
		      SELECT 1 FROM session_content_grants g
		      WHERE g.session_id = ss.session_id AND g.grantee_user_id = $1::uuid AND g.revoked_at IS NULL
		    )
		  )
		  AND ($3 = '' OR LOWER(ss.harness) = LOWER($3))
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_sessions a
		    WHERE a.project_id IN (SELECT projs.id FROM projs)
		      AND a.harness = ss.harness AND a.native_id = ss.conversation_id
		      AND (
		        a.owner_user_id = $1::uuid
		        OR EXISTS (
		          SELECT 1 FROM session_grants sg
		          WHERE sg.session_id = a.id AND sg.revoked_at IS NULL AND sg.grantee_user_id = $1::uuid
		        )
		        OR (
		          a.visibility = 'team' AND (
		            EXISTS (SELECT 1 FROM projects p WHERE p.id = a.project_id AND p.created_by = $1::uuid)
		            OR EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = a.project_id AND pm.user_id = $1::uuid)
		          )
		        )
		      )
		  )
		  AND ($4::timestamptz IS NULL OR ss.updated_at < $4::timestamptz
		       OR (ss.updated_at = $4::timestamptz AND ss.session_id::text COLLATE "C" < $5::text COLLATE "C"))
		ORDER BY ss.updated_at DESC, ss.session_id DESC
		LIMIT $6`,
		q.UserID, q.ProjectID, strings.TrimSpace(q.Harness), cursorAt, q.CursorID, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSnapshot{}
	for rows.Next() {
		var snap SessionSnapshot
		if err := rows.Scan(&snap.ID, &snap.SessionID, &snap.SnapshotVersion, &snap.ProjectID, &snap.Harness,
			&snap.ConversationID, &snap.TurnCount, &snap.SourceMachineID, &snap.OwnerUserID,
			&snap.CreatedAt, &snap.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// LatestSnapshotTranscripts: see LegacyTimelineStore. Only the transcript
// column is read; diffs and artifact bundles stay on disk.
func (s *PostgresStore) LatestSnapshotTranscripts(ctx context.Context, sessionIDs []string) (map[string][]byte, error) {
	ids := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		if looksLikeUUID(id) {
			ids = append(ids, id)
		}
	}
	out := make(map[string][]byte, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (session_id) session_id::text, transcript_payload
		FROM session_snapshots
		WHERE session_id = ANY($1::uuid[])
		ORDER BY session_id, snapshot_version DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		out[id] = payload
	}
	return out, rows.Err()
}

// GetLatestSnapshotMeta: see LegacyTimelineStore.
func (s *PostgresStore) GetLatestSnapshotMeta(ctx context.Context, sessionID string) (*SessionSnapshot, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	var snap SessionSnapshot
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, session_id::text, snapshot_version, project_id::text, harness, conversation_id, turn_count,
		       COALESCE(git_branch,''), COALESCE(git_commit,''), COALESCE(git_dirty,false),
		       COALESCE(diff_size_bytes,0), diff_truncated, COALESCE(source_machine_id,''),
		       COALESCE(owner_user_id::text,''), created_at, updated_at
		FROM session_snapshots
		WHERE session_id = $1::uuid
		ORDER BY snapshot_version DESC
		LIMIT 1`, sessionID).Scan(&snap.ID, &snap.SessionID, &snap.SnapshotVersion, &snap.ProjectID, &snap.Harness,
		&snap.ConversationID, &snap.TurnCount, &snap.GitBranch, &snap.GitCommit, &snap.GitDirty,
		&snap.DiffSizeBytes, &snap.DiffTruncated, &snap.SourceMachineID, &snap.OwnerUserID,
		&snap.CreatedAt, &snap.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// pageTurns applies the ListSessionTurnsPage contract to idx-sorted turns.
func pageTurns(sorted []SessionTurn, after *int, limit int) ([]SessionTurn, bool) {
	page := make([]SessionTurn, 0, limit)
	for i, turn := range sorted {
		if after != nil && turn.Idx <= *after {
			continue
		}
		if len(page) == limit {
			return page, true
		}
		page = append(page, sorted[i])
	}
	return page, false
}

func (m *MemStore) sortedTurns(sessionID string) ([]SessionTurn, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.agentSessions[sessionID] == nil {
		return nil, ErrNotFound
	}
	out := append([]SessionTurn(nil), m.sessionTurns[sessionID]...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Idx < out[j].Idx })
	return out, nil
}

// ListSessionTurnsPage: see SessionTurnReader.
func (m *MemStore) ListSessionTurnsPage(ctx context.Context, sessionID string, after *int, limit int) ([]SessionTurn, bool, error) {
	_ = ctx
	turns, err := m.sortedTurns(sessionID)
	if err != nil {
		return nil, false, err
	}
	page, more := pageTurns(turns, after, limit)
	return page, more, nil
}

func blankTurnText(text string) bool { return strings.TrimSpace(text) == "" }

// SessionTurnOverview: see SessionTurnReader.
func (m *MemStore) SessionTurnOverview(ctx context.Context, sessionID string) (SessionTurn, SessionTurn, error) {
	// Same order as ListSessionTurns (idx order in Postgres).
	turns, err := m.ListSessionTurns(ctx, sessionID)
	if err != nil {
		return SessionTurn{}, SessionTurn{}, err
	}
	var first, last SessionTurn
	for _, turn := range turns {
		if blankTurnText(turn.TextPreview) {
			continue
		}
		switch strings.ToLower(turn.Role) {
		case "user":
			if first.Role == "" {
				first = turn
			}
		case "assistant":
			last = turn
		}
	}
	return first, last, nil
}

// mayCarryToolCalls is the cheap superset filter ListSessionToolCallTurns
// applies; the caller decides from the parsed values.
func mayCarryToolCalls(turn SessionTurn) bool {
	var calls []json.RawMessage
	if json.Unmarshal(turn.ToolCalls, &calls) == nil && len(calls) > 0 {
		return true
	}
	return strings.Contains(turn.TextPreview, "{")
}

// ListSessionToolCallTurns: see SessionTurnReader.
func (m *MemStore) ListSessionToolCallTurns(ctx context.Context, sessionID string) ([]SessionTurn, error) {
	// Same order as ListSessionTurns.
	turns, err := m.ListSessionTurns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := []SessionTurn{}
	for _, turn := range turns {
		if mayCarryToolCalls(turn) {
			out = append(out, turn)
		}
	}
	return out, nil
}

func scanTurnRows(rows pgx.Rows) ([]SessionTurn, error) {
	defer rows.Close()
	out := []SessionTurn{}
	for rows.Next() {
		var t SessionTurn
		var calls []byte
		if err := rows.Scan(&t.SessionID, &t.Idx, &t.Role, &t.At, &t.TextPreview, &calls, &t.BlobRef, &t.SourceAt); err != nil {
			return nil, err
		}
		t.ToolCalls = calls
		out = append(out, t)
	}
	return out, rows.Err()
}

const turnColumns = `session_id::text, idx, role, ts, COALESCE(text_preview,''), tool_calls, COALESCE(blob_ref,''), source_ts`

// ListSessionTurnsPage: see SessionTurnReader. Keyset on the (session_id,
// idx) primary key: the cost is the page, not the session.
func (s *PostgresStore) ListSessionTurnsPage(ctx context.Context, sessionID string, after *int, limit int) ([]SessionTurn, bool, error) {
	if !looksLikeUUID(sessionID) {
		return nil, false, ErrNotFound
	}
	if limit <= 0 {
		return []SessionTurn{}, false, nil
	}
	var afterIdx any
	if after != nil {
		afterIdx = *after
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+turnColumns+`
		FROM session_turns
		WHERE session_id = $1::uuid AND ($2::int IS NULL OR idx > $2::int)
		ORDER BY idx
		LIMIT $3`, sessionID, afterIdx, limit+1)
	if err != nil {
		return nil, false, err
	}
	turns, err := scanTurnRows(rows)
	if err != nil {
		return nil, false, err
	}
	if len(turns) > limit {
		return turns[:limit], true, nil
	}
	return turns, false, nil
}

// SessionTurnOverview: see SessionTurnReader. Two index walks from either
// end of the session.
func (s *PostgresStore) SessionTurnOverview(ctx context.Context, sessionID string) (SessionTurn, SessionTurn, error) {
	var first, last SessionTurn
	if !looksLikeUUID(sessionID) {
		return first, last, ErrNotFound
	}
	for _, q := range []struct {
		dst   *SessionTurn
		role  string
		order string
	}{{&first, "user", "ASC"}, {&last, "assistant", "DESC"}} {
		rows, err := s.pool.Query(ctx, `
			SELECT `+turnColumns+`
			FROM session_turns
			WHERE session_id = $1::uuid AND LOWER(role) = $2 AND BTRIM(COALESCE(text_preview,''), E' \t\n\r\f\v') <> ''
			ORDER BY idx `+q.order+`
			LIMIT 1`, sessionID, q.role)
		if err != nil {
			return first, last, err
		}
		turns, err := scanTurnRows(rows)
		if err != nil {
			return first, last, err
		}
		if len(turns) == 1 {
			*q.dst = turns[0]
		}
	}
	return first, last, nil
}

// ListSessionToolCallTurns: see SessionTurnReader.
func (s *PostgresStore) ListSessionToolCallTurns(ctx context.Context, sessionID string) ([]SessionTurn, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+turnColumns+`
		FROM session_turns
		WHERE session_id = $1::uuid
		  AND ((jsonb_typeof(tool_calls) = 'array' AND jsonb_array_length(tool_calls) > 0)
		       OR STRPOS(COALESCE(text_preview,''), '{') > 0)
		ORDER BY idx`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanTurnRows(rows)
}
