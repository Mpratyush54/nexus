package server

// Owner-only clean-up of a chat:
//
//	DELETE /v1/agent-sessions/{id}              delete the chat (turns, usage,
//	                                            versions, shares, snapshots).
//	                                            Memories made from it stay.
//	POST   /v1/agent-sessions/{id}/turns/dedupe remove undated legacy turns:
//	                                            copies of dated turns and
//	                                            harness-only user envelopes.
//	                                            ?mode=all|exact|copies|envelopes
//	                                            (default all); ?dry_run=1 counts
//	                                            only. -> {removed, kept, rules, ...}
//
// Both answer 404 when the chat does not exist and 403 to anyone but its
// owner, including people it is shared with.

import (
	"errors"
	"net/http"

	"central-memory/internal/store"
)

func (s *Server) registerSessionDeleteRoutes() {
	s.Mux.HandleFunc("DELETE /v1/agent-sessions/{id}", s.requireAuth(s.handleAgentSessionDelete))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/turns/dedupe", s.requireAuth(s.handleAgentSessionTurnDedupe))
}

// requireSessionDeleteOwner loads the chat and checks the caller owns it.
func (s *Server) requireSessionDeleteOwner(w http.ResponseWriter, r *http.Request) (*store.AgentSession, store.AgentSessionDeleter, bool) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return nil, nil, false
	}
	deleter, ok := s.Store.(store.AgentSessionDeleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "deleting chats is unavailable")
		return nil, nil, false
	}
	row, err := cs.GetAgentSession(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return nil, nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}
	if row.OwnerUserID == "" || row.OwnerUserID != authSubject(r) {
		writeError(w, http.StatusForbidden, "only the chat's owner can change its history")
		return nil, nil, false
	}
	return row, deleter, true
}

func (s *Server) handleAgentSessionDelete(w http.ResponseWriter, r *http.Request) {
	row, deleter, ok := s.requireSessionDeleteOwner(w, r)
	if !ok {
		return
	}
	res, err := deleter.DeleteAgentSession(r.Context(), row.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not delete the chat")
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.deleted", ResourceKind: "session", ResourceID: row.ID, ProjectID: row.ProjectID,
		Metadata: map[string]any{
			"harness": row.Harness, "turns": res.Turns, "versions": res.Versions,
			"grants": res.Grants, "usage": res.Usage, "snapshots": res.Snapshots,
		},
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "session_id": row.ID, "removed": res})
}

func (s *Server) handleAgentSessionTurnDedupe(w http.ResponseWriter, r *http.Request) {
	row, deleter, ok := s.requireSessionDeleteOwner(w, r)
	if !ok {
		return
	}
	dryRun := queryFlag(r.URL.Query().Get("dry_run"))
	mode, err := store.NormalizeDedupeMode(r.URL.Query().Get("mode"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := deleter.DedupeSessionTurns(r.Context(), row.ID, store.TurnDedupeOptions{Mode: mode, DryRun: dryRun})
	if err != nil {
		if errors.Is(err, store.ErrBadDedupeMode) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not remove duplicate turns")
		return
	}
	if !dryRun && res.Removed > 0 {
		s.recordAudit(r, store.AuditEvent{
			Action: "session.turns_deduped", ResourceKind: "session", ResourceID: row.ID, ProjectID: row.ProjectID,
			Metadata: map[string]any{"removed": res.Removed, "kept": res.Kept, "mode": res.Mode, "rules": res.Rules},
		})
	}
	writeJSON(w, http.StatusOK, res)
}
