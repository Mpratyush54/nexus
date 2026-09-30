package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const guestLinkDefaultTTL = 7 * 24 * time.Hour

// ErrGuestLinkExpired is returned when a guest link is past expires_at.
var ErrGuestLinkExpired = errors.New("guest link expired")

// GuestLink is a session-scoped invite (spec 9.2 / D12). It carries no
// session title, summary, or file name.
type GuestLink struct {
	ID            string     `json:"id"`
	SessionID     string     `json:"session_id"`
	Token         string     `json:"token,omitempty"`
	CreatedBy     string     `json:"created_by,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	SingleUse     bool       `json:"single_use"`
	UsedAt        *time.Time `json:"used_at,omitempty"`
	GranteeUserID string     `json:"grantee_user_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at,omitempty"`
}

// OffboardResult is the D21 offboarding preview and result: counts only.
type OffboardResult struct {
	SessionsTransferred     int `json:"sessions_transferred"`
	GrantsKept              int `json:"grants_kept"`
	SecretGrantsTransferred int `json:"secret_grants_transferred"`
	TokensRevoked           int `json:"tokens_revoked"`
}

// OffboardPreview is wizard step 1: counts only (D21).
type OffboardPreview struct {
	Sessions int `json:"sessions"`
	Grants   int `json:"grants"`
	Tokens   int `json:"tokens"`
}

// GuestOffboardStore is guest links plus org offboarding.
type GuestOffboardStore interface {
	CreateGuestLink(ctx context.Context, sessionID, createdBy string, expiresAt time.Time, singleUse bool) (*GuestLink, error)
	GetGuestLinkByToken(ctx context.Context, token string) (*GuestLink, error)
	AcceptGuestLink(ctx context.Context, token, userID string) (*GuestLink, error)
	Offboard(ctx context.Context, orgID, fromUser, toUser string) (*OffboardResult, error)
	PreviewOffboard(ctx context.Context, orgID, fromUser string) (*OffboardPreview, error)
}

// guestLinkBook is package-level because MemStore's fields live in store.go,
// which this change does not edit. Books are keyed by store pointer.
type guestLinkBook struct {
	mu      sync.Mutex
	byID    map[string]*GuestLink
	byToken map[string]*GuestLink
}

var guestLinkBooks sync.Map

func guestBook(m *MemStore) *guestLinkBook {
	if v, ok := guestLinkBooks.Load(m); ok {
		return v.(*guestLinkBook)
	}
	book := &guestLinkBook{
		byID:    map[string]*GuestLink{},
		byToken: map[string]*GuestLink{},
	}
	actual, _ := guestLinkBooks.LoadOrStore(m, book)
	return actual.(*guestLinkBook)
}

func cloneGuestLink(g *GuestLink) *GuestLink {
	if g == nil {
		return nil
	}
	cp := *g
	if g.UsedAt != nil {
		t := *g.UsedAt
		cp.UsedAt = &t
	}
	return &cp
}

func (m *MemStore) CreateGuestLink(ctx context.Context, sessionID, createdBy string, expiresAt time.Time, singleUse bool) (*GuestLink, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	createdBy = strings.TrimSpace(createdBy)
	if sessionID == "" || createdBy == "" {
		return nil, errors.New("store: guest link requires session and creator")
	}
	m.mu.RLock()
	sess := m.agentSessions[sessionID]
	if sess == nil {
		m.mu.RUnlock()
		return nil, fmt.Errorf("store: agent session %s: %w", sessionID, ErrNotFound)
	}
	if sess.OwnerUserID != createdBy {
		m.mu.RUnlock()
		return nil, fmt.Errorf("store: only the session owner can create a guest link: %w", ErrForbidden)
	}
	m.mu.RUnlock()
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(guestLinkDefaultTTL)
	} else {
		expiresAt = expiresAt.UTC()
	}
	token, err := inviteToken()
	if err != nil {
		return nil, err
	}
	row := &GuestLink{
		ID:        newID("glink"),
		SessionID: sessionID,
		Token:     token,
		CreatedBy: createdBy,
		ExpiresAt: expiresAt,
		SingleUse: singleUse,
		CreatedAt: time.Now().UTC(),
	}
	book := guestBook(m)
	book.mu.Lock()
	defer book.mu.Unlock()
	book.byID[row.ID] = row
	book.byToken[row.Token] = row
	return cloneGuestLink(row), nil
}

func (m *MemStore) GetGuestLinkByToken(ctx context.Context, token string) (*GuestLink, error) {
	_ = ctx
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("store: guest link: %w", ErrNotFound)
	}
	book := guestBook(m)
	book.mu.Lock()
	defer book.mu.Unlock()
	row := book.byToken[token]
	if row == nil {
		return nil, fmt.Errorf("store: guest link: %w", ErrNotFound)
	}
	return cloneGuestLink(row), nil
}

func (m *MemStore) AcceptGuestLink(ctx context.Context, token, userID string) (*GuestLink, error) {
	token = strings.TrimSpace(token)
	userID = strings.TrimSpace(userID)
	if token == "" || userID == "" {
		return nil, errors.New("store: token and user id are required")
	}
	book := guestBook(m)
	book.mu.Lock()
	link := book.byToken[token]
	if link == nil {
		book.mu.Unlock()
		return nil, fmt.Errorf("store: guest link: %w", ErrNotFound)
	}
	if !link.ExpiresAt.After(time.Now()) {
		book.mu.Unlock()
		return nil, ErrGuestLinkExpired
	}
	if link.SingleUse && link.UsedAt != nil {
		book.mu.Unlock()
		return nil, fmt.Errorf("store: guest link already used: %w", ErrConflict)
	}
	now := time.Now().UTC()
	prevUsed := link.UsedAt
	prevGrantee := link.GranteeUserID
	link.UsedAt = &now
	link.GranteeUserID = userID
	sessionID := link.SessionID
	grantedBy := link.CreatedBy
	book.mu.Unlock()

	if err := m.GrantAgentSession(ctx, sessionID, userID, grantedBy); err != nil {
		book.mu.Lock()
		if cur := book.byToken[token]; cur != nil && cur.GranteeUserID == userID {
			cur.UsedAt = prevUsed
			cur.GranteeUserID = prevGrantee
		}
		book.mu.Unlock()
		return nil, err
	}
	book.mu.Lock()
	defer book.mu.Unlock()
	return cloneGuestLink(book.byToken[token]), nil
}

// Offboard transfers fromUser's agent sessions in this org to toUser,
// transfers secret-key ownership/grants, and leaves session grants intact.
// Visibility is unchanged. Personal projects (empty org_id) are skipped.
// Token revocation is applied by the server layer when a token store is wired.
// The result is counts only.
func (m *MemStore) Offboard(ctx context.Context, orgID, fromUser, toUser string) (*OffboardResult, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	fromUser = strings.TrimSpace(fromUser)
	toUser = strings.TrimSpace(toUser)
	if orgID == "" || fromUser == "" || toUser == "" {
		return nil, errors.New("store: offboard requires org, from user, and to user")
	}
	if fromUser == toUser {
		return nil, errors.New("store: offboard receiver must differ from the departing user")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	inOrg := map[string]bool{}
	for id, p := range m.projects {
		if p != nil && p.OrgID == orgID {
			inOrg[id] = true
		}
	}
	res := &OffboardResult{}
	for _, sess := range m.agentSessions {
		if sess == nil || sess.OwnerUserID != fromUser || !inOrg[sess.ProjectID] {
			continue
		}
		for _, g := range m.agentGrants[sess.ID] {
			if !g.Revoked && g.Grantee != "" {
				res.GrantsKept++
			}
		}
		sess.OwnerUserID = toUser
		res.SessionsTransferred++
	}
	res.SecretGrantsTransferred = m.transferSecretGrantsLocked(fromUser, toUser)
	return res, nil
}

func (m *MemStore) transferSecretGrantsLocked(fromUser, toUser string) int {
	moved := 0
	for _, row := range m.secretKeys {
		if row == nil {
			continue
		}
		if row.Owner == fromUser {
			row.Owner = toUser
			moved++
		}
		if row.Grants == nil {
			continue
		}
		if _, ok := row.Grants[fromUser]; ok {
			delete(row.Grants, fromUser)
			row.Grants[toUser] = struct{}{}
			moved++
		}
	}
	return moved
}

func (m *MemStore) countSecretGrantsLocked(userID string) int {
	n := 0
	for _, row := range m.secretKeys {
		if row == nil {
			continue
		}
		if row.Owner == userID {
			n++
		}
		if row.Grants != nil {
			if _, ok := row.Grants[userID]; ok {
				n++
			}
		}
	}
	return n
}

// PreviewOffboard returns D21 counts for wizard step 1 without mutating state.
func (m *MemStore) PreviewOffboard(ctx context.Context, orgID, fromUser string) (*OffboardPreview, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	fromUser = strings.TrimSpace(fromUser)
	if orgID == "" || fromUser == "" {
		return nil, errors.New("store: offboard preview requires org and user")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	inOrg := map[string]bool{}
	for id, p := range m.projects {
		if p != nil && p.OrgID == orgID {
			inOrg[id] = true
		}
	}
	sessions := 0
	for _, sess := range m.agentSessions {
		if sess != nil && sess.OwnerUserID == fromUser && inOrg[sess.ProjectID] {
			sessions++
		}
	}
	return &OffboardPreview{
		Sessions: sessions,
		Grants:   m.countSecretGrantsLocked(fromUser),
		Tokens:   0, // server fills from API token store when configured
	}, nil
}

const guestLinkReturning = `id::text, session_id::text, token, COALESCE(created_by,''), expires_at, single_use, used_at, COALESCE(grantee_user_id,''), created_at`

func scanGuestLink(row pgx.Row) (*GuestLink, error) {
	var g GuestLink
	if err := row.Scan(&g.ID, &g.SessionID, &g.Token, &g.CreatedBy, &g.ExpiresAt, &g.SingleUse, &g.UsedAt, &g.GranteeUserID, &g.CreatedAt); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *PostgresStore) CreateGuestLink(ctx context.Context, sessionID, createdBy string, expiresAt time.Time, singleUse bool) (*GuestLink, error) {
	sessionID = strings.TrimSpace(sessionID)
	createdBy = strings.TrimSpace(createdBy)
	if !looksLikeUUID(sessionID) {
		return nil, errors.New("store: session id must be a uuid")
	}
	if createdBy == "" {
		return nil, errors.New("store: created_by is required")
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(guestLinkDefaultTTL)
	} else {
		expiresAt = expiresAt.UTC()
	}
	var owner string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(owner_user_id::text,'') FROM agent_sessions WHERE id = $1::uuid`, sessionID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: agent session %s: %w", sessionID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if owner != createdBy {
		return nil, fmt.Errorf("store: only the session owner can create a guest link: %w", ErrForbidden)
	}
	token, err := inviteToken()
	if err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO guest_links (session_id, token, created_by, expires_at, single_use)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING `+guestLinkReturning,
		sessionID, token, createdBy, expiresAt, singleUse)
	g, err := scanGuestLink(row)
	if err != nil {
		return nil, fmt.Errorf("store: create guest link: %w", err)
	}
	return g, nil
}

func (s *PostgresStore) GetGuestLinkByToken(ctx context.Context, token string) (*GuestLink, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("store: guest link: %w", ErrNotFound)
	}
	row := s.pool.QueryRow(ctx, `SELECT `+guestLinkReturning+` FROM guest_links WHERE token = $1`, token)
	g, err := scanGuestLink(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: guest link: %w", ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (s *PostgresStore) AcceptGuestLink(ctx context.Context, token, userID string) (*GuestLink, error) {
	token = strings.TrimSpace(token)
	userID = strings.TrimSpace(userID)
	if token == "" || userID == "" {
		return nil, errors.New("store: token and user id are required")
	}
	if !looksLikeUUID(userID) {
		return nil, errors.New("store: grantee id must be a uuid")
	}
	link, err := s.GetGuestLinkByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if !link.ExpiresAt.After(time.Now()) {
		return nil, ErrGuestLinkExpired
	}
	if link.SingleUse && link.UsedAt != nil {
		return nil, fmt.Errorf("store: guest link already used: %w", ErrConflict)
	}
	q := `UPDATE guest_links SET used_at = now(), grantee_user_id = $2 WHERE token = $1 AND expires_at > now()`
	if link.SingleUse {
		q += ` AND used_at IS NULL`
	}
	q += ` RETURNING ` + guestLinkReturning
	updated, err := scanGuestLink(s.pool.QueryRow(ctx, q, token, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		again, gerr := s.GetGuestLinkByToken(ctx, token)
		if gerr != nil {
			return nil, gerr
		}
		if !again.ExpiresAt.After(time.Now()) {
			return nil, ErrGuestLinkExpired
		}
		return nil, fmt.Errorf("store: guest link already used: %w", ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("store: accept guest link: %w", err)
	}
	if err := s.GrantAgentSession(ctx, updated.SessionID, userID, updated.CreatedBy); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *PostgresStore) Offboard(ctx context.Context, orgID, fromUser, toUser string) (*OffboardResult, error) {
	orgID = strings.TrimSpace(orgID)
	fromUser = strings.TrimSpace(fromUser)
	toUser = strings.TrimSpace(toUser)
	if orgID == "" || fromUser == "" || toUser == "" {
		return nil, errors.New("store: offboard requires org, from user, and to user")
	}
	if fromUser == toUser {
		return nil, errors.New("store: offboard receiver must differ from the departing user")
	}
	if !looksLikeUUID(orgID) || !looksLikeUUID(fromUser) || !looksLikeUUID(toUser) {
		return nil, errors.New("store: offboard ids must be uuids")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var grants int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM session_grants g
		WHERE g.revoked_at IS NULL
		  AND g.session_id IN (
		    SELECT s.id FROM agent_sessions s
		    WHERE s.owner_user_id = $1::uuid
		      AND s.project_id IN (SELECT id FROM projects WHERE org_id = $2::uuid)
		  )`, fromUser, orgID).Scan(&grants); err != nil {
		return nil, fmt.Errorf("store: offboard grants: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE agent_sessions SET owner_user_id = $1::uuid
		WHERE owner_user_id = $2::uuid
		  AND project_id IN (SELECT id FROM projects WHERE org_id = $3::uuid)`,
		toUser, fromUser, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: offboard: %w", err)
	}
	var ownedMoved int64
	otag, err := tx.Exec(ctx, `
		UPDATE secret_keys SET owner_user_id = $1
		WHERE owner_user_id = $2`, toUser, fromUser)
	if err != nil {
		return nil, fmt.Errorf("store: offboard secret owners: %w", err)
	}
	ownedMoved = otag.RowsAffected()
	// Transfer decrypt grants: insert for receiver, drop from leaver.
	gtag, err := tx.Exec(ctx, `
		INSERT INTO secret_key_grants (blob_id, grantee_user_id)
		SELECT blob_id, $1 FROM secret_key_grants WHERE grantee_user_id = $2
		ON CONFLICT DO NOTHING`, toUser, fromUser)
	if err != nil {
		return nil, fmt.Errorf("store: offboard secret grants copy: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM secret_key_grants WHERE grantee_user_id = $1`, fromUser); err != nil {
		return nil, fmt.Errorf("store: offboard secret grants clear: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &OffboardResult{
		SessionsTransferred:     int(tag.RowsAffected()),
		GrantsKept:              grants,
		SecretGrantsTransferred: int(ownedMoved) + int(gtag.RowsAffected()),
	}, nil
}

func (s *PostgresStore) PreviewOffboard(ctx context.Context, orgID, fromUser string) (*OffboardPreview, error) {
	orgID = strings.TrimSpace(orgID)
	fromUser = strings.TrimSpace(fromUser)
	if orgID == "" || fromUser == "" {
		return nil, errors.New("store: offboard preview requires org and user")
	}
	if !looksLikeUUID(orgID) || !looksLikeUUID(fromUser) {
		return nil, errors.New("store: offboard ids must be uuids")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var sessions int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM agent_sessions
		WHERE owner_user_id = $1::uuid
		  AND project_id IN (SELECT id FROM projects WHERE org_id = $2::uuid)`,
		fromUser, orgID).Scan(&sessions); err != nil {
		return nil, err
	}
	var owned, granted int
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM secret_keys WHERE owner_user_id = $1`, fromUser).Scan(&owned)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM secret_key_grants WHERE grantee_user_id = $1`, fromUser).Scan(&granted)
	return &OffboardPreview{
		Sessions: sessions,
		Grants:   owned + granted,
		Tokens:   0,
	}, nil
}

var _ GuestOffboardStore = (*MemStore)(nil)
var _ GuestOffboardStore = (*PostgresStore)(nil)
