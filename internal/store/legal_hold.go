package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// LegalHoldRequest is a two-Owner legal-hold export request (D20 / P4).
// It never carries session titles, summaries, turns, or secret plaintext.
type LegalHoldRequest struct {
	ID           string     `json:"id"`
	OrgID        string     `json:"org_id"`
	RequestedBy  string     `json:"requested_by"`
	SessionIDs   []string   `json:"session_ids"`
	CustodianIDs []string   `json:"custodian_ids,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	Status       string     `json:"status"`
	ApprovedBy   string     `json:"approved_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ApprovedAt   *time.Time `json:"approved_at,omitempty"`
	ExportedAt   *time.Time `json:"exported_at,omitempty"`
}

// LegalHoldSessionMeta is content-free export metadata for one session.
type LegalHoldSessionMeta struct {
	SessionID      string `json:"session_id"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
}

// LegalHoldReceipt is the content-free export artifact (ids + hashes only).
type LegalHoldReceipt struct {
	RequestID    string                 `json:"request_id"`
	OrgID        string                 `json:"org_id"`
	ExportedAt   time.Time              `json:"exported_at"`
	RequestedBy  string                 `json:"requested_by"`
	ApprovedBy   string                 `json:"approved_by"`
	SessionCount int                    `json:"session_count"`
	Sessions     []LegalHoldSessionMeta `json:"sessions"`
}

// LegalHoldStore is the audited two-Owner legal-hold export path.
type LegalHoldStore interface {
	CreateLegalHoldRequest(ctx context.Context, orgID, requestedBy string, sessionIDs, custodianIDs []string, reason string) (*LegalHoldRequest, error)
	GetLegalHoldRequest(ctx context.Context, orgID, requestID string) (*LegalHoldRequest, error)
	ApproveLegalHoldRequest(ctx context.Context, orgID, requestID, approverID string) (*LegalHoldRequest, *LegalHoldReceipt, error)
}

type legalHoldBook struct {
	mu   sync.Mutex
	byID map[string]*LegalHoldRequest
}

var legalHoldBooks sync.Map

func holdBook(key any) *legalHoldBook {
	if v, ok := legalHoldBooks.Load(key); ok {
		return v.(*legalHoldBook)
	}
	book := &legalHoldBook{byID: map[string]*LegalHoldRequest{}}
	actual, _ := legalHoldBooks.LoadOrStore(key, book)
	return actual.(*legalHoldBook)
}

func cloneLegalHold(r *LegalHoldRequest) *LegalHoldRequest {
	if r == nil {
		return nil
	}
	cp := *r
	cp.SessionIDs = append([]string(nil), r.SessionIDs...)
	cp.CustodianIDs = append([]string(nil), r.CustodianIDs...)
	if r.ApprovedAt != nil {
		t := *r.ApprovedAt
		cp.ApprovedAt = &t
	}
	if r.ExportedAt != nil {
		t := *r.ExportedAt
		cp.ExportedAt = &t
	}
	return &cp
}

func normalizeIDList(ids []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (m *MemStore) CreateLegalHoldRequest(ctx context.Context, orgID, requestedBy string, sessionIDs, custodianIDs []string, reason string) (*LegalHoldRequest, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	requestedBy = strings.TrimSpace(requestedBy)
	sessionIDs = normalizeIDList(sessionIDs)
	custodianIDs = normalizeIDList(custodianIDs)
	reason = strings.TrimSpace(reason)
	if orgID == "" || requestedBy == "" {
		return nil, errors.New("store: legal hold requires org and requester")
	}
	if len(sessionIDs) == 0 {
		return nil, errors.New("store: legal hold requires session_ids")
	}
	m.mu.RLock()
	if _, ok := m.orgs[orgID]; !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	inOrg := map[string]bool{}
	for id, p := range m.projects {
		if p != nil && p.OrgID == orgID {
			inOrg[id] = true
		}
	}
	for _, sid := range sessionIDs {
		sess := m.agentSessions[sid]
		if sess == nil || !inOrg[sess.ProjectID] {
			m.mu.RUnlock()
			return nil, fmt.Errorf("store: session %s: %w", sid, ErrNotFound)
		}
	}
	m.mu.RUnlock()

	row := &LegalHoldRequest{
		ID:           newID("lhold"),
		OrgID:        orgID,
		RequestedBy:  requestedBy,
		SessionIDs:   sessionIDs,
		CustodianIDs: custodianIDs,
		Reason:       reason,
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}
	book := holdBook(m)
	book.mu.Lock()
	defer book.mu.Unlock()
	book.byID[row.ID] = row
	return cloneLegalHold(row), nil
}

func (m *MemStore) GetLegalHoldRequest(ctx context.Context, orgID, requestID string) (*LegalHoldRequest, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	requestID = strings.TrimSpace(requestID)
	book := holdBook(m)
	book.mu.Lock()
	defer book.mu.Unlock()
	row := book.byID[requestID]
	if row == nil || row.OrgID != orgID {
		return nil, fmt.Errorf("store: legal hold %s: %w", requestID, ErrNotFound)
	}
	return cloneLegalHold(row), nil
}

func (m *MemStore) sessionExportMeta(sessionID string) LegalHoldSessionMeta {
	meta := LegalHoldSessionMeta{SessionID: sessionID}
	versions := m.sessionVersions[sessionID]
	var best *SessionVersion
	for i := range versions {
		v := &versions[i]
		if v.State != "complete" {
			continue
		}
		if best == nil || v.Version > best.Version {
			best = v
		}
	}
	if best != nil {
		meta.ManifestSHA256 = best.ManifestSHA
	}
	// Content-free integrity hash over session id + latest manifest sha (never turns/plaintext).
	sum := sha256.Sum256([]byte(sessionID + "\n" + meta.ManifestSHA256))
	meta.ContentHash = hex.EncodeToString(sum[:])
	return meta
}

func (m *MemStore) ApproveLegalHoldRequest(ctx context.Context, orgID, requestID, approverID string) (*LegalHoldRequest, *LegalHoldReceipt, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	requestID = strings.TrimSpace(requestID)
	approverID = strings.TrimSpace(approverID)
	if orgID == "" || requestID == "" || approverID == "" {
		return nil, nil, errors.New("store: legal hold approve requires org, request, and approver")
	}
	book := holdBook(m)
	book.mu.Lock()
	row := book.byID[requestID]
	if row == nil || row.OrgID != orgID {
		book.mu.Unlock()
		return nil, nil, fmt.Errorf("store: legal hold %s: %w", requestID, ErrNotFound)
	}
	if row.Status != "pending" {
		book.mu.Unlock()
		return nil, nil, fmt.Errorf("store: legal hold already %s: %w", row.Status, ErrConflict)
	}
	if row.RequestedBy == approverID {
		book.mu.Unlock()
		return nil, nil, errors.New("store: legal hold requires a second distinct owner")
	}
	now := time.Now().UTC()
	row.Status = "exported"
	row.ApprovedBy = approverID
	row.ApprovedAt = &now
	row.ExportedAt = &now
	sessionIDs := append([]string(nil), row.SessionIDs...)
	requestedBy := row.RequestedBy
	book.mu.Unlock()

	m.mu.RLock()
	sessions := make([]LegalHoldSessionMeta, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		sessions = append(sessions, m.sessionExportMeta(sid))
	}
	m.mu.RUnlock()

	receipt := &LegalHoldReceipt{
		RequestID:    requestID,
		OrgID:        orgID,
		ExportedAt:   now,
		RequestedBy:  requestedBy,
		ApprovedBy:   approverID,
		SessionCount: len(sessions),
		Sessions:     sessions,
	}
	book.mu.Lock()
	defer book.mu.Unlock()
	return cloneLegalHold(book.byID[requestID]), receipt, nil
}

func (s *PostgresStore) CreateLegalHoldRequest(ctx context.Context, orgID, requestedBy string, sessionIDs, custodianIDs []string, reason string) (*LegalHoldRequest, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	requestedBy = strings.TrimSpace(requestedBy)
	sessionIDs = normalizeIDList(sessionIDs)
	custodianIDs = normalizeIDList(custodianIDs)
	reason = strings.TrimSpace(reason)
	if orgID == "" || requestedBy == "" {
		return nil, errors.New("store: legal hold requires org and requester")
	}
	if len(sessionIDs) == 0 {
		return nil, errors.New("store: legal hold requires session_ids")
	}
	if !looksLikeUUID(orgID) {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id = $1::uuid)`, orgID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("store: organization %s: %w", orgID, ErrNotFound)
	}
	for _, sid := range sessionIDs {
		if !looksLikeUUID(sid) {
			return nil, fmt.Errorf("store: session %s: %w", sid, ErrNotFound)
		}
		var n int
		err := s.pool.QueryRow(ctx, `
			SELECT COUNT(*)::int FROM agent_sessions sess
			JOIN projects p ON p.id = sess.project_id
			WHERE sess.id = $1::uuid AND p.org_id = $2::uuid`, sid, orgID).Scan(&n)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("store: session %s: %w", sid, ErrNotFound)
		}
	}
	row := &LegalHoldRequest{
		ID:           newID("lhold"),
		OrgID:        orgID,
		RequestedBy:  requestedBy,
		SessionIDs:   sessionIDs,
		CustodianIDs: custodianIDs,
		Reason:       reason,
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
	}
	book := holdBook(s)
	book.mu.Lock()
	defer book.mu.Unlock()
	book.byID[row.ID] = row
	return cloneLegalHold(row), nil
}

func (s *PostgresStore) GetLegalHoldRequest(ctx context.Context, orgID, requestID string) (*LegalHoldRequest, error) {
	_ = ctx
	orgID = strings.TrimSpace(orgID)
	requestID = strings.TrimSpace(requestID)
	book := holdBook(s)
	book.mu.Lock()
	defer book.mu.Unlock()
	row := book.byID[requestID]
	if row == nil || row.OrgID != orgID {
		return nil, fmt.Errorf("store: legal hold %s: %w", requestID, ErrNotFound)
	}
	return cloneLegalHold(row), nil
}

func (s *PostgresStore) pgSessionExportMeta(ctx context.Context, sessionID string) (LegalHoldSessionMeta, error) {
	meta := LegalHoldSessionMeta{SessionID: sessionID}
	var sha string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(manifest_sha256,'') FROM session_versions
		WHERE session_id = $1::uuid AND state = 'complete'
		ORDER BY version DESC LIMIT 1`, sessionID).Scan(&sha)
	if err == nil {
		meta.ManifestSHA256 = sha
	}
	sum := sha256.Sum256([]byte(sessionID + "\n" + meta.ManifestSHA256))
	meta.ContentHash = hex.EncodeToString(sum[:])
	return meta, nil
}

func (s *PostgresStore) ApproveLegalHoldRequest(ctx context.Context, orgID, requestID, approverID string) (*LegalHoldRequest, *LegalHoldReceipt, error) {
	orgID = strings.TrimSpace(orgID)
	requestID = strings.TrimSpace(requestID)
	approverID = strings.TrimSpace(approverID)
	if orgID == "" || requestID == "" || approverID == "" {
		return nil, nil, errors.New("store: legal hold approve requires org, request, and approver")
	}
	book := holdBook(s)
	book.mu.Lock()
	row := book.byID[requestID]
	if row == nil || row.OrgID != orgID {
		book.mu.Unlock()
		return nil, nil, fmt.Errorf("store: legal hold %s: %w", requestID, ErrNotFound)
	}
	if row.Status != "pending" {
		book.mu.Unlock()
		return nil, nil, fmt.Errorf("store: legal hold already %s: %w", row.Status, ErrConflict)
	}
	if row.RequestedBy == approverID {
		book.mu.Unlock()
		return nil, nil, errors.New("store: legal hold requires a second distinct owner")
	}
	now := time.Now().UTC()
	row.Status = "exported"
	row.ApprovedBy = approverID
	row.ApprovedAt = &now
	row.ExportedAt = &now
	sessionIDs := append([]string(nil), row.SessionIDs...)
	requestedBy := row.RequestedBy
	book.mu.Unlock()

	sessions := make([]LegalHoldSessionMeta, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		meta, _ := s.pgSessionExportMeta(ctx, sid)
		sessions = append(sessions, meta)
	}
	receipt := &LegalHoldReceipt{
		RequestID:    requestID,
		OrgID:        orgID,
		ExportedAt:   now,
		RequestedBy:  requestedBy,
		ApprovedBy:   approverID,
		SessionCount: len(sessions),
		Sessions:     sessions,
	}
	book.mu.Lock()
	defer book.mu.Unlock()
	return cloneLegalHold(book.byID[requestID]), receipt, nil
}

var (
	_ LegalHoldStore = (*MemStore)(nil)
	_ LegalHoldStore = (*PostgresStore)(nil)
)
