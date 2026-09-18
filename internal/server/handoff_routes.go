package server

// handoff_routes.go — session handoff REST + WS hub fan-out (issue #82,
// server side). CLI seam is cmd/nexus (out of scope here); this file owns
// hub events + routes so handoffs are visible live and persistable.
//
//   - POST /sessions/{id}/handoff builds a HandoffPackage + emits
//     SESSION_HANDOFF_INITIATED via the hub and event store.
//   - POST /sessions/{id}/handoff/accept accepts a package + emits
//     SESSION_HANDOFF_ACCEPTED.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/handoff"
	"central-memory/internal/store"
)

// handoffRecord is one in-flight handoff.
type handoffRecord struct {
	pkg       *handoff.HandoffPackage
	createdAt time.Time
}

type handoffInitRequest struct {
	ToUser        string                  `json:"to_user"`
	ToAgent       string                  `json:"to_agent"`
	FromAgent     string                  `json:"from_agent"`
	Task          handoff.TaskStatus      `json:"task"`
	Memories      []handoff.MemorySnippet `json:"memories"`
	ModifiedFiles []string                `json:"files"`
	Branch        handoff.BranchState     `json:"branch"`
	Note          string                  `json:"note"`
}

type handoffAcceptRequest struct {
	HandoffID string `json:"handoff_id"`
}

func (s *Server) registerHandoffRoutes() {
	s.Mux.HandleFunc("POST /sessions/{id}/handoff", s.requireAuth(s.handleHandoffInit))
	s.Mux.HandleFunc("POST /sessions/{id}/handoff/accept", s.requireAuth(s.handleHandoffAccept))
}

func (s *Server) handleHandoffInit(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	var req handoffInitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ToUser) == "" {
		writeError(w, http.StatusBadRequest, "to_user is required")
		return
	}
	fromUser := authSubject(r)
	projectID := ""
	sessionID := id
	if ss, ok := s.sessionStore(); ok {
		sess, err := ss.GetSession(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		projectID = sess.ProjectID
	} else {
		writeError(w, http.StatusNotImplemented, "sessions are not supported by this store")
		return
	}
	pkg, ev := handoff.BuildHandoffFull(projectID, sessionID, fromUser, req.ToUser,
		req.FromAgent, req.ToAgent, req.Task, req.Memories, req.ModifiedFiles, req.Branch, req.Note)
	s.handoffMu.Lock()
	if s.handoffs == nil {
		s.handoffs = make(map[string]*handoffRecord)
	}
	s.handoffs[pkg.ID] = &handoffRecord{pkg: pkg, createdAt: time.Now().UTC()}
	s.handoffMu.Unlock()

	s.publishHandoffEvent(projectID, sessionID, ev)
	writeJSON(w, http.StatusCreated, pkg)
}

func (s *Server) handleHandoffAccept(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	var req handoffAcceptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.HandoffID) == "" {
		writeError(w, http.StatusBadRequest, "handoff_id is required")
		return
	}
	s.handoffMu.Lock()
	rec, ok := s.handoffs[req.HandoffID]
	s.handoffMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "handoff not found")
		return
	}
	byUser := authSubject(r)
	updated, ev, err := handoff.AcceptHandoff(rec.pkg, byUser)
	if err != nil {
		if errors.Is(err, handoff.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.publishHandoffEvent(updated.ProjectID, updated.SessionID, ev)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) publishHandoffEvent(projectID, sessionID string, ev handoff.Event) {
	payload := map[string]any{
		"handoff_id": ev.HandoffID,
		"from_user":  ev.FromUser,
		"to_user":    ev.ToUser,
	}
	for k, v := range ev.Payload {
		payload[k] = v
	}
	// Persist best-effort so the event log carries the transfer.
	_ = s.Store.AppendEvent(context.Background(), &store.Event{
		ProjectID: projectID, SessionID: sessionID,
		EventType: ev.Type, Payload: payload,
	})
	// Fan out live via whichever hub is attached.
	s.steerMu.RLock()
	h := s.hub
	s.steerMu.RUnlock()
	if h != nil {
		func() {
			defer func() { _ = recover() }()
			h.PublishEvent(projectID, sessionID, ev.Type, payload, ev.FromUser)
		}()
	}
}
