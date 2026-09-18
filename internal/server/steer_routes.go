package server

// steer_routes.go — SteerEmitter/InterruptManager wiring to routes/WS
// (issue #42).
//
//   - Server.AttachSteering wires a steering manager + hub for fan-out.
//   - POST /sessions/{id}/steer/interrupt|ack|prompt|resume map onto the
//     InterruptManager state machine and emit AGENT_* events via SteerEmitter.
//   - WS actions carrying steering event types are routed through the same
//     manager so live steering works over the socket.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"central-memory/internal/steering"
)

// steerManager is the narrow steering surface the server needs.
type steerManager interface {
	Register(runID string, bridge steering.DaemonBridge) error
	RequestInterrupt(runID, steererID, reason string) error
	AcknowledgePaused(runID string) error
	Steer(runID, steererID, prompt string) error
	Resume(runID, steererID string) error
}

// AttachSteering wires the steering manager and hub used by steer routes
// and WS steering actions. It also installs the hub's steer hook so WS
// actions carrying steering event types drive the same manager (issue #42)
// instead of broadcasting raw.
func (s *Server) AttachSteering(m steerManager, h *Hub) {
	s.steerMu.Lock()
	s.steer = m
	s.hub = h
	s.steerMu.Unlock()
	if h != nil {
		h.SetSteerHook(s.routeSteerAction)
	}
}

func (s *Server) getSteer() (steerManager, *Hub) {
	s.steerMu.RLock()
	defer s.steerMu.RUnlock()
	return s.steer, s.hub
}

func (s *Server) registerSteerRoutes() {
	s.Mux.HandleFunc("POST /sessions/{id}/steer/interrupt", s.requireAuth(s.handleSteerInterrupt))
	s.Mux.HandleFunc("POST /sessions/{id}/steer/ack", s.requireAuth(s.handleSteerAck))
	s.Mux.HandleFunc("POST /sessions/{id}/steer/prompt", s.requireAuth(s.handleSteerPrompt))
	s.Mux.HandleFunc("POST /sessions/{id}/steer/resume", s.requireAuth(s.handleSteerResume))
}

type steerInterruptRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) steerEmitterFor(projectID string) *SteerEmitter {
	_, hub := s.getSteer()
	return NewSteerEmitter(s.Store, hub, projectID)
}

func (s *Server) handleSteerInterrupt(w http.ResponseWriter, r *http.Request) {
	m, _ := s.getSteer()
	if m == nil {
		writeError(w, http.StatusNotImplemented, "steering not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	var req steerInterruptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	steerer := authSubject(r)
	_ = m.Register(id, nil)
	if err := m.RequestInterrupt(id, steerer, req.Reason); err != nil {
		writeSteerError(w, err)
		return
	}
	s.emitSteer(id, steering.EventInterruptRequested, map[string]any{
		"run_id": id, "steerer_id": steerer, "reason": req.Reason, "session_id": id,
	})
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "state": "pause_requested"})
}

func (s *Server) handleSteerAck(w http.ResponseWriter, r *http.Request) {
	m, _ := s.getSteer()
	if m == nil {
		writeError(w, http.StatusNotImplemented, "steering not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	if err := m.AcknowledgePaused(id); err != nil {
		writeSteerError(w, err)
		return
	}
	s.emitSteer(id, steering.EventPaused, map[string]any{"run_id": id, "session_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "state": "paused"})
}

type steerPromptRequest struct {
	Prompt string `json:"prompt"`
}

func (s *Server) handleSteerPrompt(w http.ResponseWriter, r *http.Request) {
	m, _ := s.getSteer()
	if m == nil {
		writeError(w, http.StatusNotImplemented, "steering not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	var req steerPromptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	steerer := authSubject(r)
	if err := m.Steer(id, steerer, req.Prompt); err != nil {
		writeSteerError(w, err)
		return
	}
	s.emitSteer(id, steering.EventSteerPrompt, map[string]any{
		"run_id": id, "steerer_id": steerer, "prompt": req.Prompt, "session_id": id,
	})
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "queued": true})
}

func (s *Server) handleSteerResume(w http.ResponseWriter, r *http.Request) {
	m, _ := s.getSteer()
	if m == nil {
		writeError(w, http.StatusNotImplemented, "steering not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id path parameter is required")
		return
	}
	steerer := authSubject(r)
	if err := m.Resume(id, steerer); err != nil {
		writeSteerError(w, err)
		return
	}
	s.emitSteer(id, steering.EventResumed, map[string]any{"run_id": id, "steerer_id": steerer, "session_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "state": "running"})
}

func (s *Server) emitSteer(sessionID, eventType string, payload map[string]any) {
	projectID := ""
	if ss, ok := s.sessionStore(); ok {
		if sess, err := ss.GetSession(context.Background(), sessionID); err == nil {
			projectID = sess.ProjectID
		}
	}
	s.steerEmitterFor(projectID).Emit(eventType, payload)
}

// routeSteerAction is the hub SteerHook (issue #42): WS actions carrying a
// steering event type drive the InterruptManager state machine and fan out
// through the SteerEmitter, exactly like the HTTP steer routes. Non-steering
// event types return handled=false so the hub broadcasts them normally.
//
// Cross-run binding (issue #134): the target runID must equal the sender's
// subscribed session — otherwise a client in session A could Interrupt /
// Steer / Resume session B by naming its run_id. Unsubscribed clients
// (no session) cannot steer at all.
func (s *Server) routeSteerAction(clientID, userID, eventType string, payload map[string]any) (bool, error) {
	if !steering.IsSteeringEvent(eventType) {
		return false, nil
	}
	m, hub := s.getSteer()
	if m == nil {
		return true, errors.New("steering not configured")
	}
	runID := ""
	if payload != nil {
		runID, _ = payload["run_id"].(string)
		if strings.TrimSpace(runID) == "" {
			runID, _ = payload["session_id"].(string)
		}
	}
	if strings.TrimSpace(runID) == "" {
		return true, errors.New("run_id is required for steering actions")
	}
	var clientSession string
	if hub != nil {
		if c := hub.Get(clientID); c != nil {
			clientSession = c.SessionID
		}
	}
	if strings.TrimSpace(clientSession) == "" || runID != clientSession {
		return true, errors.New("run_id does not match the subscribed session")
	}
	cp := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		cp[k] = v
	}
	cp["run_id"] = runID
	if _, ok := cp["session_id"]; !ok {
		cp["session_id"] = runID
	}
	switch eventType {
	case steering.EventInterruptRequested:
		_ = m.Register(runID, nil)
		reason, _ := cp["reason"].(string)
		if err := m.RequestInterrupt(runID, userID, reason); err != nil {
			return true, err
		}
	case steering.EventSteerPrompt:
		prompt, _ := cp["prompt"].(string)
		if err := m.Steer(runID, userID, prompt); err != nil {
			return true, err
		}
	case steering.EventPaused:
		if err := m.AcknowledgePaused(runID); err != nil {
			return true, err
		}
	case steering.EventResumed:
		if err := m.Resume(runID, userID); err != nil {
			return true, err
		}
	}
	cp["steerer_id"] = userID
	s.emitSteer(runID, eventType, cp)
	return true, nil
}

func writeSteerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, steering.ErrNoRun):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, steering.ErrSteerConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, steering.ErrInvalidState):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, steering.ErrEmptyPrompt):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		if strings.Contains(err.Error(), "is required") || strings.Contains(err.Error(), "must not be empty") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
