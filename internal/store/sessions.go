// Sessions and session-scoped memory (issue #12, plan §§3.1, 3.3, 2.7).
//
// Sessions are live multiplayer working sessions within a project.
// Participants are users and/or agents with an OWNER/MEMBER/OBSERVER role;
// join/leave history is preserved (Leave stamps left_at, re-join inserts a
// fresh row) while partial unique indexes in migrations/003 guarantee a
// single ACTIVE seat per user/agent per session.
//
// Memory scoping (plan §3.3): memories default to session-scoped
// (session_id set, level 'session'). Promotion to project scope clears
// session_id and flips level to 'project'; per plan §2.7 the Memory
// Processor (or a user) proposes promotion when the same key appears in 3+
// sessions. Isolation is encoded in the pure IsVisibleToSession predicate:
// a new session inherits project/org CONFIRMED memories, never the
// session-scoped rows of a sibling session, until they are promoted.
//
// Testability: SessionStore depends on the DBTX interface (db.go), so unit
// tests run without a live Postgres. The promotion counter and visibility
// predicates are pure and covered DB-free; live-DB behaviour is covered by
// TEST_POSTGRES_DSN-gated tests in sessions_integration_test.go.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Participant roles (CHECK constraint in migrations/003, plan §3.1).
const (
	RoleOwner    = "OWNER"
	RoleMember   = "MEMBER"
	RoleObserver = "OBSERVER"
)

// DefaultRole is assigned when JoinParams leaves Role empty.
const DefaultRole = RoleMember

// PromotionThreshold is the plan §2.7 trigger: the same memory key observed
// in this many distinct sessions proposes SESSION → PROJECT promotion.
const PromotionThreshold = 3

// IsValidRole reports whether role is a known participant role. Empty is
// invalid here — callers normalize with NormalizeRole first.
func IsValidRole(role string) bool {
	switch role {
	case RoleOwner, RoleMember, RoleObserver:
		return true
	default:
		return false
	}
}

// NormalizeRole upper-cases and trims role, defaulting "" to MEMBER. The
// second return is false when the role is unknown.
func NormalizeRole(role string) (string, bool) {
	r := strings.ToUpper(strings.TrimSpace(role))
	if r == "" {
		return DefaultRole, true
	}
	if !IsValidRole(r) {
		return r, false
	}
	return r, true
}

// Session mirrors a sessions row (plan §3.1). Empty Title means SQL NULL;
// EndedAt is nil while the session is active.
type Session struct {
	ID        string
	ProjectID string
	Title     string
	CreatedBy string
	IsActive  bool
	CreatedAt time.Time
	EndedAt   *time.Time
}

// Participant mirrors a session_participants row. Empty UserID/AgentID means
// SQL NULL; at least one of the two is always set (CHECK in 003). LeftAt is
// nil while the seat is active.
type Participant struct {
	ID        string
	SessionID string
	UserID    string
	AgentID   string
	Role      string
	JoinedAt  time.Time
	LeftAt    *time.Time
}

// SessionParams carries session identity for Create.
type SessionParams struct {
	ProjectID string
	Title     string // optional; "" = NULL
	CreatedBy string // user UUID of the creator; required (NOT NULL in 003)
}

// JoinParams carries one seat claim for Join. At least one of UserID/AgentID
// is required; empty Role defaults to MEMBER.
type JoinParams struct {
	UserID  string // user UUID; "" = NULL (agent-only seat)
	AgentID string // agent UUID; "" = NULL (deferred FK to 004 agents)
	Role    string // OWNER/MEMBER/OBSERVER; "" = MEMBER
}

// Validate rejects seat claims with neither user nor agent, or an unknown
// role. It does not touch the database.
func (p JoinParams) Validate() error {
	if strings.TrimSpace(p.UserID) == "" && strings.TrimSpace(p.AgentID) == "" {
		return errors.New("store: join requires a user id or an agent id")
	}
	if _, ok := NormalizeRole(p.Role); !ok {
		return fmt.Errorf("store: unknown participant role %q", p.Role)
	}
	return nil
}

// sessionColumns selects sessions with NULLs coalesced (except ended_at,
// which stays nullable so "still active" is representable).
const sessionColumns = `id::TEXT AS id, ` +
	`project_id::TEXT AS project_id, ` +
	`COALESCE(title, '') AS title, ` +
	`created_by::TEXT AS created_by, ` +
	`COALESCE(is_active, true) AS is_active, ` +
	`created_at, ` +
	`ended_at`

// participantColumns selects participants with NULLs coalesced (except
// left_at, which stays nullable so "active seat" is representable).
const participantColumns = `id::TEXT AS id, ` +
	`session_id::TEXT AS session_id, ` +
	`COALESCE(user_id::TEXT, '') AS user_id, ` +
	`COALESCE(agent_id::TEXT, '') AS agent_id, ` +
	`COALESCE(role, 'MEMBER') AS role, ` +
	`joined_at, ` +
	`left_at`

// scanSession scans a full sessionColumns row.
func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	if err := row.Scan(
		&s.ID, &s.ProjectID, &s.Title, &s.CreatedBy,
		&s.IsActive, &s.CreatedAt, &s.EndedAt,
	); err != nil {
		return nil, err
	}
	return &s, nil
}

// scanParticipant scans a full participantColumns row.
func scanParticipant(row pgx.Row) (*Participant, error) {
	var p Participant
	if err := row.Scan(
		&p.ID, &p.SessionID, &p.UserID, &p.AgentID,
		&p.Role, &p.JoinedAt, &p.LeftAt,
	); err != nil {
		return nil, err
	}
	return &p, nil
}

// SessionStore is session lifecycle plus participant seats.
type SessionStore struct {
	db DBTX
}

// NewSessionStore wires a SessionStore to any DBTX (pool, transaction, fake).
func NewSessionStore(db DBTX) *SessionStore {
	return &SessionStore{db: db}
}

// Create opens a session and seats the creator as OWNER in one call, so a
// freshly created session always has an owner for the multi-user join flow.
// Title "" stores NULL. Ended sessions cannot be created (is_active starts
// true, ended_at NULL).
func (s *SessionStore) Create(ctx context.Context, params SessionParams) (*Session, error) {
	if strings.TrimSpace(params.ProjectID) == "" {
		return nil, errors.New("store: session project id is required")
	}
	if strings.TrimSpace(params.CreatedBy) == "" {
		return nil, errors.New("store: session creator is required")
	}
	sess, err := scanSession(s.db.QueryRow(ctx,
		`INSERT INTO sessions (project_id, title, created_by)
		 VALUES ($1, $2, $3)
		 RETURNING `+sessionColumns,
		params.ProjectID,
		nullText(strings.TrimSpace(params.Title)),
		params.CreatedBy))
	if err != nil {
		return nil, fmt.Errorf("store: create session: %w", err)
	}
	if _, err := scanParticipant(s.db.QueryRow(ctx,
		`INSERT INTO session_participants (session_id, user_id, role)
		 VALUES ($1, $2, 'OWNER')
		 RETURNING `+participantColumns,
		sess.ID, params.CreatedBy)); err != nil {
		return nil, fmt.Errorf("store: seat session owner: %w", err)
	}
	return sess, nil
}

// GetByID fetches one session or a wrapped ErrNotFound.
func (s *SessionStore) GetByID(ctx context.Context, id string) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: session %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get session: %w", err)
	}
	return sess, nil
}

// End closes a session (is_active = false, ended_at = now()) and releases
// all active seats, preserving them as history. Ending an already-ended
// session is a no-op success returning the current row.
func (s *SessionStore) End(ctx context.Context, id string) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(ctx,
		`UPDATE sessions
		 SET is_active = false, ended_at = COALESCE(ended_at, now())
		 WHERE id = $1
		 RETURNING `+sessionColumns, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: session %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: end session: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE session_participants
		 SET left_at = now()
		 WHERE session_id = $1 AND left_at IS NULL`, id); err != nil {
		return nil, fmt.Errorf("store: release session seats: %w", err)
	}
	return sess, nil
}

// ListActive returns the open sessions of a project, oldest first (stable
// join order for a lobby view).
func (s *SessionStore) ListActive(ctx context.Context, projectID string) ([]Session, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+sessionColumns+` FROM sessions
		 WHERE project_id = $1 AND is_active
		 ORDER BY created_at ASC, id ASC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list active sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(
			&sess.ID, &sess.ProjectID, &sess.Title, &sess.CreatedBy,
			&sess.IsActive, &sess.CreatedAt, &sess.EndedAt,
		); err != nil {
			return nil, fmt.Errorf("store: list active sessions scan: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list active sessions rows: %w", err)
	}
	return out, nil
}

// activeSeatQuery builds the single-active-seat lookup: exactly one of
// user/agent is set per call, and the other side must be NULL so a
// user-seat never collides with an agent-only seat holding the same id
// text. NULL is expressed with IS NULL (never = NULL).
func activeSeatQuery(sessionID, userID, agentID string) (string, []any) {
	base := `SELECT ` + participantColumns + ` FROM session_participants ` +
		`WHERE session_id = $1 AND left_at IS NULL AND `
	switch {
	case userID != "" && agentID != "":
		return base + `user_id = $2 AND agent_id = $3`, []any{sessionID, userID, agentID}
	case userID != "":
		return base + `user_id = $2 AND agent_id IS NULL`, []any{sessionID, userID}
	default:
		return base + `agent_id = $2 AND user_id IS NULL`, []any{sessionID, agentID}
	}
}

// Join claims a seat in a session. Re-joining while the seat is still active
// is idempotent: the existing row is returned and no duplicate is inserted
// (the 003 partial unique indexes backstop concurrent double-joins with a
// 23505, surfaced as-is for the caller to retry). Joining after Leave
// inserts a fresh row, preserving history.
func (s *SessionStore) Join(ctx context.Context, sessionID string, params JoinParams) (*Participant, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("store: session id is required")
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(params.UserID)
	agentID := strings.TrimSpace(params.AgentID)
	role, _ := NormalizeRole(params.Role)

	query, args := activeSeatQuery(sessionID, userID, agentID)
	if existing, err := scanParticipant(s.db.QueryRow(ctx, query, args...)); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: lookup session seat: %w", err)
	}
	p, err := scanParticipant(s.db.QueryRow(ctx,
		`INSERT INTO session_participants (session_id, user_id, agent_id, role)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+participantColumns,
		sessionID, nullUUID(userID), nullUUID(agentID), role))
	if err != nil {
		return nil, fmt.Errorf("store: join session: %w", err)
	}
	return p, nil
}

// Leave releases one active seat (left_at = now()), preserving history.
// Leaving with no active seat is a wrapped ErrNotFound.
func (s *SessionStore) Leave(ctx context.Context, sessionID, userID, agentID string) (*Participant, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("store: session id is required")
	}
	userID = strings.TrimSpace(userID)
	agentID = strings.TrimSpace(agentID)
	if userID == "" && agentID == "" {
		return nil, errors.New("store: leave requires a user id or an agent id")
	}
	base := `UPDATE session_participants SET left_at = now() ` +
		`WHERE session_id = $1 AND left_at IS NULL AND `
	var query string
	var args []any
	switch {
	case userID != "" && agentID != "":
		query, args = base+`user_id = $2 AND agent_id = $3 RETURNING `+participantColumns, []any{sessionID, userID, agentID}
	case userID != "":
		query, args = base+`user_id = $2 AND agent_id IS NULL RETURNING `+participantColumns, []any{sessionID, userID}
	default:
		query, args = base+`agent_id = $2 AND user_id IS NULL RETURNING `+participantColumns, []any{sessionID, agentID}
	}
	p, err := scanParticipant(s.db.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: no active seat in session %s: %w", sessionID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: leave session: %w", err)
	}
	return p, nil
}

// ListParticipants returns the ACTIVE seats of a session in join order —
// the multi-user join acceptance check (Alice and Bob both listed).
func (s *SessionStore) ListParticipants(ctx context.Context, sessionID string) ([]Participant, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+participantColumns+` FROM session_participants
		 WHERE session_id = $1 AND left_at IS NULL
		 ORDER BY joined_at ASC, id ASC`,
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list session participants: %w", err)
	}
	defer rows.Close()
	var out []Participant
	for rows.Next() {
		var p Participant
		if err := rows.Scan(
			&p.ID, &p.SessionID, &p.UserID, &p.AgentID,
			&p.Role, &p.JoinedAt, &p.LeftAt,
		); err != nil {
			return nil, fmt.Errorf("store: list session participants scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list session participants rows: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Session-scoped memory: isolation + promotion (plan §§3.3, 2.7)
// ---------------------------------------------------------------------------

// IsSessionScoped reports whether a memory item is session-scoped: it carries
// a session_id and therefore lives in exactly one session until promoted.
// Promotion clears SessionID (and flips Level to 'project'); callers must
// treat "" as shared/project-visible.
func IsSessionScoped(item MemoryItem) bool {
	return strings.TrimSpace(item.SessionID) != ""
}

// IsVisibleToSession encodes the plan §3.3 isolation rule: a session sees
// its own session-scoped items plus every non-session-scoped item
// (project/org/personal CONFIRMED rows — personal user-filtering is the
// caller's concern, this predicate only gates the session boundary).
// Sibling-session rows stay invisible until promoted (session_id → NULL).
func IsVisibleToSession(item MemoryItem, sessionID string) bool {
	if !IsSessionScoped(item) {
		return true
	}
	return strings.TrimSpace(item.SessionID) == strings.TrimSpace(sessionID)
}

// FilterVisibleToSession keeps the items visible inside sessionID,
// preserving caller order. Pure counterpart of the SQL scoping a session
// read path applies (`WHERE session_id IS NULL OR session_id = $1` plus
// project/org inheritance).
func FilterVisibleToSession(items []MemoryItem, sessionID string) []MemoryItem {
	out := items[:0:0]
	for _, it := range items {
		if IsVisibleToSession(it, sessionID) {
			out = append(out, it)
		}
	}
	return out
}

// CountKeySessions counts the DISTINCT sessions holding a session-scoped
// item under key — the plan §2.7 promotion signal. Project-level copies
// (SessionID "") never count: only unpromoted session rows vote.
func CountKeySessions(items []MemoryItem, key string) int {
	seen := make(map[string]struct{})
	for _, it := range items {
		if it.Key != key || !IsSessionScoped(it) {
			continue
		}
		seen[strings.TrimSpace(it.SessionID)] = struct{}{}
	}
	return len(seen)
}

// ShouldProposePromotion reports whether a key observed in distinctSessions
// distinct sessions meets the SESSION → PROJECT promotion bar (plan §2.7:
// same fact in 3+ sessions).
func ShouldProposePromotion(distinctSessions int) bool {
	return distinctSessions >= PromotionThreshold
}

// PromotionProposal is one key's promotion case: the key, how many distinct
// sessions hold it, and which (sorted for determinism).
type PromotionProposal struct {
	Key          string
	SessionCount int
	SessionIDs   []string
}

// PromotionCandidates groups session-scoped items by key and proposes every
// key present in >= threshold distinct sessions (threshold <= 0 selects
// PromotionThreshold). Proposals sort by Key so output is deterministic.
func PromotionCandidates(items []MemoryItem, threshold int) []PromotionProposal {
	if threshold <= 0 {
		threshold = PromotionThreshold
	}
	byKey := make(map[string]map[string]struct{})
	for _, it := range items {
		if !IsSessionScoped(it) {
			continue
		}
		set, ok := byKey[it.Key]
		if !ok {
			set = make(map[string]struct{})
			byKey[it.Key] = set
		}
		set[strings.TrimSpace(it.SessionID)] = struct{}{}
	}
	var out []PromotionProposal
	for key, set := range byKey {
		if len(set) < threshold {
			continue
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out = append(out, PromotionProposal{Key: key, SessionCount: len(set), SessionIDs: ids})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

// CountKeySessionsDB is the DB-backed promotion signal: distinct sessions
// holding key in a project (session-scoped rows only). The Memory Processor
// calls this per candidate key and proposes promotion at >=
// PromotionThreshold.
func (s *SessionStore) CountKeySessionsDB(ctx context.Context, projectID, key string) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(DISTINCT session_id) FROM memory_items
		 WHERE project_id = $1 AND key = $2 AND session_id IS NOT NULL`,
		projectID, key).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count key sessions: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Issue #35 append-only: processor promotion seam (plan §2.7)
// ---------------------------------------------------------------------------
//
// These methods complete the daemon.ProcessorStore surface on SessionStore
// with stdlib-only signatures identical to the daemon side, so the processor
// can count and promote without importing this package. No existing code
// above is modified.

// CountKeySessions is the issue #35 seam alias of CountKeySessionsDB: the
// name the daemon ProcessorStore declares. Behavior is identical.
func (s *SessionStore) CountKeySessions(ctx context.Context, projectID, key string) (int, error) {
	return s.CountKeySessionsDB(ctx, projectID, key)
}

// PromoteKey flips every session-scoped row under key to project scope
// (session_id → NULL, level → 'project'; SQL shared with MemoryStore via
// BuildPromoteKeySQL) and returns the touched row count.
func (s *SessionStore) PromoteKey(ctx context.Context, projectID, key string) (int64, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, errors.New("store: promote requires a project id")
	}
	if strings.TrimSpace(key) == "" {
		return 0, errors.New("store: promote requires a key")
	}
	tag, err := s.db.Exec(ctx, BuildPromoteKeySQL(), projectID, key)
	if err != nil {
		return 0, fmt.Errorf("store: promote key: %w", err)
	}
	return tag.RowsAffected(), nil
}
