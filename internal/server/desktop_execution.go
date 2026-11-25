package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/desktopbridge"
	"central-memory/internal/store"
)

func (s *Server) registerDesktopExecutionRoutes() {
	account := func(next http.HandlerFunc) http.HandlerFunc { return s.requireAuth(desktopAccountOnly(next)) }
	s.Mux.HandleFunc("GET /v1/desktop/devices", account(s.desktopDevices))
	s.Mux.HandleFunc("POST /v1/desktop/devices", account(s.desktopRegister))
	s.Mux.HandleFunc("POST /v1/desktop/devices/{id}/claim", account(s.desktopClaim))
	s.Mux.HandleFunc("POST /v1/desktop/jobs", account(s.desktopCreate))
	s.Mux.HandleFunc("GET /v1/desktop/jobs/{id}", account(s.desktopGet))
	s.Mux.HandleFunc("GET /v1/desktop/jobs/{id}/events", account(s.desktopEvents))
	s.Mux.HandleFunc("POST /v1/desktop/jobs/{id}/progress", account(s.desktopProgress))
	s.Mux.HandleFunc("POST /v1/desktop/jobs/{id}/cancel", account(s.desktopCancel))
}

// An agent's memory/MCP API credential is not an account sign-in and must not
// gain a new device-registration or execution capability through this feature.
func desktopAccountOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// requireAuth sets X-Auth-Token-ID for every opaque API token; the
		// prefix check also covers tokens of the current nxs_ format.
		if r.Header.Get("X-Auth-Token-ID") != "" || strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), "nxs_") {
			writeError(w, 403, "sign in to your Nexus account; agent/API tokens cannot use desktop execution")
			return
		}
		next(w, r)
	}
}
func (s *Server) desktopReady(w http.ResponseWriter) bool {
	if s.Desktop == nil {
		writeError(w, 503, "desktop execution storage is not configured")
		return false
	}
	return true
}
func (s *Server) desktopError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, desktopbridge.ErrNotFound):
		writeError(w, 404, "desktop request not found")
	case errors.Is(e, desktopbridge.ErrConflict):
		writeError(w, 409, "desktop request changed, expired, or queue is full")
	default:
		s.Log.Printf("desktop channel: %v", e)
		writeError(w, 500, "desktop channel unavailable")
	}
}
func (s *Server) desktopDevices(w http.ResponseWriter, r *http.Request) {
	if !s.desktopReady(w) {
		return
	}
	items, e := s.Desktop.Devices(r.Context(), authSubject(r))
	if e != nil {
		s.desktopError(w, e)
		return
	}
	// Device credentials and local paths never appear in the public response.
	writeJSON(w, 200, map[string]any{"items": items, "online_threshold_seconds": int(desktopbridge.DeviceLifetime.Seconds())})
}
func (s *Server) desktopRegister(w http.ResponseWriter, r *http.Request) {
	if !s.desktopReady(w) {
		return
	}
	var req struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Projects  []string `json:"projects"`
		Harnesses []string `json:"harnesses"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	secret := r.Header.Get("X-Nexus-Desktop-Key")
	if len(secret) < 32 || len(secret) > 256 || len(req.ID) < 16 || len(req.ID) > 80 || strings.ContainsAny(req.ID, "/\\\x00") || len(req.Name) > 100 || len(req.Projects) > 20 || len(req.Harnesses) > 4 {
		writeError(w, 400, "invalid desktop registration")
		return
	}
	for _, project := range req.Projects {
		if !s.authorizeProject(w, r, project) {
			return
		}
	}
	for _, agent := range req.Harnesses {
		if !desktopbridge.ValidAgent(agent) {
			writeError(w, 400, "unsupported harness")
			return
		}
	}
	d := desktopbridge.Device{ID: req.ID, Owner: authSubject(r), KeyHash: desktopbridge.HashKey(secret), Name: req.Name, Projects: req.Projects, Harnesses: req.Harnesses, Seen: time.Now().UTC(), Protocol: desktopProtocol(r)}
	if e := s.Desktop.Register(r.Context(), d); e != nil {
		s.desktopError(w, e)
		return
	}
	writeJSON(w, 200, d)
}

// desktopProtocol reads the relay version a desktop reports in its header.
// No header (every desktop before images) is protocol 1.
func desktopProtocol(r *http.Request) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.Header.Get(desktopbridge.ProtocolHeader)))
	if err != nil || v < 1 {
		return 1
	}
	return min(v, 1000)
}
func (s *Server) desktopAuthenticate(w http.ResponseWriter, r *http.Request, id string) (desktopbridge.Device, bool) {
	if !s.desktopReady(w) {
		return desktopbridge.Device{}, false
	}
	d, e := s.Desktop.Device(r.Context(), authSubject(r), id)
	if e != nil || !d.Authenticate(r.Header.Get("X-Nexus-Desktop-Key")) {
		writeError(w, 403, "desktop credential required")
		return d, false
	}
	return d, true
}
func (s *Server) desktopClaim(w http.ResponseWriter, r *http.Request) {
	d, ok := s.desktopAuthenticate(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	j, e := s.Desktop.Claim(r.Context(), authSubject(r), d.ID, time.Now().UTC())
	if e != nil {
		s.desktopError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"job": j})
}
func (s *Server) desktopCreate(w http.ResponseWriter, r *http.Request) {
	if !s.desktopReady(w) {
		return
	}
	var req desktopbridge.Request
	// Turns may carry up to four images; every other route keeps the default limit.
	if !decodeJSONLimit(w, r, &req, desktopbridge.MaxCreateBodyBytes) {
		return
	}
	if e := req.Validate(); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	if e := req.PrepareAttachments(); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	if !s.authorizeProject(w, r, req.ProjectID) {
		return
	}
	if req.Kind == "turn" {
		row, ok := s.requireAgentOwner(w, r, req.SessionID)
		if !ok {
			return
		}
		if row.ProjectID != req.ProjectID {
			writeError(w, 400, "session is not in this project")
			return
		}
	}
	d, e := s.Desktop.Device(r.Context(), authSubject(r), req.DeviceID)
	if e != nil {
		s.desktopError(w, e)
		return
	}
	now := time.Now().UTC()
	if now.Sub(d.Seen) > desktopbridge.DeviceLifetime || !desktopbridge.Contains(d.Projects, req.ProjectID) || !desktopbridge.Contains(d.Harnesses, req.Agent) {
		writeError(w, 409, "desktop is offline, workspace is not mapped, or harness is not installed")
		return
	}
	// An older desktop would ignore the images and run the text alone.
	// Refuse plainly instead, before anything is queued.
	if len(req.Attachments) > 0 && d.Protocol < desktopbridge.ProtocolImages {
		writeError(w, 409, desktopbridge.ErrDesktopTooOld.Error())
		return
	}
	j, e := s.Desktop.Create(r.Context(), desktopbridge.Job{Request: req, Owner: authSubject(r), State: "queued", Revision: 1, Created: now, Updated: now, Expires: now.Add(desktopbridge.ApprovalLifetime), Status: "Waiting for Nexus Desktop"})
	if e != nil {
		s.desktopError(w, e)
		return
	}
	writeJSON(w, 202, j.Public())
}
func (s *Server) desktopReadJob(w http.ResponseWriter, r *http.Request) (desktopbridge.Job, bool) {
	if !s.desktopReady(w) {
		return desktopbridge.Job{}, false
	}
	j, e := s.Desktop.Get(r.Context(), authSubject(r), r.PathValue("id"))
	if e != nil {
		s.desktopError(w, e)
		return j, false
	}
	if !s.authorizeProject(w, r, j.ProjectID) {
		return j, false
	}
	if j.Kind == "turn" {
		if _, ok := s.requireAgentOwner(w, r, j.SessionID); !ok {
			return j, false
		}
	}
	return j, true
}
func (s *Server) desktopGet(w http.ResponseWriter, r *http.Request) {
	if j, ok := s.desktopReadJob(w, r); ok {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, j.Public())
	}
}
func (s *Server) desktopCancel(w http.ResponseWriter, r *http.Request) {
	j, ok := s.desktopReadJob(w, r)
	if !ok {
		return
	}
	j, e := s.Desktop.Cancel(r.Context(), authSubject(r), j.ID, time.Now().UTC())
	if e != nil {
		s.desktopError(w, e)
		return
	}
	writeJSON(w, 200, j.Public())
}
func (s *Server) desktopProgress(w http.ResponseWriter, r *http.Request) {
	j, ok := s.desktopReadJob(w, r)
	if !ok {
		return
	}
	if _, ok := s.desktopAuthenticate(w, r, j.DeviceID); !ok {
		return
	}
	var update desktopbridge.Update
	if !decodeJSON(w, r, &update) {
		return
	}
	j, e := s.Desktop.Update(r.Context(), authSubject(r), j.DeviceID, j.ID, update, time.Now().UTC())
	if e != nil {
		s.desktopError(w, e)
		return
	}
	writeJSON(w, 200, j.Public())
}

// Browser SSE uses an Authorization header (fetch), never a bearer token in a
// URL. Reconnecting returns the latest complete snapshot, not lost token deltas.
func (s *Server) desktopEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.desktopReadJob(w, r)
	if !ok {
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	revision := int64(-1)
	for {
		if j.Revision != revision {
			b, _ := json.Marshal(j.Public())
			if _, e := fmt.Fprintf(w, "id: %d\nevent: snapshot\ndata: %s\n\n", j.Revision, b); e != nil {
				return
			}
			flush.Flush()
			revision = j.Revision
		}
		if desktopbridge.Terminal(j.State) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
		// Revalidate ACLs while streaming: an open connection is not a grant.
		member, e := s.Store.IsProjectMember(r.Context(), authSubject(r), j.ProjectID)
		if e != nil || !member {
			return
		}
		var err error
		j, err = s.Desktop.Get(r.Context(), authSubject(r), j.ID)
		if err != nil {
			return
		}
	}
}

// requireAgentOwner loads a live (non-archived) session the caller owns.
// Sending a turn to a desktop is a write, so grantees and admins are refused.
func (s *Server) requireAgentOwner(w http.ResponseWriter, r *http.Request, sessionID string) (*store.AgentSession, bool) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return nil, false
	}
	row, err := cs.GetAgentSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "could not load session")
		return nil, false
	}
	if row.OwnerUserID == "" || row.OwnerUserID != authSubject(r) {
		writeError(w, http.StatusForbidden, "only the session owner can modify it")
		return nil, false
	}
	if row.LineageKind == "legacy_snapshot" {
		writeError(w, http.StatusForbidden, "archived sessions are read-only")
		return nil, false
	}
	return row, true
}
