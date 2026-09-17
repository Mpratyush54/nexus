// Memory lifecycle transitions for issue #38.
//
// web/app.js (memoryAction) calls:
//
//	POST /memory/{id}/confirm
//	POST /memory/{id}/reject
//	POST /memory/{id}/promote
//
// None of these existed in the issue #8 route table, so the dashboard's
// Confirm/Reject/Promote buttons always got 404. This file adds exactly the
// three handlers; route registration lives in New (server.go) so routes.go
// stays untouched per the issue constraint.
//
// Validation mirrors the memory_items CHECK constraints (migrations/001 and
// routes.go allowed vocabularies) at the API boundary:
//
//	confirm: status must be PROPOSED → CONFIRMED, else 409
//	reject:  status must be PROPOSED → REJECTED, else 409
//	promote: level must be session → project, else 409
//
// Unknown id → 404 (via store.ErrNotFound, same envelope as storeError).
// Invalid state → 409 Conflict with the {"error": ...} envelope.
//
// Store seam: the narrow Store interface in server.go is frozen (no edits
// allowed by this issue), so lifecycle persistence is an OPTIONAL extension
// interface. Handlers type-assert s.store to lifecycleStore; a store that
// does not implement it yields 500. Tests wrap fakeStore (server_test.go)
// with GetMemory/UpdateMemory; the future Postgres adapter implements the
// same two methods in SQL:
//
//	confirm: UPDATE memory_items SET status='CONFIRMED' WHERE id=$1 AND status='PROPOSED'
//	reject:  UPDATE memory_items SET status='REJECTED'  WHERE id=$1 AND status='PROPOSED'
//	promote: UPDATE memory_items SET session_id=NULL, level='project'
//	         WHERE id=$1 AND level='session'
//
// NOTE on promote + session_id: server.Memory carries no session_id field,
// so the handler sets Level=project and the SQL adapter MUST also NULL
// session_id in the same row update (see sessions.go promotion rule and
// ADR-012: promotion clears SessionID and flips Level to 'project'). The
// in-memory test fake has no session column, so level flip is the whole
// observable transition there.
//
// Bodies: confirm/reject ignore any body (web/ sends {}). Promote likewise
// ignores the body — web/ sends {level: nextLevel(...)} which for a session
// item is "personal", while the server rule is fixed session→project; the
// body is accepted-but-ignored for forward compatibility with a future
// target-level promote.
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/store"
)

// ErrConflict signals a valid-id but invalid-state lifecycle transition.
// Handlers map it to 409 Conflict (never 400: the request shape is fine,
// the resource state forbids it).
var ErrConflict = errors.New("server: conflicting memory state")

// lifecycleStore is the optional persistence surface for memory lifecycle
// transitions. It is separate from Store (server.go, frozen) so this issue
// adds handlers without widening the narrow Store seam.
type lifecycleStore interface {
	GetMemory(ctx context.Context, id string) (*Memory, error)
	UpdateMemory(ctx context.Context, m Memory) (*Memory, error)
}

// lifecycleBackend asserts the optional extension; false when the wired
// Store predates issue #38 (handlers report 500, never panic).
func (s *Server) lifecycleBackend() (lifecycleStore, bool) {
	ls, ok := s.store.(lifecycleStore)
	return ls, ok
}

// lifecycleID extracts the {id} path value; "" when absent.
func lifecycleID(r *http.Request) string {
	return strings.TrimSpace(r.PathValue("id"))
}

// loadForLifecycle fetches the memory or writes the error response.
// It returns nil when a response was already written.
func (s *Server) loadForLifecycle(w http.ResponseWriter, r *http.Request) (*Memory, lifecycleStore, bool) {
	id := lifecycleID(r)
	if id == "" {
		writeError(w, http.StatusBadRequest, "memory id path parameter is required")
		return nil, nil, false
	}
	ls, ok := s.lifecycleBackend()
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	m, err := ls.GetMemory(r.Context(), id)
	if err != nil {
		storeError(w, err) // ErrNotFound → 404, rest → 500
		return nil, nil, false
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "not found")
		return nil, nil, false
	}
	return m, ls, true
}

// storeLifecycle persists the mutated memory or writes the error response.
func storeLifecycle(w http.ResponseWriter, r *http.Request, ls lifecycleStore, m *Memory) bool {
	updated, err := ls.UpdateMemory(r.Context(), *m)
	if err != nil {
		// A race that deletes the row between GET and UPDATE still reads
		// as 404; anything else is a 500.
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return false
	}
	writeJSON(w, http.StatusOK, updated)
	return true
}

// handleConfirmMemory: POST /memory/{id}/confirm — PROPOSED → CONFIRMED.
func (s *Server) handleConfirmMemory(w http.ResponseWriter, r *http.Request) {
	m, ls, ok := s.loadForLifecycle(w, r)
	if !ok {
		return
	}
	if m.Status != store.StatusProposed {
		writeError(w, http.StatusConflict,
			"cannot confirm memory with status "+m.Status+": only PROPOSED may be confirmed")
		return
	}
	m.Status = store.StatusConfirmed
	storeLifecycle(w, r, ls, m)
}

// handleRejectMemory: POST /memory/{id}/reject — PROPOSED → REJECTED.
func (s *Server) handleRejectMemory(w http.ResponseWriter, r *http.Request) {
	m, ls, ok := s.loadForLifecycle(w, r)
	if !ok {
		return
	}
	if m.Status != store.StatusProposed {
		writeError(w, http.StatusConflict,
			"cannot reject memory with status "+m.Status+": only PROPOSED may be rejected")
		return
	}
	m.Status = store.StatusRejected
	storeLifecycle(w, r, ls, m)
}

// handlePromoteMemory: POST /memory/{id}/promote — level session → project.
// Status is orthogonal (a session memory promotes whatever its lifecycle
// state); only the session scope gate is enforced here. The SQL adapter
// must NULL session_id in the same update (see package comment).
func (s *Server) handlePromoteMemory(w http.ResponseWriter, r *http.Request) {
	m, ls, ok := s.loadForLifecycle(w, r)
	if !ok {
		return
	}
	if m.Level != store.LevelSession {
		writeError(w, http.StatusConflict,
			"cannot promote memory with level "+m.Level+": only session memories may be promoted")
		return
	}
	m.Level = store.LevelProject
	storeLifecycle(w, r, ls, m)
}
