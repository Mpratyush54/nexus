package server

// memory_edit.go — PUT/DELETE/history/revert for memory items (issue #162).
//
// Follows confirm/reject/promote conventions: authorizeMemory (project
// membership), decodeJSON, writeJSON/writeError. Store methods live on both
// MemStore and PostgresStore; the memoryEditStore seam keeps the core Store
// interface unchanged (same pattern as rejectMemoryStore).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

// memoryEditStore is the Phase 2 edit/history surface (issue #162).
type memoryEditStore interface {
	UpdateMemory(ctx context.Context, id string, patch store.MemoryPatch, editedBy string) (*store.MemoryItem, error)
	SoftDeleteMemory(ctx context.Context, id string) (*store.MemoryItem, error)
	ListMemoryVersions(ctx context.Context, id string) ([]*store.MemoryVersion, error)
	RevertMemory(ctx context.Context, id string, version int, editedBy string) (*store.MemoryItem, error)
}

func (s *Server) memoryEditStore() (memoryEditStore, bool) {
	es, ok := s.Store.(memoryEditStore)
	return es, ok
}

type memoryUpdateRequest struct {
	Key            *string   `json:"key"`
	Content        *string   `json:"content"`
	Tags           *[]string `json:"tags"`
	ContextSnippet *string   `json:"context_snippet"`
	Level          *string   `json:"level"`
	Scope          *string   `json:"scope"`
}

type memoryRevertRequest struct {
	Version int `json:"version"`
}

func (s *Server) registerMemoryEditRoutes() {
	s.Mux.HandleFunc("PUT /memory/{id}", s.requireAuth(s.handleMemoryUpdate))
	s.Mux.HandleFunc("DELETE /memory/{id}", s.requireAuth(s.handleMemoryDelete))
	s.Mux.HandleFunc("GET /memory/{id}/history", s.requireAuth(s.handleMemoryHistory))
	s.Mux.HandleFunc("POST /memory/{id}/revert", s.requireAuth(s.handleMemoryRevert))
}

func (s *Server) handleMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemoryPermission(w, r, id, store.PermMemoryEdit) {
		return
	}
	es, ok := s.memoryEditStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory editing not supported by configured store")
		return
	}
	var req memoryUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	patch := store.MemoryPatch{
		Key:            req.Key,
		Content:        req.Content,
		Tags:           req.Tags,
		ContextSnippet: req.ContextSnippet,
		Level:          req.Level,
		Scope:          req.Scope,
	}
	if patch.Empty() {
		writeError(w, http.StatusBadRequest, "at least one field is required (key, content, tags, context_snippet, level, scope)")
		return
	}
	item, err := es.UpdateMemory(r.Context(), id, patch, authSubject(r))
	if err != nil {
		writeMemoryEditError(w, err, "could not update memory")
		return
	}
	s.publishMemoryLifecycle(item, "MEMORY_UPDATED", "updated")
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemoryPermission(w, r, id, store.PermMemoryDelete) {
		return
	}
	es, ok := s.memoryEditStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory editing not supported by configured store")
		return
	}
	item, err := es.SoftDeleteMemory(r.Context(), id)
	if err != nil {
		writeMemoryEditError(w, err, "could not delete memory")
		return
	}
	s.publishMemoryLifecycle(item, "MEMORY_SUPERSEDED", "superseded")
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleMemoryHistory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemory(w, r, id) {
		return
	}
	es, ok := s.memoryEditStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory editing not supported by configured store")
		return
	}
	versions, err := es.ListMemoryVersions(r.Context(), id)
	if err != nil {
		writeMemoryEditError(w, err, "could not list memory history")
		return
	}
	if versions == nil {
		versions = []*store.MemoryVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": versions, "count": len(versions)})
}

func (s *Server) handleMemoryRevert(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return
	}
	if !s.authorizeMemoryPermission(w, r, id, store.PermMemoryEdit) {
		return
	}
	es, ok := s.memoryEditStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "memory editing not supported by configured store")
		return
	}
	var req memoryRevertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Version < 1 {
		writeError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}
	item, err := es.RevertMemory(r.Context(), id, req.Version, authSubject(r))
	if err != nil {
		writeMemoryEditError(w, err, "could not revert memory")
		return
	}
	s.publishMemoryLifecycle(item, "MEMORY_UPDATED", "updated")
	writeJSON(w, http.StatusOK, item)
}

func writeMemoryEditError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "required") ||
		strings.Contains(msg, "invalid") ||
		strings.Contains(msg, "outside 20-2000") ||
		strings.Contains(msg, "patch is empty") ||
		strings.Contains(msg, "version must") {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	writeError(w, http.StatusInternalServerError, fallback+": "+msg)
}

// publishMemoryLifecycle appends a durable event and fans out a memory_update
// frame when a hub is attached (issue #162 / plan §5 real-time sync).
func (s *Server) publishMemoryLifecycle(item *store.MemoryItem, eventType, action string) {
	if item == nil || strings.TrimSpace(item.ProjectID) == "" {
		return
	}
	payload := memoryItemPayload(item)
	_ = s.Store.AppendEvent(context.Background(), &store.Event{
		ProjectID: item.ProjectID,
		SessionID: item.SessionID,
		UserID:    item.UserID,
		EventType: eventType,
		Payload:   payload,
	})
	s.steerMu.RLock()
	h := s.hub
	s.steerMu.RUnlock()
	if h != nil {
		func() {
			defer func() { _ = recover() }()
			h.PublishMemoryUpdate(item.ProjectID, item.SessionID, item, action)
		}()
	}
}

func memoryItemPayload(item *store.MemoryItem) map[string]any {
	raw, err := json.Marshal(item)
	if err != nil {
		return map[string]any{"id": item.ID, "key": item.Key, "status": item.Status}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"id": item.ID, "key": item.Key, "status": item.Status}
	}
	return out
}
