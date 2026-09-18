package store

// sessions.go — Session layer + memory scoping (Phase 3, nexus issue #12).
//
// Schema comes from migrations/003_sessions.up.sql; this file is behavior
// only (no DDL strings). Methods are defined on MemStore and PostgresStore
// here — same package, so no edits to store.go/db.go were needed.
//
// Scoping contract (plan §3.3):
//   - Memories created in a session start session-scoped: level='session'
//     with session_id set (CreateSessionMemory enforces this).
//   - A new session inherits project + org CONFIRMED memories, never sibling
//     session memories (NewSessionInherits / ListSessionVisibleMemories).
//   - Within a session, a session memory with the same key as a project
//     memory takes precedence (override applied in ListSessionVisibleMemories).
//   - A session-level key seen in >=3 distinct sessions auto-proposes
//     promotion (PromotionCandidates); PromoteSessionMemory flips one item
//     to level='project' with session_id cleared.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session is one multiplayer collaboration session within a project.
type Session struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title,omitempty"`
	CreatedBy string    `json:"created_by"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	// ExpiresAt optionally overrides DefaultSessionTTL for the expiry
	// sweeper (migration 010, issue #119). Zero means "created_at + TTL".
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// SessionParticipant is one user-or-agent membership in a session.
// Either UserID or AgentID is set (agents table lands in migration 004,
// so AgentID is a bare UUID string for now).
type SessionParticipant struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	UserID    string    `json:"user_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	Role      string    `json:"role"` // OWNER | MEMBER | OBSERVER
	JoinedAt  time.Time `json:"joined_at"`
	LeftAt    time.Time `json:"left_at,omitempty"`
}

// PromotionCandidate is a session-level key observed in enough distinct
// sessions to propose promotion to project scope.
type PromotionCandidate struct {
	Key          string   `json:"key"`
	SessionCount int      `json:"session_count"`
	ItemIDs      []string `json:"item_ids"`
}

// Session roles (uppercase, matching the CHECK constraint in 003).
const (
	SessionRoleOwner    = "OWNER"
	SessionRoleMember   = "MEMBER"
	SessionRoleObserver = "OBSERVER"
)

// DefaultPromotionThreshold is the number of distinct sessions after which a
// repeated session-level key is proposed for project scope (plan §2.7).
const DefaultPromotionThreshold = 3

// SessionStore is the session + scoping surface. Both MemStore (tests/local
// dev) and PostgresStore (production) implement it.
type SessionStore interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	ListProjectSessions(ctx context.Context, projectID string, activeOnly bool) ([]*Session, error)
	EndSession(ctx context.Context, id string) error

	JoinSession(ctx context.Context, sessionID, userID, agentID, role string) (*SessionParticipant, error)
	LeaveSession(ctx context.Context, sessionID, userID, agentID string) error
	ListSessionParticipants(ctx context.Context, sessionID string, activeOnly bool) ([]*SessionParticipant, error)

	CreateSessionMemory(ctx context.Context, item *MemoryItem) error
	ListSessionVisibleMemories(ctx context.Context, sessionID string) ([]*MemoryItem, error)
	PromotionCandidates(ctx context.Context, projectID string, minSessions int) ([]PromotionCandidate, error)
	PromoteSessionMemory(ctx context.Context, id, confirmedBy string) error
}

// Compile-time guarantees.
var _ SessionStore = (*MemStore)(nil)
var _ SessionStore = (*PostgresStore)(nil)

// normalizeRole uppercases and validates a participant role,
// defaulting blank to MEMBER.
func normalizeRole(role string) (string, error) {
	r := strings.ToUpper(strings.TrimSpace(role))
	if r == "" {
		return SessionRoleMember, nil
	}
	switch r {
	case SessionRoleOwner, SessionRoleMember, SessionRoleObserver:
		return r, nil
	default:
		return "", fmt.Errorf("invalid session role %q: must be OWNER, MEMBER, or OBSERVER", role)
	}
}

// NewSessionInherits reports whether m is inherited by a brand-new session:
// project- or organization-level, CONFIRMED, and not tied to any session.
// Sibling session memories (session_id set, or level='session') are never
// inherited — that is the isolation rule from issue #12.
func NewSessionInherits(m *MemoryItem) bool {
	if m == nil {
		return false
	}
	if m.SessionID != "" {
		return false
	}
	if m.Status != "CONFIRMED" {
		return false
	}
	return m.Level == "project" || m.Level == "organization"
}

// applySessionOverride drops project/org memories whose key is shadowed by a
// session memory with the same key (session wins within its session).
func applySessionOverride(sessionMems, inherited []*MemoryItem) []*MemoryItem {
	shadowed := make(map[string]struct{}, len(sessionMems))
	for _, m := range sessionMems {
		shadowed[m.Key] = struct{}{}
	}
	out := make([]*MemoryItem, 0, len(sessionMems)+len(inherited))
	out = append(out, sessionMems...)
	for _, m := range inherited {
		if _, ok := shadowed[m.Key]; !ok {
			out = append(out, m)
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// MemStore implementation (in-memory session state lives in a side bucket so
// store.go stays untouched; the bucket is keyed by store pointer).
// ----------------------------------------------------------------------------

type memSessionBucket struct {
	mu           sync.RWMutex
	sessions     map[string]*Session
	participants map[string][]*SessionParticipant // by session ID
}

var memSessionBuckets sync.Map // map[*MemStore]*memSessionBucket

func memSessionsOf(s *MemStore) *memSessionBucket {
	b, _ := memSessionBuckets.LoadOrStore(s, &memSessionBucket{
		sessions:     make(map[string]*Session),
		participants: make(map[string][]*SessionParticipant),
	})
	return b.(*memSessionBucket)
}

// CreateSession opens a new active session for an existing project.
func (s *MemStore) CreateSession(ctx context.Context, sess *Session) error {
	if strings.TrimSpace(sess.ProjectID) == "" {
		return fmt.Errorf("project_id is required")
	}
	if strings.TrimSpace(sess.CreatedBy) == "" {
		return fmt.Errorf("created_by is required")
	}
	s.mu.RLock()
	_, ok := s.projects[sess.ProjectID]
	s.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	b := memSessionsOf(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	if sess.ID == "" {
		sess.ID = newID("sess")
	}
	if _, exists := b.sessions[sess.ID]; exists {
		return ErrConflict
	}
	sess.IsActive = true
	sess.CreatedAt = time.Now().UTC()
	cp := *sess
	b.sessions[sess.ID] = &cp
	return nil
}

// GetSession fetches one session by ID.
func (s *MemStore) GetSession(ctx context.Context, id string) (*Session, error) {
	b := memSessionsOf(s)
	b.mu.RLock()
	defer b.mu.RUnlock()
	sess, ok := b.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *sess
	return &cp, nil
}

// ListProjectSessions lists sessions of a project, newest first.
func (s *MemStore) ListProjectSessions(ctx context.Context, projectID string, activeOnly bool) ([]*Session, error) {
	b := memSessionsOf(s)
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []*Session
	for _, sess := range b.sessions {
		if sess.ProjectID != projectID {
			continue
		}
		if activeOnly && !sess.IsActive {
			continue
		}
		cp := *sess
		out = append(out, &cp)
	}
	return out, nil
}

// EndSession marks a session inactive (idempotent) and stamps ended_at.
func (s *MemStore) EndSession(ctx context.Context, id string) error {
	b := memSessionsOf(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, ok := b.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if !sess.IsActive {
		return nil
	}
	sess.IsActive = false
	sess.EndedAt = time.Now().UTC()
	// Close out active participations so presence stops counting them.
	now := sess.EndedAt
	for _, p := range b.participants[id] {
		if p.LeftAt.IsZero() {
			p.LeftAt = now
		}
	}
	return nil
}

// JoinSession adds a user/agent to a session, or re-activates their row on
// re-join (clears left_at). At least one of userID/agentID is required.
func (s *MemStore) JoinSession(ctx context.Context, sessionID, userID, agentID, role string) (*SessionParticipant, error) {
	if userID == "" && agentID == "" {
		return nil, fmt.Errorf("user_id or agent_id is required")
	}
	r, err := normalizeRole(role)
	if err != nil {
		return nil, err
	}
	b := memSessionsOf(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, ok := b.sessions[sessionID]
	if !ok {
		return nil, ErrNotFound
	}
	if !sess.IsActive {
		return nil, fmt.Errorf("session %s is not active", sessionID)
	}
	for _, p := range b.participants[sessionID] {
		if p.UserID == userID && p.AgentID == agentID {
			p.LeftAt = time.Time{}
			p.Role = r
			cp := *p
			return &cp, nil
		}
	}
	p := &SessionParticipant{
		ID:        newID("spart"),
		SessionID: sessionID,
		UserID:    userID,
		AgentID:   agentID,
		Role:      r,
		JoinedAt:  time.Now().UTC(),
	}
	b.participants[sessionID] = append(b.participants[sessionID], p)
	cp := *p
	return &cp, nil
}

// LeaveSession stamps left_at on the active membership row.
func (s *MemStore) LeaveSession(ctx context.Context, sessionID, userID, agentID string) error {
	b := memSessionsOf(s)
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.sessions[sessionID]; !ok {
		return ErrNotFound
	}
	for _, p := range b.participants[sessionID] {
		if p.UserID == userID && p.AgentID == agentID && p.LeftAt.IsZero() {
			p.LeftAt = time.Now().UTC()
			return nil
		}
	}
	return ErrNotFound
}

// ListSessionParticipants lists members; activeOnly hides rows with left_at set.
func (s *MemStore) ListSessionParticipants(ctx context.Context, sessionID string, activeOnly bool) ([]*SessionParticipant, error) {
	b := memSessionsOf(s)
	b.mu.RLock()
	defer b.mu.RUnlock()
	if _, ok := b.sessions[sessionID]; !ok {
		return nil, ErrNotFound
	}
	var out []*SessionParticipant
	for _, p := range b.participants[sessionID] {
		if activeOnly && !p.LeftAt.IsZero() {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
}

// CreateSessionMemory stores a memory as session-scoped: session_id is
// required, level is forced to 'session' regardless of caller input. The
// session must exist and belong to the memory's project (issue #102
// cross-project invariant): an empty memory project is filled from the
// session, a mismatched one fails instead of linking Project B's memory to
// Project A's session.
func (s *MemStore) CreateSessionMemory(ctx context.Context, item *MemoryItem) error {
	if strings.TrimSpace(item.SessionID) == "" {
		return fmt.Errorf("session_id is required for session memory")
	}
	b := memSessionsOf(s)
	b.mu.RLock()
	sess, ok := b.sessions[item.SessionID]
	b.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	if strings.TrimSpace(item.ProjectID) == "" {
		item.ProjectID = sess.ProjectID
	} else if item.ProjectID != sess.ProjectID {
		return fmt.Errorf("store: session %s belongs to project %s, not %s: %w",
			item.SessionID, sess.ProjectID, item.ProjectID, ErrConflict)
	}
	item.Level = "session"
	return s.CreateMemoryItem(ctx, item)
}

// ListSessionVisibleMemories returns the session's own PROPOSED/CONFIRMED
// memories plus inherited project/org CONFIRMED memories, with session keys
// overriding project keys. Sibling session memories are excluded.
func (s *MemStore) ListSessionVisibleMemories(ctx context.Context, sessionID string) ([]*MemoryItem, error) {
	b := memSessionsOf(s)
	b.mu.RLock()
	sess, ok := b.sessions[sessionID]
	b.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var own, inherited []*MemoryItem
	for _, m := range s.memories {
		if m.SessionID == sessionID {
			if m.Status == "CONFIRMED" || m.Status == "PROPOSED" {
				own = append(own, cloneMemoryItem(m))
			}
			continue
		}
		if m.SessionID != "" {
			continue // sibling session: isolated
		}
		if m.ProjectID != "" && m.ProjectID != sess.ProjectID {
			continue
		}
		if NewSessionInherits(m) {
			inherited = append(inherited, cloneMemoryItem(m))
		}
	}
	return applySessionOverride(own, inherited), nil
}

// PromotionCandidates groups session-level memories of a project by key and
// returns keys seen in >= minSessions distinct sessions (default 3).
func (s *MemStore) PromotionCandidates(ctx context.Context, projectID string, minSessions int) ([]PromotionCandidate, error) {
	if minSessions <= 0 {
		minSessions = DefaultPromotionThreshold
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byKey := make(map[string]map[string][]string) // key -> sessionID -> itemIDs
	for _, m := range s.memories {
		if m.Level != "session" || m.SessionID == "" {
			continue
		}
		// Strict project scoping like Postgres (WHERE project_id = $1):
		// org-level (NULL-project) session rows are NOT candidates of any
		// project (issue #110).
		if m.ProjectID != projectID {
			continue
		}
		if m.Status == "REJECTED" {
			continue
		}
		if byKey[m.Key] == nil {
			byKey[m.Key] = make(map[string][]string)
		}
		byKey[m.Key][m.SessionID] = append(byKey[m.Key][m.SessionID], m.ID)
	}
	var out []PromotionCandidate
	for key, perSession := range byKey {
		if len(perSession) < minSessions {
			continue
		}
		c := PromotionCandidate{Key: key, SessionCount: len(perSession)}
		for _, ids := range perSession {
			c.ItemIDs = append(c.ItemIDs, ids...)
		}
		out = append(out, c)
	}
	return out, nil
}

// PromoteSessionMemory flips one session memory to project scope:
// level='project', session_id cleared. The promoted item keeps its key so
// future sessions inherit it via NewSessionInherits.
func (s *MemStore) PromoteSessionMemory(ctx context.Context, id, confirmedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return ErrNotFound
	}
	if m.Level != "session" {
		return fmt.Errorf("memory %s is not session-scoped (level=%s)", id, m.Level)
	}
	if m.Status != StatusProposed && m.Status != StatusConfirmed {
		return fmt.Errorf("memory %s has terminal status %s: %w", id, m.Status, ErrConflict)
	}
	m.Level = "project"
	m.SessionID = ""
	m.Status = "CONFIRMED"
	m.ConfirmedBy = confirmedBy
	m.UpdatedAt = time.Now().UTC()
	return nil
}

// ----------------------------------------------------------------------------
// PostgresStore implementation
// ----------------------------------------------------------------------------

const sessionColumns = `id, project_id, title, created_by, is_active, created_at, ended_at, expires_at`

func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	var title *string
	var endedAt, expiresAt *time.Time
	if err := row.Scan(&s.ID, &s.ProjectID, &title, &s.CreatedBy,
		&s.IsActive, &s.CreatedAt, &endedAt, &expiresAt); err != nil {
		return nil, err
	}
	if title != nil {
		s.Title = *title
	}
	if endedAt != nil {
		s.EndedAt = *endedAt
	}
	if expiresAt != nil {
		s.ExpiresAt = *expiresAt
	}
	return &s, nil
}

const participantColumns = `id, session_id, user_id, agent_id, role, joined_at, left_at`

func scanParticipant(row pgx.Row) (*SessionParticipant, error) {
	var p SessionParticipant
	var userID, agentID *string
	var leftAt *time.Time
	if err := row.Scan(&p.ID, &p.SessionID, &userID, &agentID,
		&p.Role, &p.JoinedAt, &leftAt); err != nil {
		return nil, err
	}
	if userID != nil {
		p.UserID = *userID
	}
	if agentID != nil {
		p.AgentID = *agentID
	}
	if leftAt != nil {
		p.LeftAt = *leftAt
	}
	return &p, nil
}

// CreateSession opens a new active session for an existing project.
func (s *PostgresStore) CreateSession(ctx context.Context, sess *Session) error {
	if strings.TrimSpace(sess.ProjectID) == "" {
		return fmt.Errorf("project_id is required")
	}
	if strings.TrimSpace(sess.CreatedBy) == "" {
		return fmt.Errorf("created_by is required")
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO sessions (project_id, title, created_by, expires_at)
		 VALUES ($1::uuid, NULLIF($2,''), $3::uuid, $4)
		 RETURNING `+sessionColumns,
		sess.ProjectID, sess.Title, sess.CreatedBy, nullTime(sess.ExpiresAt))
	got, err := scanSession(row)
	if err != nil {
		return err
	}
	*sess = *got
	return nil
}

// GetSession fetches one session by ID.
func (s *PostgresStore) GetSession(ctx context.Context, id string) (*Session, error) {
	sess, err := scanSession(s.pool.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = $1::uuid`, id))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return sess, err
}

// ListProjectSessions lists sessions of a project, newest first.
func (s *PostgresStore) ListProjectSessions(ctx context.Context, projectID string, activeOnly bool) ([]*Session, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+sessionColumns+` FROM sessions
		  WHERE project_id = $1::uuid AND ($2 = false OR is_active)
		  ORDER BY created_at DESC`,
		projectID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// EndSession marks a session inactive and closes active participations.
func (s *PostgresStore) EndSession(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET is_active = false, ended_at = now()
		  WHERE id = $1::uuid AND is_active`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either missing or already ended; distinguish the two.
		if _, gerr := s.GetSession(ctx, id); gerr != nil {
			return gerr
		}
		return nil
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE session_participants SET left_at = now()
		  WHERE session_id = $1::uuid AND left_at IS NULL`, id)
	return err
}

// JoinSession adds a user/agent to an active session (re-join reuses the row).
func (s *PostgresStore) JoinSession(ctx context.Context, sessionID, userID, agentID, role string) (*SessionParticipant, error) {
	if userID == "" && agentID == "" {
		return nil, fmt.Errorf("user_id or agent_id is required")
	}
	r, err := normalizeRole(role)
	if err != nil {
		return nil, err
	}
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !sess.IsActive {
		return nil, fmt.Errorf("session %s is not active", sessionID)
	}
	if userID != "" {
		row := s.pool.QueryRow(ctx,
			`INSERT INTO session_participants (session_id, user_id, agent_id, role)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
			 ON CONFLICT (session_id, user_id) WHERE user_id IS NOT NULL
			 DO UPDATE SET left_at = NULL, role = EXCLUDED.role, joined_at = now()
			 RETURNING `+participantColumns,
			sessionID, nullText(userID), nullText(agentID), r)
		return scanParticipant(row)
	}
	// Agent-only rows have no unique key (NULLs are distinct), so look for an
	// active row first and reuse it before inserting.
	existing, err := s.pool.Query(ctx,
		`SELECT `+participantColumns+` FROM session_participants
		  WHERE session_id = $1::uuid AND user_id IS NULL AND agent_id = $2::uuid
		    AND left_at IS NULL LIMIT 1`,
		sessionID, agentID)
	if err != nil {
		return nil, err
	}
	if existing.Next() {
		p, serr := scanParticipant(existing)
		existing.Close()
		if serr != nil {
			return nil, serr
		}
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE session_participants SET role = $2 WHERE id = $1::uuid`, p.ID, r); uerr != nil {
			return nil, uerr
		}
		p.Role = r
		return p, nil
	}
	existing.Close()
	return scanParticipant(s.pool.QueryRow(ctx,
		`INSERT INTO session_participants (session_id, agent_id, role)
		 VALUES ($1::uuid, $2::uuid, $3)
		 RETURNING `+participantColumns,
		sessionID, agentID, r))
}

// LeaveSession stamps left_at on the active membership row.
func (s *PostgresStore) LeaveSession(ctx context.Context, sessionID, userID, agentID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE session_participants SET left_at = now()
		  WHERE session_id = $1::uuid
		    AND COALESCE(user_id::text,'') = COALESCE(NULLIF($2,''),'')
		    AND COALESCE(agent_id::text,'') = COALESCE(NULLIF($3,''),'')
		    AND left_at IS NULL`,
		sessionID, userID, agentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSessionParticipants lists members; activeOnly hides left rows.
func (s *PostgresStore) ListSessionParticipants(ctx context.Context, sessionID string, activeOnly bool) ([]*SessionParticipant, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+participantColumns+` FROM session_participants
		  WHERE session_id = $1::uuid AND ($2 = false OR left_at IS NULL)
		  ORDER BY joined_at ASC`,
		sessionID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SessionParticipant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateSessionMemory stores a memory as session-scoped (level forced).
// The session must exist and belong to the memory's project (issue #102):
// an empty memory project is filled from the session, a mismatched one
// fails instead of cross-linking projects.
func (s *PostgresStore) CreateSessionMemory(ctx context.Context, item *MemoryItem) error {
	if strings.TrimSpace(item.SessionID) == "" {
		return fmt.Errorf("session_id is required for session memory")
	}
	sess, err := s.GetSession(ctx, item.SessionID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(item.ProjectID) == "" {
		item.ProjectID = sess.ProjectID
	} else if item.ProjectID != sess.ProjectID {
		return fmt.Errorf("store: session %s belongs to project %s, not %s: %w",
			item.SessionID, sess.ProjectID, item.ProjectID, ErrConflict)
	}
	item.Level = "session"
	return s.CreateMemoryItem(ctx, item)
}

// ListSessionVisibleMemories returns own session memories + inherited
// project/org memories with session-key override.
func (s *PostgresStore) ListSessionVisibleMemories(ctx context.Context, sessionID string) ([]*MemoryItem, error) {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+memoryColumns+` FROM memory_items
		  WHERE ((session_id = $1::uuid AND status IN ('CONFIRMED','PROPOSED'))
		     OR (session_id IS NULL AND status = 'CONFIRMED'
		         AND (project_id = $2::uuid OR project_id IS NULL)
		         AND level IN ('project','organization')))`,
		sessionID, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var own, inherited []*MemoryItem
	for rows.Next() {
		m, err := scanMemoryItem(rows)
		if err != nil {
			return nil, err
		}
		if m.SessionID == sessionID {
			own = append(own, m)
		} else {
			inherited = append(inherited, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return applySessionOverride(own, inherited), nil
}

// PromotionCandidates returns session-level keys seen in >= minSessions
// distinct sessions of a project.
func (s *PostgresStore) PromotionCandidates(ctx context.Context, projectID string, minSessions int) ([]PromotionCandidate, error) {
	if minSessions <= 0 {
		minSessions = DefaultPromotionThreshold
	}
	rows, err := s.pool.Query(ctx,
		`SELECT "key", COUNT(DISTINCT session_id), array_agg(id::text)
		  FROM memory_items
		  WHERE project_id = $1::uuid AND level = 'session'
		    AND session_id IS NOT NULL AND status <> 'REJECTED'
		  GROUP BY "key"
		  HAVING COUNT(DISTINCT session_id) >= $2`,
		projectID, minSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PromotionCandidate
	for rows.Next() {
		var c PromotionCandidate
		if err := rows.Scan(&c.Key, &c.SessionCount, &c.ItemIDs); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PromoteSessionMemory flips one session memory to project scope.
// Terminal rows (REJECTED/SUPERSEDED) never promote (issue #131).
func (s *PostgresStore) PromoteSessionMemory(ctx context.Context, id, confirmedBy string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE memory_items SET level = 'project', session_id = NULL,
			status = 'CONFIRMED', confirmed_by = $2::uuid, updated_at = now()
		  WHERE id = $1::uuid AND level = 'session'
		    AND status IN ('PROPOSED','CONFIRMED')`,
		id, nullText(confirmedBy))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.GetMemoryItem(ctx, id); gerr != nil {
			return gerr
		}
		return fmt.Errorf("memory %s is not session-scoped", id)
	}
	return nil
}
