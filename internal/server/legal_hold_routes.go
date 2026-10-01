package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"central-memory/internal/store"
)

var legalHoldRoutes sync.Map

func (s *Server) legalHoldStore() (store.LegalHoldStore, bool) {
	hs, ok := s.Store.(store.LegalHoldStore)
	return hs, ok
}

func (s *Server) registerLegalHoldRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := legalHoldRoutes.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("POST /orgs/{id}/legal-hold/export", s.requireAuth(s.handleLegalHoldExportRequest))
	s.Mux.HandleFunc("POST /orgs/{id}/legal-hold/{request}/approve", s.requireAuth(s.handleLegalHoldApprove))
}

func (s *Server) handleLegalHoldExportRequest(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	hs, ok := s.legalHoldStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "legal hold unavailable")
		return
	}
	orgID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeOrgOwner(w, r, os, orgID) {
		return
	}
	var body struct {
		SessionIDs   []string `json:"session_ids"`
		ApproverIDs  []string `json:"approver_ids"`
		CustodianIDs []string `json:"custodian_ids"`
		Reason       string   `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// approver_ids is optional on create; the second owner confirms via /approve.
	// If provided, require it to name a distinct owner other than the requester.
	requester := authSubject(r)
	for _, id := range body.ApproverIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if id == requester {
			writeError(w, http.StatusBadRequest, "approver_ids must name a distinct second owner")
			return
		}
		role, err := os.GetOrgMemberRole(r.Context(), orgID, id)
		if err != nil || role != store.OrgRoleOwner {
			writeError(w, http.StatusBadRequest, "approver_ids must name an org owner")
			return
		}
	}
	req, err := hs.CreateLegalHoldRequest(r.Context(), orgID, requester, body.SessionIDs, body.CustodianIDs, body.Reason)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "organization or session not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action:       "legal_hold.requested",
		ResourceKind: "legal_hold",
		ResourceID:   req.ID,
		TenantID:     orgID,
		ActorKind:    "admin",
		Metadata: map[string]any{
			"session_count":   len(req.SessionIDs),
			"custodian_count": len(req.CustodianIDs),
		},
	})
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleLegalHoldApprove(w http.ResponseWriter, r *http.Request) {
	os, ok := s.requireOrgStore(w)
	if !ok {
		return
	}
	hs, ok := s.legalHoldStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "legal hold unavailable")
		return
	}
	orgID := strings.TrimSpace(r.PathValue("id"))
	requestID := strings.TrimSpace(r.PathValue("request"))
	if !s.authorizeOrgOwner(w, r, os, orgID) {
		return
	}
	approver := authSubject(r)
	pending, err := hs.GetLegalHoldRequest(r.Context(), orgID, requestID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "legal hold request not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pending.RequestedBy == approver {
		writeError(w, http.StatusForbidden, "a second distinct owner must approve")
		return
	}
	req, receipt, err := hs.ApproveLegalHoldRequest(r.Context(), orgID, requestID, approver)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "legal hold request not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "legal hold already processed")
			return
		}
		if strings.Contains(err.Error(), "second distinct owner") {
			writeError(w, http.StatusForbidden, "a second distinct owner must approve")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action:       "legal_hold.approved",
		ResourceKind: "legal_hold",
		ResourceID:   req.ID,
		TenantID:     orgID,
		ActorKind:    "admin",
		Metadata: map[string]any{
			"requested_by": req.RequestedBy,
		},
	})
	s.recordAudit(r, store.AuditEvent{
		Action:       "legal_hold.exported",
		ResourceKind: "legal_hold",
		ResourceID:   req.ID,
		TenantID:     orgID,
		ActorKind:    "admin",
		Metadata: map[string]any{
			"session_count": receipt.SessionCount,
			"requested_by":  req.RequestedBy,
			"approved_by":   req.ApprovedBy,
		},
	})
	s.notifyLegalHoldCustodians(r, orgID, req, receipt)

	if strings.EqualFold(r.URL.Query().Get("format"), "zip") {
		s.writeLegalHoldZip(w, receipt)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request": req,
		"receipt": receipt,
	})
}

func (s *Server) notifyLegalHoldCustodians(r *http.Request, orgID string, req *store.LegalHoldRequest, receipt *store.LegalHoldReceipt) {
	if s.Mail == nil || !s.Mail.Configured() || req == nil {
		return
	}
	ids := req.CustodianIDs
	if len(ids) == 0 {
		return
	}
	body := fmt.Sprintf(
		"A legal-hold export was completed for your organization.\n\nRequest: %s\nSessions: %d\nOrg: %s\n\nThis notice does not include session content.\n",
		req.ID, receipt.SessionCount, orgID,
	)
	for _, uid := range ids {
		email := s.lookupUserEmail(r, uid)
		if email == "" {
			continue
		}
		if err := s.Mail.Send(email, "Nexus legal-hold export notice", body); err != nil {
			s.Log.Printf("legal-hold mail: %v", err)
		}
	}
}

func (s *Server) lookupUserEmail(r *http.Request, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" || s.Accounts == nil {
		return ""
	}
	u, err := s.Accounts.GetByID(r.Context(), userID)
	if err != nil || u == nil {
		return ""
	}
	return strings.TrimSpace(u.Email)
}

func (s *Server) writeLegalHoldZip(w http.ResponseWriter, receipt *store.LegalHoldReceipt) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	meta, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encode receipt")
		return
	}
	fw, err := zw.Create("receipt.json")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build zip")
		return
	}
	if _, err := fw.Write(meta); err != nil {
		writeError(w, http.StatusInternalServerError, "could not write zip")
		return
	}
	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not close zip")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="legal-hold-receipt.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
