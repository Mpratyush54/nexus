package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditEvent is one append-only audit row (product spec 10.4).
// Rows carry identifiers and metadata, never session content.
type AuditEvent struct {
	ID           string         `json:"id"`
	At           time.Time      `json:"at"`
	TenantID     string         `json:"tenant_id,omitempty"`
	ActorUserID  string         `json:"actor_user_id,omitempty"`
	ActorKind    string         `json:"actor_kind"`
	Action       string         `json:"action"`
	ResourceKind string         `json:"resource_kind,omitempty"`
	ResourceID   string         `json:"resource_id,omitempty"`
	ProjectID    string         `json:"project_id,omitempty"`
	IP           string         `json:"ip,omitempty"`
	DeviceID     string         `json:"device_id,omitempty"`
	UserAgent    string         `json:"user_agent,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	RequestID    string         `json:"request_id,omitempty"`
	Outcome      string         `json:"outcome"`
	PrevHash     string         `json:"prev_hash,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// SessionContentStore is the owner-or-grantee ACL for session content
// (product spec 4.5 / D9) plus the audit log those handlers emit.
type SessionContentStore interface {
	GetSessionContentOwner(ctx context.Context, sessionID string) (ownerUserID, projectID string, found bool, err error)
	SetSessionContentOwner(ctx context.Context, sessionID, projectID, ownerUserID string) error
	AssignSessionOwner(ctx context.Context, sessionID, ownerUserID string) error
	DeleteOwnerlessSessionContent(ctx context.Context, sessionID string) error
	SessionHasContent(ctx context.Context, sessionID string) (bool, error)
	HasSessionContentGrant(ctx context.Context, sessionID, userID string) (bool, error)
	GrantSessionContent(ctx context.Context, sessionID, projectID, granteeUserID, grantedBy string) error
	AppendAudit(ctx context.Context, ev AuditEvent) error
	ListAuditEvents(ctx context.Context, projectID, action string, limit int) ([]AuditEvent, error)
}

type sessionContentOwner struct {
	ProjectID   string
	OwnerUserID string
}

type sessionContentGrant struct {
	ProjectID string
	GrantedBy string
	Revoked   bool
}

var validActorKinds = map[string]bool{
	"user": true, "admin": true, "super_admin": true,
	"support": true, "system": true, "agent": true,
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func nullUUIDStrict(s string) any {
	s = strings.TrimSpace(s)
	if !looksLikeUUID(s) {
		return nil
	}
	return s
}

func normalizeAudit(ev *AuditEvent) error {
	ev.ActorKind = strings.TrimSpace(ev.ActorKind)
	if ev.ActorKind == "" {
		ev.ActorKind = "user"
	}
	if !validActorKinds[ev.ActorKind] {
		return fmt.Errorf("store: invalid audit actor_kind %q", ev.ActorKind)
	}
	ev.Action = strings.TrimSpace(ev.Action)
	if ev.Action == "" {
		return errors.New("store: audit action is required")
	}
	ev.Outcome = strings.TrimSpace(ev.Outcome)
	if ev.Outcome == "" {
		ev.Outcome = "ok"
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	} else {
		ev.At = ev.At.UTC()
	}
	// Postgres timestamptz stores microseconds. Hash the value that round-trips.
	ev.At = ev.At.Truncate(time.Microsecond)
	return nil
}

func auditMetadataJSON(meta map[string]any) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// auditChainHash links an event to the previous hash for its tenant.
func auditChainHash(prev string, ev AuditEvent, metadataJSON string) string {
	payload := strings.Join([]string{
		prev,
		ev.At.UTC().Format(time.RFC3339Nano),
		ev.ActorUserID,
		ev.ActorKind,
		ev.Action,
		ev.ResourceKind,
		ev.ResourceID,
		ev.ProjectID,
		ev.TenantID,
		ev.Outcome,
		metadataJSON,
	}, "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func (m *MemStore) ensureACLMaps() {
	if m.sessionOwners == nil {
		m.sessionOwners = make(map[string]sessionContentOwner)
	}
	if m.sessionGrants == nil {
		m.sessionGrants = make(map[string]map[string]sessionContentGrant)
	}
}

func (m *MemStore) sessionHasContentLocked(sessionID string) bool {
	if _, ok := m.sessionOwners[sessionID]; ok {
		return true
	}
	for _, s := range m.snapshots {
		if s.SessionID == sessionID {
			return true
		}
	}
	for _, op := range m.fileOps {
		if op.SessionID == sessionID {
			return true
		}
	}
	for _, te := range m.toolExecs {
		if te.SessionID == sessionID {
			return true
		}
	}
	return false
}

func (m *MemStore) GetSessionContentOwner(ctx context.Context, sessionID string) (string, string, bool, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", "", false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	row, ok := m.sessionOwners[sessionID]
	if !ok {
		return "", "", false, nil
	}
	return row.OwnerUserID, row.ProjectID, true, nil
}

func (m *MemStore) SetSessionContentOwner(ctx context.Context, sessionID, projectID, ownerUserID string) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if sessionID == "" || projectID == "" || ownerUserID == "" {
		return errors.New("store: session owner requires session, project, and user")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureACLMaps()
	if row, ok := m.sessionOwners[sessionID]; ok {
		if row.OwnerUserID == ownerUserID {
			return nil
		}
		return fmt.Errorf("store: session owner already set: %w", ErrConflict)
	}
	m.sessionOwners[sessionID] = sessionContentOwner{ProjectID: projectID, OwnerUserID: ownerUserID}
	return nil
}

func (m *MemStore) AssignSessionOwner(ctx context.Context, sessionID, ownerUserID string) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if sessionID == "" || ownerUserID == "" {
		return errors.New("store: assign owner requires session and user")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureACLMaps()
	if !m.sessionHasContentLocked(sessionID) {
		return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
	}
	if row, ok := m.sessionOwners[sessionID]; ok && row.OwnerUserID != "" {
		return fmt.Errorf("store: session already has an owner: %w", ErrConflict)
	}
	projectID := ""
	if row, ok := m.sessionOwners[sessionID]; ok {
		projectID = row.ProjectID
	}
	for i := range m.snapshots {
		if m.snapshots[i].SessionID != sessionID {
			continue
		}
		if m.snapshots[i].OwnerUserID != "" {
			return fmt.Errorf("store: session already has an owner: %w", ErrConflict)
		}
		if projectID == "" {
			projectID = m.snapshots[i].ProjectID
		}
	}
	if projectID == "" {
		for _, op := range m.fileOps {
			if op.SessionID == sessionID && op.ProjectID != "" {
				projectID = op.ProjectID
				break
			}
		}
	}
	if projectID == "" {
		return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
	}
	for i := range m.snapshots {
		if m.snapshots[i].SessionID == sessionID {
			m.snapshots[i].OwnerUserID = ownerUserID
		}
	}
	m.sessionOwners[sessionID] = sessionContentOwner{ProjectID: projectID, OwnerUserID: ownerUserID}
	return nil
}

func (m *MemStore) DeleteOwnerlessSessionContent(ctx context.Context, sessionID string) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("store: session id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureACLMaps()
	if !m.sessionHasContentLocked(sessionID) {
		return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
	}
	if row, ok := m.sessionOwners[sessionID]; ok && row.OwnerUserID != "" {
		return fmt.Errorf("store: session has an owner: %w", ErrConflict)
	}
	for _, s := range m.snapshots {
		if s.SessionID == sessionID && s.OwnerUserID != "" {
			return fmt.Errorf("store: session has an owner: %w", ErrConflict)
		}
	}
	keptSnaps := make([]SessionSnapshot, 0, len(m.snapshots))
	for _, s := range m.snapshots {
		if s.SessionID != sessionID {
			keptSnaps = append(keptSnaps, s)
		}
	}
	m.snapshots = keptSnaps
	keptOps := make([]FileOperation, 0, len(m.fileOps))
	for _, op := range m.fileOps {
		if op.SessionID != sessionID {
			keptOps = append(keptOps, op)
		}
	}
	m.fileOps = keptOps
	keptTools := make([]ToolExecution, 0, len(m.toolExecs))
	for _, te := range m.toolExecs {
		if te.SessionID != sessionID {
			keptTools = append(keptTools, te)
		}
	}
	m.toolExecs = keptTools
	delete(m.sessionOwners, sessionID)
	delete(m.sessionGrants, sessionID)
	return nil
}

func (m *MemStore) SessionHasContent(ctx context.Context, sessionID string) (bool, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionHasContentLocked(sessionID), nil
}

func (m *MemStore) HasSessionContentGrant(ctx context.Context, sessionID, userID string) (bool, error) {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	userID = strings.TrimSpace(userID)
	if sessionID == "" || userID == "" {
		return false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	g, ok := m.sessionGrants[sessionID][userID]
	return ok && !g.Revoked, nil
}

func (m *MemStore) GrantSessionContent(ctx context.Context, sessionID, projectID, granteeUserID, grantedBy string) error {
	_ = ctx
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	granteeUserID = strings.TrimSpace(granteeUserID)
	grantedBy = strings.TrimSpace(grantedBy)
	if sessionID == "" || projectID == "" || granteeUserID == "" {
		return errors.New("store: session grant requires session, project, and grantee")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureACLMaps()
	if m.sessionGrants[sessionID] == nil {
		m.sessionGrants[sessionID] = make(map[string]sessionContentGrant)
	}
	if g, ok := m.sessionGrants[sessionID][granteeUserID]; ok && !g.Revoked {
		return nil
	}
	m.sessionGrants[sessionID][granteeUserID] = sessionContentGrant{ProjectID: projectID, GrantedBy: grantedBy}
	return nil
}

func (m *MemStore) AppendAudit(ctx context.Context, ev AuditEvent) error {
	_ = ctx
	if err := normalizeAudit(&ev); err != nil {
		return err
	}
	meta, err := auditMetadataJSON(ev.Metadata)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := ""
	for i := len(m.auditEvents) - 1; i >= 0; i-- {
		if m.auditEvents[i].TenantID == ev.TenantID {
			prev = m.auditEvents[i].PrevHash
			break
		}
	}
	ev.PrevHash = auditChainHash(prev, ev, meta)
	if ev.ID == "" {
		ev.ID = newID("aud")
	}
	m.auditEvents = append(m.auditEvents, ev)
	return nil
}

func (m *MemStore) ListAuditEvents(ctx context.Context, projectID, action string, limit int) ([]AuditEvent, error) {
	_ = ctx
	projectID = strings.TrimSpace(projectID)
	action = strings.TrimSpace(action)
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []AuditEvent
	for i := len(m.auditEvents) - 1; i >= 0; i-- {
		ev := m.auditEvents[i]
		if projectID != "" && ev.ProjectID != projectID {
			continue
		}
		if action != "" && ev.Action != action {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	if out == nil {
		out = []AuditEvent{}
	}
	return out, nil
}

func (s *PostgresStore) GetSessionContentOwner(ctx context.Context, sessionID string) (string, string, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if !looksLikeUUID(sessionID) {
		return "", "", false, nil
	}
	var owner, projectID string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(owner_user_id::text, ''), project_id::text
		FROM session_content_owners
		WHERE session_id = $1::uuid`, sessionID).Scan(&owner, &projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("store: session owner: %w", err)
	}
	return owner, projectID, true, nil
}

func (s *PostgresStore) SetSessionContentOwner(ctx context.Context, sessionID, projectID, ownerUserID string) error {
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if !looksLikeUUID(sessionID) || !looksLikeUUID(projectID) || !looksLikeUUID(ownerUserID) {
		return errors.New("store: session owner ids must be uuids")
	}
	var inserted string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO session_content_owners (session_id, project_id, owner_user_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT (session_id) DO NOTHING
		RETURNING COALESCE(owner_user_id::text, '')`,
		sessionID, projectID, ownerUserID).Scan(&inserted)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("store: set session owner: %w", err)
	}
	var existing string
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(owner_user_id::text, '')
		FROM session_content_owners WHERE session_id = $1::uuid`, sessionID).Scan(&existing); err != nil {
		return fmt.Errorf("store: set session owner: %w", err)
	}
	if existing == ownerUserID {
		return nil
	}
	return fmt.Errorf("store: session owner already set: %w", ErrConflict)
}

func (s *PostgresStore) AssignSessionOwner(ctx context.Context, sessionID, ownerUserID string) error {
	sessionID = strings.TrimSpace(sessionID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if !looksLikeUUID(sessionID) || !looksLikeUUID(ownerUserID) {
		return errors.New("store: assign owner ids must be uuids")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var has bool
	if err := tx.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM session_content_owners WHERE session_id = $1::uuid)
		  OR EXISTS(SELECT 1 FROM session_snapshots WHERE session_id = $1::uuid)
		  OR EXISTS(SELECT 1 FROM session_file_operations WHERE session_id = $1::uuid)
		  OR EXISTS(SELECT 1 FROM session_tool_executions WHERE session_id = $1::uuid)`,
		sessionID).Scan(&has); err != nil {
		return fmt.Errorf("store: assign owner: %w", err)
	}
	if !has {
		return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
	}
	var owned bool
	if err := tx.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM session_content_owners WHERE session_id = $1::uuid AND owner_user_id IS NOT NULL)
		  OR EXISTS(SELECT 1 FROM session_snapshots WHERE session_id = $1::uuid AND owner_user_id IS NOT NULL)`,
		sessionID).Scan(&owned); err != nil {
		return fmt.Errorf("store: assign owner: %w", err)
	}
	if owned {
		return fmt.Errorf("store: session already has an owner: %w", ErrConflict)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session_snapshots
		SET owner_user_id = $2::uuid
		WHERE session_id = $1::uuid AND owner_user_id IS NULL`, sessionID, ownerUserID); err != nil {
		return fmt.Errorf("store: assign owner: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE session_content_owners
		SET owner_user_id = $2::uuid
		WHERE session_id = $1::uuid AND owner_user_id IS NULL`, sessionID, ownerUserID)
	if err != nil {
		return fmt.Errorf("store: assign owner: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var assigned string
		err := tx.QueryRow(ctx, `
			INSERT INTO session_content_owners (session_id, project_id, owner_user_id)
			SELECT $1::uuid, project_id, $2::uuid
			FROM (
			  SELECT project_id FROM session_snapshots WHERE session_id = $1::uuid
			  UNION ALL
			  SELECT project_id FROM session_file_operations WHERE session_id = $1::uuid
			  UNION ALL
			  SELECT project_id FROM session_tool_executions WHERE session_id = $1::uuid
			  LIMIT 1
			) src
			RETURNING session_id::text`, sessionID, ownerUserID).Scan(&assigned)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("store: assign owner: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) DeleteOwnerlessSessionContent(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if !looksLikeUUID(sessionID) {
		return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var has, owned bool
	if err := tx.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM session_content_owners WHERE session_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM session_snapshots WHERE session_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM session_file_operations WHERE session_id = $1::uuid)
		    OR EXISTS(SELECT 1 FROM session_tool_executions WHERE session_id = $1::uuid),
		  EXISTS(SELECT 1 FROM session_content_owners WHERE session_id = $1::uuid AND owner_user_id IS NOT NULL)
		    OR EXISTS(SELECT 1 FROM session_snapshots WHERE session_id = $1::uuid AND owner_user_id IS NOT NULL)`,
		sessionID).Scan(&has, &owned); err != nil {
		return fmt.Errorf("store: delete session content: %w", err)
	}
	if !has {
		return fmt.Errorf("store: session %s: %w", sessionID, ErrNotFound)
	}
	if owned {
		return fmt.Errorf("store: session has an owner: %w", ErrConflict)
	}
	for _, q := range []string{
		`DELETE FROM session_snapshots WHERE session_id = $1::uuid`,
		`DELETE FROM session_file_operations WHERE session_id = $1::uuid`,
		`DELETE FROM session_tool_executions WHERE session_id = $1::uuid`,
		`DELETE FROM session_content_grants WHERE session_id = $1::uuid`,
		`DELETE FROM session_content_owners WHERE session_id = $1::uuid`,
	} {
		if _, err := tx.Exec(ctx, q, sessionID); err != nil {
			return fmt.Errorf("store: delete session content: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) SessionHasContent(ctx context.Context, sessionID string) (bool, error) {
	if !looksLikeUUID(strings.TrimSpace(sessionID)) {
		return false, nil
	}
	var has bool
	err := s.pool.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM session_content_owners WHERE session_id = $1::uuid)
		  OR EXISTS(SELECT 1 FROM session_snapshots WHERE session_id = $1::uuid)
		  OR EXISTS(SELECT 1 FROM session_file_operations WHERE session_id = $1::uuid)
		  OR EXISTS(SELECT 1 FROM session_tool_executions WHERE session_id = $1::uuid)`,
		sessionID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("store: session content: %w", err)
	}
	return has, nil
}

func (s *PostgresStore) HasSessionContentGrant(ctx context.Context, sessionID, userID string) (bool, error) {
	if !looksLikeUUID(strings.TrimSpace(sessionID)) || !looksLikeUUID(strings.TrimSpace(userID)) {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM session_content_grants
		  WHERE session_id = $1::uuid AND grantee_user_id = $2::uuid AND revoked_at IS NULL
		)`, sessionID, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: session grant: %w", err)
	}
	return ok, nil
}

func (s *PostgresStore) GrantSessionContent(ctx context.Context, sessionID, projectID, granteeUserID, grantedBy string) error {
	if !looksLikeUUID(sessionID) || !looksLikeUUID(projectID) || !looksLikeUUID(granteeUserID) {
		return errors.New("store: session grant ids must be uuids")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO session_content_grants (session_id, project_id, grantee_user_id, granted_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		ON CONFLICT (session_id, grantee_user_id) WHERE revoked_at IS NULL DO NOTHING`,
		sessionID, projectID, granteeUserID, nullUUIDStrict(grantedBy))
	if err != nil {
		return fmt.Errorf("store: session grant: %w", err)
	}
	return nil
}

func (s *PostgresStore) AppendAudit(ctx context.Context, ev AuditEvent) error {
	if err := normalizeAudit(&ev); err != nil {
		return err
	}
	// Columns are UUID. Non-UUID ids (in-memory subjects) are not stored;
	// the hash is over the values that land in the row.
	if !looksLikeUUID(ev.TenantID) {
		ev.TenantID = ""
	}
	if !looksLikeUUID(ev.ProjectID) {
		ev.ProjectID = ""
	}
	meta, err := auditMetadataJSON(ev.Metadata)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := "platform"
	if ev.TenantID != "" {
		lockKey = ev.TenantID
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("store: audit lock: %w", err)
	}
	var prev string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(prev_hash, '')
		FROM audit_events
		WHERE tenant_id IS NOT DISTINCT FROM $1::uuid
		ORDER BY at DESC, id DESC
		LIMIT 1`, nullUUIDStrict(ev.TenantID)).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		prev = ""
		err = nil
	}
	if err != nil {
		return fmt.Errorf("store: audit prev: %w", err)
	}
	ev.PrevHash = auditChainHash(prev, ev, meta)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (
			at, tenant_id, actor_user_id, actor_kind, action, resource_kind, resource_id,
			project_id, ip, device_id, user_agent, reason, request_id, outcome, prev_hash, metadata
		) VALUES (
			$1, $2::uuid, NULLIF($3, ''), $4, $5, NULLIF($6, ''), NULLIF($7, ''),
			$8::uuid, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''),
			NULLIF($13, ''), $14, $15, $16::jsonb
		)`,
		ev.At, nullUUIDStrict(ev.TenantID), ev.ActorUserID, ev.ActorKind, ev.Action,
		ev.ResourceKind, ev.ResourceID, nullUUIDStrict(ev.ProjectID), ev.IP, ev.DeviceID,
		ev.UserAgent, ev.Reason, ev.RequestID, ev.Outcome, ev.PrevHash, meta,
	); err != nil {
		return fmt.Errorf("store: audit insert: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListAuditEvents(ctx context.Context, projectID, action string, limit int) ([]AuditEvent, error) {
	projectID = strings.TrimSpace(projectID)
	action = strings.TrimSpace(action)
	if limit <= 0 {
		limit = 50
	}
	if projectID != "" && !looksLikeUUID(projectID) {
		return []AuditEvent{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, at, COALESCE(tenant_id::text, ''), COALESCE(actor_user_id, ''),
		       actor_kind, action, COALESCE(resource_kind, ''), COALESCE(resource_id, ''),
		       COALESCE(project_id::text, ''), COALESCE(ip, ''), COALESCE(device_id, ''),
		       COALESCE(user_agent, ''), COALESCE(reason, ''), COALESCE(request_id, ''),
		       outcome, COALESCE(prev_hash, ''), metadata
		FROM audit_events
		WHERE ($1 = '' OR project_id = $1::uuid)
		  AND ($2 = '' OR action = $2)
		ORDER BY at DESC, id DESC
		LIMIT $3`, projectID, action, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var ev AuditEvent
		var meta []byte
		if err := rows.Scan(
			&ev.ID, &ev.At, &ev.TenantID, &ev.ActorUserID, &ev.ActorKind, &ev.Action,
			&ev.ResourceKind, &ev.ResourceID, &ev.ProjectID, &ev.IP, &ev.DeviceID,
			&ev.UserAgent, &ev.Reason, &ev.RequestID, &ev.Outcome, &ev.PrevHash, &meta,
		); err != nil {
			return nil, err
		}
		if len(meta) > 0 && string(meta) != "{}" {
			_ = json.Unmarshal(meta, &ev.Metadata)
		}
		out = append(out, ev)
	}
	if out == nil {
		out = []AuditEvent{}
	}
	return out, rows.Err()
}

var _ SessionContentStore = (*MemStore)(nil)
var _ SessionContentStore = (*PostgresStore)(nil)
