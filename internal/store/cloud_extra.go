package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"central-memory/internal/capture"

	"github.com/jackc/pgx/v5"
)

// Teleport is one send of a session to another person (spec 7.7).
// The preview is already redacted. Session titles are not stored here.
type Teleport struct {
	ID         string     `json:"id"`
	SessionID  string     `json:"session_id"`
	FromUserID string     `json:"from_user_id"`
	ToUserID   string     `json:"to_user_id"`
	Status     string     `json:"status"`
	Preview    string     `json:"preview"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type secretKeyRec struct {
	Owner   string
	Wrapped []byte
	Grants  map[string]struct{}
}

func (m *MemStore) RevokeAgentSessionGrant(ctx context.Context, sessionID, granteeUserID string) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	granteeUserID = strings.TrimSpace(granteeUserID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentSessions[sessionID] == nil {
		return ErrNotFound
	}
	found := false
	for i, g := range m.agentGrants[sessionID] {
		if !g.Revoked && g.Grantee == granteeUserID {
			m.agentGrants[sessionID][i].Revoked = true
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (m *MemStore) SetAgentSessionVisibility(ctx context.Context, sessionID, visibility string) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	visibility = strings.TrimSpace(visibility)
	if visibility != "private" && visibility != "team" {
		return errors.New("store: visibility must be private or team")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.agentSessions[sessionID]
	if row == nil {
		return ErrNotFound
	}
	row.Visibility = visibility
	return nil
}

func (m *MemStore) GetSessionVersion(ctx context.Context, sessionID string, version int) (*SessionVersion, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.sessionVersions[strings.TrimSpace(sessionID)] {
		if v.Version == version {
			cp := v
			cp.Manifest = append([]byte(nil), v.Manifest...)
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListSessionVersions(ctx context.Context, sessionID string) ([]SessionVersion, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	src := m.sessionVersions[strings.TrimSpace(sessionID)]
	out := append([]SessionVersion(nil), src...)
	if out == nil {
		out = []SessionVersion{}
	}
	return out, nil
}

func (m *MemStore) StorageUsage(ctx context.Context, scope string) (used, cap int64, err error) {
	_ = ctx
	scope = strings.TrimSpace(scope)
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec := m.storageUsage[scope]
	used, cap = rec.Used, rec.Cap
	if cap <= 0 {
		cap = m.planCapForScopeLocked(scope)
	}
	return used, cap, nil
}

func (m *MemStore) planCapForScopeLocked(scope string) int64 {
	if strings.HasPrefix(scope, "user:") {
		userID := strings.TrimPrefix(scope, "user:")
		return m.planStorageCapLocked(OwnerUser, userID)
	}
	if p := m.projects[scope]; p != nil && strings.TrimSpace(p.CreatedBy) != "" {
		return m.planStorageCapLocked(OwnerUser, p.CreatedBy)
	}
	if strings.HasPrefix(scope, "project:") {
		pid := strings.TrimPrefix(scope, "project:")
		if p := m.projects[pid]; p != nil && strings.TrimSpace(p.CreatedBy) != "" {
			return m.planStorageCapLocked(OwnerUser, p.CreatedBy)
		}
	}
	return PlanStorageBytes(PlanFree)
}

func (m *MemStore) planStorageCapLocked(ownerType, ownerID string) int64 {
	planID := PlanFree
	if m.billingSubs != nil {
		if sub, ok := m.billingSubs[subKey(ownerType, ownerID)]; ok && sub != nil {
			planID = sub.PlanID
		}
	}
	if m.plans != nil {
		if p, ok := m.plans[planID]; ok && p.Limits.StorageBytes > 0 {
			return p.Limits.StorageBytes
		}
	}
	return PlanStorageBytes(planID)
}

func (m *MemStore) PruneSessionVersions(ctx context.Context, now time.Time) (int, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pruneVersionsLocked(now), nil
}

func (m *MemStore) CreateTeleport(ctx context.Context, sessionID, fromUser, toUser, preview string) (*Teleport, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	fromUser = strings.TrimSpace(fromUser)
	toUser = strings.TrimSpace(toUser)
	if sessionID == "" || fromUser == "" || toUser == "" {
		return nil, errors.New("store: teleport requires session, sender, and recipient")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.teleports == nil {
		m.teleports = map[string]*Teleport{}
	}
	if m.agentSessions[sessionID] == nil {
		return nil, ErrNotFound
	}
	row := &Teleport{
		ID: newID("tp"), SessionID: sessionID, FromUserID: fromUser, ToUserID: toUser,
		Status: "sent", Preview: preview, CreatedAt: time.Now().UTC(),
	}
	m.teleports[row.ID] = row
	cp := *row
	return &cp, nil
}

func (m *MemStore) ListTeleports(ctx context.Context, userID, box string) ([]Teleport, error) {
	_ = ctx
	userID = strings.TrimSpace(userID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Teleport
	for _, row := range m.teleports {
		if row == nil || row.RevokedAt != nil {
			continue
		}
		switch box {
		case "sent":
			if row.FromUserID != userID {
				continue
			}
		default:
			if row.ToUserID != userID {
				continue
			}
		}
		out = append(out, *row)
	}
	if out == nil {
		out = []Teleport{}
	}
	return out, nil
}

func (m *MemStore) GetTeleport(ctx context.Context, id string) (*Teleport, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	row := m.teleports[strings.TrimSpace(id)]
	if row == nil {
		return nil, ErrNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *MemStore) SetTeleportStatus(ctx context.Context, id, status string, revoke bool) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.teleports[strings.TrimSpace(id)]
	if row == nil {
		return ErrNotFound
	}
	if revoke {
		now := time.Now().UTC()
		row.RevokedAt = &now
		row.Status = "revoked"
		return nil
	}
	row.Status = status
	return nil
}

func (m *MemStore) PutSecretKey(ctx context.Context, blobID, owner string, wrapped []byte) error {
	_ = ctx
	blobID = strings.TrimSpace(blobID)
	owner = strings.TrimSpace(owner)
	if blobID == "" || owner == "" || len(wrapped) == 0 {
		return errors.New("store: secret key requires blob, owner, and wrapped key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secretKeys == nil {
		m.secretKeys = map[string]*secretKeyRec{}
	}
	if _, ok := m.secretKeys[blobID]; ok {
		return ErrConflict
	}
	m.secretKeys[blobID] = &secretKeyRec{Owner: owner, Wrapped: append([]byte(nil), wrapped...), Grants: map[string]struct{}{}}
	return nil
}

func (m *MemStore) SecretKey(ctx context.Context, blobID string) (owner string, wrapped []byte, grants []string, err error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	row := m.secretKeys[strings.TrimSpace(blobID)]
	if row == nil {
		return "", nil, nil, ErrNotFound
	}
	for g := range row.Grants {
		grants = append(grants, g)
	}
	return row.Owner, append([]byte(nil), row.Wrapped...), grants, nil
}

func (m *MemStore) GrantSecretKey(ctx context.Context, blobID, userID string, grant bool) error {
	_ = ctx
	blobID = strings.TrimSpace(blobID)
	userID = strings.TrimSpace(userID)
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.secretKeys[blobID]
	if row == nil {
		return ErrNotFound
	}
	if row.Grants == nil {
		row.Grants = map[string]struct{}{}
	}
	if grant {
		row.Grants[userID] = struct{}{}
	} else {
		delete(row.Grants, userID)
	}
	return nil
}

func (s *PostgresStore) RevokeAgentSessionGrant(ctx context.Context, sessionID, granteeUserID string) error {
	if !looksLikeUUID(sessionID) || !looksLikeUUID(granteeUserID) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE session_grants SET revoked_at = now()
		WHERE session_id = $1::uuid AND grantee_user_id = $2::uuid AND revoked_at IS NULL`,
		sessionID, granteeUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) SetAgentSessionVisibility(ctx context.Context, sessionID, visibility string) error {
	if visibility != "private" && visibility != "team" {
		return errors.New("store: visibility must be private or team")
	}
	if !looksLikeUUID(sessionID) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agent_sessions SET visibility = $2 WHERE id = $1::uuid`, sessionID, visibility)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) GetSessionVersion(ctx context.Context, sessionID string, version int) (*SessionVersion, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, session_id::text, version, created_at, turn_count,
		       COALESCE(manifest_sha256,''), manifest, COALESCE(uploaded_by_user_id::text,''), state
		FROM session_versions WHERE session_id = $1::uuid AND version = $2`, sessionID, version)
	return scanSessionVersion(row)
}

func (s *PostgresStore) StorageUsage(ctx context.Context, scope string) (int64, int64, error) {
	var used, cap int64
	err := s.pool.QueryRow(ctx, `SELECT bytes_used, bytes_cap FROM storage_usage WHERE plan_scope = $1`, scope).Scan(&used, &cap)
	if errors.Is(err, pgx.ErrNoRows) {
		used, cap = 0, 0
		err = nil
	}
	if err != nil {
		return 0, 0, err
	}
	if cap <= 0 {
		planCap, perr := s.planCapForScope(ctx, scope)
		if perr != nil {
			return used, PlanStorageBytes(PlanFree), nil
		}
		cap = planCap
	}
	return used, cap, nil
}

func (s *PostgresStore) planCapForScope(ctx context.Context, scope string) (int64, error) {
	ownerType, ownerID := OwnerUser, ""
	switch {
	case strings.HasPrefix(scope, "user:"):
		ownerID = strings.TrimPrefix(scope, "user:")
	case strings.HasPrefix(scope, "project:"):
		pid := strings.TrimPrefix(scope, "project:")
		_ = s.pool.QueryRow(ctx, `SELECT COALESCE(created_by::text, '') FROM projects WHERE id::text = $1`, pid).Scan(&ownerID)
	default:
		_ = s.pool.QueryRow(ctx, `SELECT COALESCE(created_by::text, '') FROM projects WHERE id::text = $1`, scope).Scan(&ownerID)
	}
	if ownerID == "" {
		return PlanStorageBytes(PlanFree), nil
	}
	sub, err := s.GetSubscription(ctx, ownerType, ownerID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return PlanStorageBytes(PlanFree), nil
		}
		return 0, err
	}
	plan, err := s.GetPlan(ctx, sub.PlanID)
	if err != nil {
		return PlanStorageBytes(sub.PlanID), nil
	}
	if plan.Limits.StorageBytes > 0 {
		return plan.Limits.StorageBytes, nil
	}
	return PlanStorageBytes(plan.ID), nil
}

func (s *PostgresStore) CreateTeleport(ctx context.Context, sessionID, fromUser, toUser, preview string) (*Teleport, error) {
	if !looksLikeUUID(sessionID) || strings.TrimSpace(fromUser) == "" || strings.TrimSpace(toUser) == "" {
		return nil, errors.New("store: teleport requires a session uuid, sender, and recipient")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO teleports (session_id, from_user_id, to_user_id, preview)
		VALUES ($1::uuid, $2, $3, $4)
		RETURNING id::text, session_id::text, from_user_id, to_user_id, status, preview, created_at, revoked_at`,
		sessionID, fromUser, toUser, preview)
	return scanTeleport(row)
}

func scanTeleport(row pgx.Row) (*Teleport, error) {
	var tp Teleport
	if err := row.Scan(&tp.ID, &tp.SessionID, &tp.FromUserID, &tp.ToUserID, &tp.Status, &tp.Preview, &tp.CreatedAt, &tp.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &tp, nil
}

func (s *PostgresStore) ListTeleports(ctx context.Context, userID, box string) ([]Teleport, error) {
	col := "to_user_id"
	if box == "sent" {
		col = "from_user_id"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, session_id::text, from_user_id, to_user_id, status, preview, created_at, revoked_at
		FROM teleports
		WHERE `+col+` = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Teleport
	for rows.Next() {
		tp, err := scanTeleport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *tp)
	}
	if out == nil {
		out = []Teleport{}
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetTeleport(ctx context.Context, id string) (*Teleport, error) {
	if !looksLikeUUID(id) {
		return nil, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, session_id::text, from_user_id, to_user_id, status, preview, created_at, revoked_at
		FROM teleports WHERE id = $1::uuid`, id)
	return scanTeleport(row)
}

func (s *PostgresStore) SetTeleportStatus(ctx context.Context, id, status string, revoke bool) error {
	if !looksLikeUUID(id) {
		return ErrNotFound
	}
	var tag interface{ RowsAffected() int64 }
	var err error
	if revoke {
		tag, err = s.pool.Exec(ctx, `UPDATE teleports SET status = 'revoked', revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, id)
	} else {
		tag, err = s.pool.Exec(ctx, `UPDATE teleports SET status = $2 WHERE id = $1::uuid AND revoked_at IS NULL`, id, status)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) PutSecretKey(ctx context.Context, blobID, owner string, wrapped []byte) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO secret_keys (blob_id, owner_user_id, wrapped)
		VALUES ($1, $2, $3)
		ON CONFLICT (blob_id) DO NOTHING`, blobID, owner, wrapped)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) SecretKey(ctx context.Context, blobID string) (string, []byte, []string, error) {
	var owner string
	var wrapped []byte
	err := s.pool.QueryRow(ctx, `SELECT owner_user_id, wrapped FROM secret_keys WHERE blob_id = $1`, blobID).Scan(&owner, &wrapped)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, ErrNotFound
	}
	if err != nil {
		return "", nil, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT grantee_user_id FROM secret_key_grants WHERE blob_id = $1`, blobID)
	if err != nil {
		return "", nil, nil, err
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return "", nil, nil, err
		}
		grants = append(grants, g)
	}
	return owner, wrapped, grants, rows.Err()
}

func (s *PostgresStore) GrantSecretKey(ctx context.Context, blobID, userID string, grant bool) error {
	if grant {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO secret_key_grants (blob_id, grantee_user_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, blobID, userID)
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM secret_key_grants WHERE blob_id = $1 AND grantee_user_id = $2`, blobID, userID)
	return err
}

func (s *PostgresStore) PruneSessionVersions(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT session_id::text, id::text, created_at FROM session_versions`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	bySession := map[string][]capture.VersionStamp{}
	for rows.Next() {
		var sid, id string
		var at time.Time
		if err := rows.Scan(&sid, &id, &at); err != nil {
			return 0, err
		}
		bySession[sid] = append(bySession[sid], capture.VersionStamp{ID: id, At: at})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, stamps := range bySession {
		for _, id := range ApplyRetention(stamps, now) {
			if _, err := s.pool.Exec(ctx, `DELETE FROM session_versions WHERE id = $1::uuid`, id); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
