package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"central-memory/internal/store"
)

func (s *Server) registerTimelineRoutes() {
	s.Mux.HandleFunc("POST /v1/agent-sessions", s.requireAuth(s.handleAgentSessionUpsert))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}", s.requireAuth(s.handleAgentSessionGet))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/grants", s.requireAuth(s.handleAgentSessionGrant))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/turns", s.requireAuth(s.handleAgentTurnPost))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/turns", s.requireAuth(s.handleAgentTurnList))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/versions", s.requireAuth(s.handleAgentVersionList))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/versions", s.requireAuth(s.handleAgentVersionCreate))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/versions/{version}/complete", s.requireAuth(s.handleAgentVersionComplete))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/forks", s.requireAuth(s.handleAgentSessionForks))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/fork", s.requireAuth(s.handleAgentSessionFork))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/merge-code", s.requireAuth(s.handleAgentSessionMergeCode))
	s.Mux.HandleFunc("POST /v1/blobs/missing", s.requireAuth(s.handleBlobsMissing))
	s.Mux.HandleFunc("PUT /v1/blobs/{sha256}", s.requireAuth(s.handleBlobPut))
	s.Mux.HandleFunc("GET /v1/timeline", s.requireAuth(s.handleTimeline))
}

func (s *Server) agentCloud() (store.AgentCloudStore, bool) {
	cs, ok := s.Store.(store.AgentCloudStore)
	return cs, ok
}

func (s *Server) handleAgentSessionUpsert(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	var body struct {
		ProjectID         string `json:"project_id"`
		Harness           string `json:"harness"`
		NativeID          string `json:"native_id"`
		OriginMachineID   string `json:"origin_machine_id"`
		Title             string `json:"title"`
		WorkspaceRootHint string `json:"workspace_root_hint"`
		Visibility        string `json:"visibility"`
		Summary           string `json:"summary"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !s.authorizeProject(w, r, body.ProjectID) {
		return
	}
	if s.rejectIfCaptureOff(w, r, body.ProjectID) {
		return
	}
	row, err := cs.UpsertAgentSession(r.Context(), &store.AgentSession{
		ProjectID: body.ProjectID, OwnerUserID: authSubject(r), Harness: body.Harness,
		NativeID: body.NativeID, OriginMachineID: body.OriginMachineID, Title: body.Title,
		WorkspaceRootHint: body.WorkspaceRootHint, Visibility: body.Visibility, Summary: body.Summary,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "session is owned by someone else")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (s *Server) requireAgentRead(w http.ResponseWriter, r *http.Request, sessionID string) (*store.AgentSession, bool) {
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	allowed, err := cs.CanReadAgentSession(r.Context(), authSubject(r), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "session is private")
		return nil, false
	}
	return row, true
}

func (s *Server) handleAgentSessionGet(w http.ResponseWriter, r *http.Request) {
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleAgentSessionForks(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	if _, ok := s.requireAgentRead(w, r, r.PathValue("id")); !ok {
		return
	}
	items, err := cs.ListAgentSessionForks(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleAgentSessionFork(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	parent, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.rejectIfCaptureOff(w, r, parent.ProjectID) {
		return
	}
	child, err := cs.ForkAgentSession(r.Context(), parent.ID, authSubject(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, child)
}

func (s *Server) handleAgentSessionMergeCode(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	target, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		FromSessionID string `json:"from_session_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	fromID := strings.TrimSpace(body.FromSessionID)
	if fromID == "" {
		writeError(w, http.StatusBadRequest, "from_session_id is required")
		return
	}
	from, ok := s.requireAgentRead(w, r, fromID)
	if !ok {
		return
	}
	if from.ID == target.ID {
		writeError(w, http.StatusBadRequest, "from_session_id must differ from the target session")
		return
	}
	targetBranch := forkBranchName(target)
	fromBranch := forkBranchName(from)
	marked, err := cs.MarkAgentSessionCodeMerged(r.Context(), from.ID, target.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"target_session_id": target.ID,
		"from_session_id":   from.ID,
		"target_branch":     targetBranch,
		"from_branch":       fromBranch,
		"new_session":       false,
		"note":              "Code-only merge (D17): conversations stay separate; no new session was created",
		"instructions": []string{
			"git fetch --all",
			"git checkout " + targetBranch,
			"git merge --no-ff " + fromBranch,
			"# resolve conflicts in your editor, then: git commit",
			"# or open a PR: gh pr create --base " + targetBranch + " --head " + fromBranch,
		},
		"merged_marker": map[string]any{
			"from_session_id":  marked.ID,
			"code_merged_into": marked.CodeMergedInto,
			"code_merged_at":   marked.CodeMergedAt,
			"lineage_kind":     marked.LineageKind,
		},
	})
}

func forkBranchName(sess *store.AgentSession) string {
	if sess == nil {
		return "fork/unknown"
	}
	owner := strings.TrimSpace(sess.OwnerUserID)
	if owner == "" {
		owner = "user"
	}
	slug := strings.TrimSpace(sess.NativeID)
	if slug == "" {
		slug = sess.ID
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-")
	return "fork/" + replacer.Replace(owner) + "/" + replacer.Replace(slug)
}

func (s *Server) handleAgentSessionGrant(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	sessionID := r.PathValue("id")
	row, err := cs.GetAgentSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row.OwnerUserID != authSubject(r) {
		writeError(w, http.StatusForbidden, "only the session owner can share it")
		return
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.UserID) == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if err := cs.GrantAgentSession(r.Context(), sessionID, body.UserID, authSubject(r)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.grant_added", ResourceKind: "session", ResourceID: sessionID,
		ProjectID: row.ProjectID, Metadata: map[string]any{"grantee_user_id": body.UserID},
	})
	writeJSON(w, http.StatusCreated, map[string]any{"session_id": sessionID, "user_id": body.UserID})
}

func (s *Server) handleAgentTurnPost(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.rejectIfCaptureOff(w, r, row.ProjectID) {
		return
	}
	var body struct {
		Idx         int             `json:"idx"`
		Role        string          `json:"role"`
		TextPreview string          `json:"text_preview"`
		ToolCalls   json.RawMessage `json:"tool_calls"`
		BlobRef     string          `json:"blob_ref"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	err := cs.AppendSessionTurn(r.Context(), store.SessionTurn{
		SessionID: r.PathValue("id"), Idx: body.Idx, Role: body.Role,
		TextPreview: body.TextPreview, ToolCalls: body.ToolCalls, BlobRef: body.BlobRef,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) handleAgentTurnList(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	if _, ok := s.requireAgentRead(w, r, r.PathValue("id")); !ok {
		return
	}
	turns, err := cs.ListSessionTurns(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": turns, "count": len(turns)})
}

// handleAgentVersionList returns complete versions the caller may see.
// Point-in-time grants only include the pinned version (D15).
func (s *Server) handleAgentVersionList(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if _, ok := s.requireAgentRead(w, r, sessionID); !ok {
		return
	}
	items, err := cs.ListVisibleSessionVersions(r.Context(), sessionID, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// requireVisibleVersion loads a version the caller can read under their grant.
// PIT grantees cannot select newer complete versions than their pin.
func (s *Server) requireVisibleVersion(w http.ResponseWriter, r *http.Request, sessionID string, version int) (*store.SessionVersion, bool) {
	if _, ok := s.requireAgentRead(w, r, sessionID); !ok {
		return nil, false
	}
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return nil, false
	}
	visible, err := cs.ListVisibleSessionVersions(r.Context(), sessionID, authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if version <= 0 {
		// "latest" = highest visible complete version.
		var best *store.SessionVersion
		for i := range visible {
			if best == nil || visible[i].Version > best.Version {
				cp := visible[i]
				best = &cp
			}
		}
		if best == nil {
			writeError(w, http.StatusNotFound, "version not found")
			return nil, false
		}
		return best, true
	}
	for i := range visible {
		if visible[i].Version == version {
			cp := visible[i]
			return &cp, true
		}
	}
	writeError(w, http.StatusNotFound, "version not found")
	return nil, false
}

func (s *Server) handleAgentVersionCreate(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.rejectIfCaptureOff(w, r, row.ProjectID) {
		return
	}
	v, err := cs.CreateSessionVersion(r.Context(), r.PathValue("id"), authSubject(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleAgentVersionComplete(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.rejectIfCaptureOff(w, r, row.ProjectID) {
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	v, err := cs.CompleteSessionVersion(r.Context(), r.PathValue("id"), version, raw)
	if err != nil {
		var missing *store.MissingBlobsError
		if errors.As(err, &missing) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   map[string]any{"code": http.StatusConflict, "message": err.Error()},
				"missing": missing.Missing,
			})
			return
		}
		if errors.Is(err, store.ErrStorageFull) {
			writeError(w, http.StatusPaymentRequired, "storage full: send a transcript-only manifest or free space")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "version is already complete")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleBlobsMissing(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	var body struct {
		ProjectID string   `json:"project_id"`
		Hashes    []string `json:"hashes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !s.authorizeProject(w, r, body.ProjectID) {
		return
	}
	missing, err := cs.MissingBlobs(r.Context(), body.ProjectID, body.Hashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missing": missing})
}

func (s *Server) handleBlobPut(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	var body struct {
		ProjectID string `json:"project_id"`
		BodyB64   string `json:"body_b64"`
		Kind      string `json:"kind"`
		Purpose   string `json:"purpose"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !s.authorizeProject(w, r, body.ProjectID) {
		return
	}
	if s.rejectIfCaptureOff(w, r, body.ProjectID) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.BodyB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad body_b64")
		return
	}
	sum := strings.ToLower(r.PathValue("sha256"))
	got := sha256.Sum256(raw)
	if hex.EncodeToString(got[:]) != sum {
		writeError(w, http.StatusBadRequest, "sha256 does not match body")
		return
	}
	if err := cs.PutBlob(r.Context(), body.ProjectID, sum, body.Kind, body.Purpose, raw); err != nil {
		if errors.Is(err, store.ErrStorageFull) {
			writeError(w, http.StatusPaymentRequired, "storage full")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sha256": sum, "size": len(raw)})
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID != "" && !s.authorizeProject(w, r, projectID) {
		return
	}
	items, next, err := cs.ListTimeline(r.Context(), store.TimelineQuery{
		UserID: authSubject(r), ProjectID: projectID,
		Harness: strings.TrimSpace(r.URL.Query().Get("harness")),
		Cursor:  r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
