package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"central-memory/internal/memoryact"
	"central-memory/internal/store"
)

// memoryActionRoutes marks muxes that already have pin, scope, forget,
// auto-promote, and owner-removal. The legacy POST /memory/{id}/promote
// handler stays on the same mux.
var memoryActionRoutes sync.Map

// registerMemoryActionRoutes wires pin, scope, forget, auto-promote, and
// owner removal (product spec 2.2 and 2.4). Legacy POST /memory/{id}/promote
// is unchanged.
func (s *Server) registerMemoryActionRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := memoryActionRoutes.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("POST /memory/{id}/pin", s.requireAuth(s.handleMemoryPin))
	s.Mux.HandleFunc("POST /memory/{id}/scope", s.requireAuth(s.handleMemoryScope))
	s.Mux.HandleFunc("POST /memory/{id}/forget", s.requireAuth(s.handleMemoryForget))
	s.Mux.HandleFunc("POST /memory/{id}/auto-promote", s.requireAuth(s.handleMemoryActionPromote))
	s.Mux.HandleFunc("POST /memory/{id}/remove-promoted", s.requireAuth(s.handleMemoryRemovePromoted))
}

func (s *Server) memoryActions() (store.MemoryActionStore, bool) {
	as, ok := s.Store.(store.MemoryActionStore)
	return as, ok
}

type memoryScopeRequest struct {
	Level string `json:"level"`
}

func (s *Server) handleMemoryPin(w http.ResponseWriter, r *http.Request) {
	id, ok := s.memoryActionID(w, r)
	if !ok {
		return
	}
	if _, ok := s.authorizeMemoryAction(w, r, id); !ok {
		return
	}
	as, ok := s.memoryActions()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory actions not supported by configured store")
		return
	}
	item, err := as.PinMemory(r.Context(), id)
	if err != nil {
		writeMemoryActionError(w, err, "could not pin memory")
		return
	}
	writePublicMemory(w, http.StatusOK, item, map[string]any{"pinned": true})
}

func (s *Server) handleMemoryScope(w http.ResponseWriter, r *http.Request) {
	id, ok := s.memoryActionID(w, r)
	if !ok {
		return
	}
	if _, ok := s.authorizeMemoryAction(w, r, id); !ok {
		return
	}
	var req memoryScopeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Level) == "" {
		writeError(w, http.StatusBadRequest, "level is required")
		return
	}
	as, ok := s.memoryActions()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory actions not supported by configured store")
		return
	}
	item, err := as.SetMemoryScope(r.Context(), id, req.Level)
	if err != nil {
		writeMemoryActionError(w, err, "could not change memory scope")
		return
	}
	writePublicMemory(w, http.StatusOK, item, nil)
}

func (s *Server) handleMemoryForget(w http.ResponseWriter, r *http.Request) {
	id, ok := s.memoryActionID(w, r)
	if !ok {
		return
	}
	if _, ok := s.authorizeMemoryAction(w, r, id); !ok {
		return
	}
	as, ok := s.memoryActions()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory actions not supported by configured store")
		return
	}
	item, err := as.ForgetMemory(r.Context(), id)
	if err != nil {
		writeMemoryActionError(w, err, "could not forget memory")
		return
	}
	writePublicMemory(w, http.StatusOK, item, nil)
}

func (s *Server) handleMemoryActionPromote(w http.ResponseWriter, r *http.Request) {
	id, ok := s.memoryActionID(w, r)
	if !ok {
		return
	}
	if _, ok := s.authorizeMemoryAction(w, r, id); !ok {
		return
	}
	as, ok := s.memoryActions()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory actions not supported by configured store")
		return
	}
	out, err := as.PromoteMemory(r.Context(), id)
	if err != nil {
		writeMemoryActionError(w, err, "could not promote memory")
		return
	}
	extra := map[string]any{"held": out.Held}
	if out.Promotion.PromotedFromPrivate && !out.Held {
		extra["provenance"] = provenanceForCaller(authSubject(r), out.Item, out.Promotion)
		s.recordMemoryAudit(r, "memory.promoted", out.Item)
	}
	writePublicMemory(w, http.StatusOK, out.Item, extra)
}

func (s *Server) handleMemoryRemovePromoted(w http.ResponseWriter, r *http.Request) {
	id, ok := s.memoryActionID(w, r)
	if !ok {
		return
	}
	item, ok := s.authorizeMemoryAction(w, r, id)
	if !ok {
		return
	}
	subject := authSubject(r)
	if item.UserID == "" || item.UserID != subject {
		writeError(w, http.StatusForbidden, "only the source owner may remove this memory")
		return
	}
	as, ok := s.memoryActions()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory actions not supported by configured store")
		return
	}
	updated, err := as.RemovePromotedMemory(r.Context(), id, subject)
	if err != nil {
		writeMemoryActionError(w, err, "could not remove promoted memory")
		return
	}
	// Removal payload records removed_by_owner and never a session title.
	// Session id stays in the owner provenance store, not in this body.
	pub := *updated
	pub.SessionID = ""
	pub.ContextSnippet = ""
	s.recordMemoryAudit(r, "memory.removed_by_owner", updated)
	writePublicMemory(w, http.StatusOK, &pub, map[string]any{"removed_by_owner": true})
}

func (s *Server) memoryActionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return "", false
	}
	return id, true
}

// authorizeMemoryAction requires project membership when the row has a project.
func (s *Server) authorizeMemoryAction(w http.ResponseWriter, r *http.Request, id string) (*store.MemoryItem, bool) {
	item, err := s.Store.GetMemoryItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "memory not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "could not load memory")
		return nil, false
	}
	subject := authSubject(r)
	projectID := strings.TrimSpace(item.ProjectID)
	if projectID != "" {
		ok, err := s.Store.IsProjectMember(r.Context(), subject, projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not check project membership")
			return nil, false
		}
		if !ok {
			writeError(w, http.StatusForbidden, "not a member of this project")
			return nil, false
		}
		return item, true
	}
	if item.UserID != "" && item.UserID != subject {
		writeError(w, http.StatusForbidden, "not authorized to access this memory")
		return nil, false
	}
	return item, true
}

func provenanceForCaller(subject string, item *store.MemoryItem, p store.Promotion) memoryact.ProvenanceView {
	owner := ""
	if item != nil {
		owner = item.UserID
	}
	if subject != "" && (subject == owner || subject == p.SourceOwnerID) {
		return p.ForOwner()
	}
	return p.ForTeammate()
}

func writePublicMemory(w http.ResponseWriter, status int, item *store.MemoryItem, extra map[string]any) {
	if item == nil {
		writeError(w, http.StatusInternalServerError, "memory action returned no item")
		return
	}
	tags := item.Tags
	if tags == nil {
		tags = []string{}
	}
	payload := map[string]any{
		"id":         item.ID,
		"project_id": item.ProjectID,
		"user_id":    item.UserID,
		"key":        item.Key,
		"content":    item.Content,
		"level":      item.Level,
		"scope":      item.Scope,
		"tags":       tags,
		"confidence": item.Confidence,
		"status":     memoryact.PublicStatus(item.Status),
		"visibility": item.Visibility,
	}
	if item.SessionID != "" {
		payload["session_id"] = item.SessionID
	}
	if memoryPinned(item) {
		payload["pinned"] = true
	}
	for k, v := range extra {
		if k == "title" || k == "session_title" {
			continue
		}
		payload[k] = v
	}
	delete(payload, "title")
	delete(payload, "session_title")
	writeJSON(w, status, payload)
}

func memoryPinned(item *store.MemoryItem) bool {
	if item == nil {
		return false
	}
	if store.MemoryPinned(item.ID) {
		return true
	}
	for _, tag := range item.Tags {
		if tag == "pinned" {
			return true
		}
	}
	return false
}

func writeMemoryActionError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	if errors.Is(err, store.ErrMemoryActionForbidden) {
		writeError(w, http.StatusForbidden, "only the source owner may remove this memory")
		return
	}
	if errors.Is(err, store.ErrInvalidMemoryAction) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, fallback)
}

type memoryAuditAppender interface {
	AppendAudit(ctx context.Context, ev store.AuditEvent) error
}

func (s *Server) recordMemoryAudit(r *http.Request, action string, item *store.MemoryItem) {
	appender, ok := s.Store.(memoryAuditAppender)
	if !ok || item == nil {
		return
	}
	meta := map[string]any{
		"memory_id": item.ID,
	}
	if action == "memory.removed_by_owner" {
		meta["removed_by_owner"] = true
	}
	if action == "memory.promoted" {
		meta["promoted_from_private"] = true
	}
	// Audit rows must not carry the session title or the session id.
	err := appender.AppendAudit(r.Context(), store.AuditEvent{
		ActorUserID:  authSubject(r),
		ActorKind:    "user",
		Action:       action,
		ResourceKind: "memory",
		ResourceID:   item.ID,
		ProjectID:    item.ProjectID,
		Outcome:      "ok",
		Metadata:     meta,
	})
	if err != nil && s.Log != nil {
		s.Log.Printf("memory audit %s: %v", action, err)
	}
}
