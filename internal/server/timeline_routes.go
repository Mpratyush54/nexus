package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"central-memory/internal/store"
)

func (s *Server) registerTimelineRoutes() {
	s.Mux.HandleFunc("GET /v1/sharing/recipients", s.requireAuth(s.handleSharingRecipients))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/grants/organization", s.requireAuth(s.handleShareWithOrganization))
	s.Mux.HandleFunc("POST /v1/agent-sessions", s.requireAuth(s.handleAgentSessionUpsert))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}", s.requireAuth(s.handleAgentSessionGet))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/grants", s.requireAuth(s.handleAgentSessionGrant))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/turns", s.requireAuth(s.handleAgentTurnPost))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/turns/batch", s.requireAuth(s.handleAgentTurnBatchPost))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/turns", s.requireAuth(s.handleAgentTurnList))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/versions", s.requireAuth(s.handleAgentVersionList))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/versions", s.requireAuth(s.handleAgentVersionCreate))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/versions/{version}/complete", s.requireAuth(s.handleAgentVersionComplete))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/forks", s.requireAuth(s.handleAgentSessionForks))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/fork", s.requireAuth(s.handleAgentSessionFork))
	s.Mux.HandleFunc("POST /v1/agent-sessions/{id}/merge-code", s.requireAuth(s.handleAgentSessionMergeCode))
	s.Mux.HandleFunc("POST /v1/blobs/missing", s.requireAuth(s.handleBlobsMissing))
	s.Mux.HandleFunc("PUT /v1/blobs/{sha256}", s.requireAuth(s.handleBlobPut))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/versions/{version}/blobs/{sha256}", s.requireAuth(s.handleSessionVersionBlob))
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
			// Snapshots captured before the agent-session store shipped still
			// belong to their owners. Present those records through the same
			// read model so a desktop upgrade does not make existing history
			// appear to vanish. This is deliberately read-only: a legacy
			// snapshot cannot be shared, edited, or teleported until it is
			// explicitly imported by a future migration flow.
			legacy, legacyOK := s.legacySnapshotSession(r.Context(), authSubject(r), sessionID)
			if legacyOK {
				return legacy, true
			}
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
		SourceAt    *time.Time      `json:"source_ts"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	err := cs.AppendSessionTurn(r.Context(), store.SessionTurn{
		SessionID: r.PathValue("id"), Idx: body.Idx, Role: body.Role,
		TextPreview: body.TextPreview, ToolCalls: body.ToolCalls, BlobRef: body.BlobRef,
		SourceAt: validAgentSourceTime(body.SourceAt),
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

// Batch ingest avoids one WAN round-trip per captured turn when importing an
// existing agent database. Duplicate indexes are safe to retry after a
// partial upload; other failures stop the batch and remain visible to the
// caller instead of silently discarding chat history.
func (s *Server) handleAgentTurnBatchPost(w http.ResponseWriter, r *http.Request) {
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
		Turns []struct {
			Idx         int             `json:"idx"`
			Role        string          `json:"role"`
			TextPreview string          `json:"text_preview"`
			ToolCalls   json.RawMessage `json:"tool_calls"`
			SourceAt    *time.Time      `json:"source_ts"`
		} `json:"turns"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Turns) == 0 || len(body.Turns) > 50 {
		writeError(w, http.StatusBadRequest, "turns must contain 1 to 50 items")
		return
	}
	created, duplicates := 0, 0
	for _, turn := range body.Turns {
		err := cs.AppendSessionTurn(r.Context(), store.SessionTurn{
			SessionID: row.ID, Idx: turn.Idx, Role: turn.Role,
			TextPreview: turn.TextPreview, ToolCalls: turn.ToolCalls,
			SourceAt: validAgentSourceTime(turn.SourceAt),
		})
		if errors.Is(err, store.ErrConflict) {
			duplicates++
			continue
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		created++
	}
	writeJSON(w, http.StatusCreated, map[string]any{"created": created, "duplicates": duplicates})
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
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID != "" && !s.authorizeProject(w, r, projectID) {
		return
	}
	cursor := r.URL.Query().Get("cursor")
	harness := strings.TrimSpace(r.URL.Query().Get("harness"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	items, modernNext, err := cs.ListTimeline(r.Context(), store.TimelineQuery{
		UserID: authSubject(r), ProjectID: projectID,
		Harness: harness, Query: query, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A conversation captured both ways (an old snapshot and a modern
	// session) is one chat: the snapshot's id is the native id, so match on
	// harness + native id too, and keep the modern session. Copies on other
	// pages are excluded by ListLegacyTimeline itself; only copies this
	// viewer can read hide a snapshot, so a snapshot shared with someone
	// stays listed when the modern copy is private to its owner.
	seen := make(map[string]bool, len(items)*2)
	for _, item := range items {
		seen[item.SessionID] = true
		if item.NativeID != "" {
			seen[item.Harness+"\x00"+item.NativeID] = true
		}
	}
	hidden := func(item store.TimelineItem) bool {
		return seen[item.SessionID] || (item.NativeID != "" && seen[item.Harness+"\x00"+item.NativeID])
	}
	// Historical snapshots and agent sessions coexist. Query both on every
	// page; otherwise the first new capture hides the entire old timeline.
	// limit+1 legacy rows are enough to fill the page and tell whether more
	// remain.
	legacy, floor, err := s.legacyTimeline(r.Context(), authSubject(r), projectID, harness, query, cursor, limit+1, hidden)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list captured snapshots")
		return
	}
	items = append(items, legacy...)
	sort.Slice(items, func(i, j int) bool {
		return timelineNewer(items[i].LastActiveAt, items[i].SessionID, items[j].LastActiveAt, items[j].SessionID)
	})
	hasMore := len(items) > limit || modernNext != ""
	if floor != nil {
		// The archived-transcript search stopped at floor: nothing at or
		// below it is known yet, so the page ends above it and the next page
		// resumes there.
		kept := items[:0]
		for _, item := range items {
			if timelineNewer(item.LastActiveAt, item.SessionID, floor.UpdatedAt, floor.SessionID) {
				kept = append(kept, item)
			}
		}
		items, hasMore = kept, true
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
	}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next = last.LastActiveAt.UTC().Format(time.RFC3339Nano) + "|" + last.SessionID
	} else if hasMore && floor != nil {
		// Nothing above floor matched. Resume at floor itself: a cursor one
		// microsecond (the store's time resolution) above it with an empty id
		// sorts after every row at floor's time.
		next = floor.UpdatedAt.Add(time.Microsecond).UTC().Format(time.RFC3339Nano) + "|"
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// legacySnapshotSession turns an owner-visible historical snapshot into the
// read-only subset of AgentSession used by the desktop's session page.
func (s *Server) legacySnapshotSession(ctx context.Context, userID, sessionID string) (*store.AgentSession, bool) {
	ps := provenanceStore(s.Store)
	if ps == nil {
		return nil, false
	}
	var snap *store.SessionSnapshot
	var err error
	if lts, ok := s.Store.(store.LegacyTimelineStore); ok {
		// Metadata only: the read check never needs the payloads.
		snap, err = lts.GetLatestSnapshotMeta(ctx, sessionID)
	} else {
		snap, err = ps.GetLatestSnapshot(ctx, sessionID)
	}
	if err != nil || !s.canReadSnapshot(ctx, userID, snap) {
		return nil, false
	}
	return legacyAgentSession(snap), true
}

func (s *Server) canReadSnapshot(ctx context.Context, userID string, snap *store.SessionSnapshot) bool {
	if snap == nil {
		return false
	}
	if snap.OwnerUserID != "" && snap.OwnerUserID == userID {
		return true
	}
	if snap.OwnerUserID == "" {
		// Ownerless historical rows retain the existing project-admin rule.
		// This method intentionally has no ResponseWriter: callers can use it
		// while building a filtered list without leaking private records.
		return false
	}
	acl, ok := s.sessionContent()
	if !ok {
		return false
	}
	granted, err := acl.HasSessionContentGrant(ctx, snap.SessionID, userID)
	return err == nil && granted
}

func legacyAgentSession(snap *store.SessionSnapshot) *store.AgentSession {
	if snap == nil {
		return nil
	}
	title := "Captured " + firstNonEmpty(snap.Harness, "agent") + " snapshot"
	return &store.AgentSession{
		ID: snap.SessionID, ProjectID: snap.ProjectID, OwnerUserID: snap.OwnerUserID,
		Harness: snap.Harness, NativeID: snap.ConversationID, OriginMachineID: snap.SourceMachineID,
		Title: title, Summary: "Archived snapshot", StartedAt: snap.CreatedAt,
		LastActiveAt: snap.UpdatedAt, Visibility: "private", LineageKind: "legacy_snapshot",
	}
}

// timelineNewer orders timeline rows newest first, then by id descending.
func timelineNewer(at time.Time, id string, otherAt time.Time, otherID string) bool {
	if at.Equal(otherAt) {
		return id > otherID
	}
	return at.After(otherAt)
}

func parseTimelineCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	idx := strings.LastIndex(cursor, "|")
	if idx <= 0 {
		return time.Time{}, "", errors.New("bad timeline cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, cursor[:idx])
	if err != nil {
		return time.Time{}, "", err
	}
	return at, cursor[idx+1:], nil
}

// A text search over archived transcripts stops after this many
// transcripts or this long, whichever comes first. The page then ends early
// and its cursor resumes the search, so no match is lost; a slow search
// just takes several pages instead of one very slow request. Variables so
// tests can shrink them.
var (
	legacySearchTranscriptBudget = 500
	legacySearchTimeBudget       = 1500 * time.Millisecond
)

// legacyTranscriptBatch is how many transcripts one query loads.
const legacyTranscriptBatch = 16

func legacyTimelineItem(snap *store.SessionSnapshot) store.TimelineItem {
	legacy := legacyAgentSession(snap)
	return store.TimelineItem{
		SessionID: legacy.ID, ProjectID: legacy.ProjectID, Harness: legacy.Harness,
		NativeID: legacy.NativeID, Title: legacy.Title, Summary: legacy.Summary,
		Visibility: legacy.Visibility, OwnerUserID: legacy.OwnerUserID,
		LastActiveAt: legacy.LastActiveAt, TurnCount: snap.TurnCount, VersionState: "legacy_snapshot",
	}
}

// legacyTimeline returns up to need archived sessions after cursor, newest
// first, skipping those hidden reports. Rows come from one paged query with
// the read rule in SQL; a text search decompresses only the transcripts of
// rows whose title does not already match, at most
// legacySearchTranscriptBudget of them within legacySearchTimeBudget. When
// the budget runs out it returns the first unchecked row as floor: rows at or
// below it are not known yet.
func (s *Server) legacyTimeline(ctx context.Context, userID, projectID, harness, query, cursor string, need int, hidden func(store.TimelineItem) bool) ([]store.TimelineItem, *store.SessionSnapshot, error) {
	items := []store.TimelineItem{}
	lts, ok := s.Store.(store.LegacyTimelineStore)
	if !ok || provenanceStore(s.Store) == nil {
		return items, nil, nil
	}
	cursorAt, cursorID, err := parseTimelineCursor(cursor)
	if err != nil {
		return nil, nil, err
	}
	lowerQuery := strings.ToLower(query)
	needsTranscript := func(item store.TimelineItem) bool {
		if query == "" || (hidden != nil && hidden(item)) {
			return false
		}
		return !strings.Contains(strings.ToLower(item.Title+" "+item.Harness+" "+item.NativeID), lowerQuery)
	}
	budget := legacySearchTranscriptBudget
	deadline := time.Now().Add(legacySearchTimeBudget)
	batch := need
	if batch < 50 {
		batch = 50
	}
	if query != "" {
		// A search skips most rows; fetch more per round trip.
		batch = 200
	}
	for len(items) < need {
		rows, err := lts.ListLegacyTimeline(ctx, store.LegacyTimelineQuery{
			UserID: userID, ProjectID: projectID, Harness: harness,
			CursorAt: cursorAt, CursorID: cursorID, Limit: batch,
		})
		if err != nil {
			return nil, nil, err
		}
		transcripts := map[string][]byte{}
		for i := range rows {
			snap := &rows[i]
			item := legacyTimelineItem(snap)
			if hidden != nil && hidden(item) {
				continue
			}
			if needsTranscript(item) {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				payload, loaded := transcripts[snap.SessionID]
				if !loaded {
					if budget <= 0 || time.Now().After(deadline) {
						return items, snap, nil
					}
					// Load this row's transcript with the next few that need
					// one, in a single query.
					ids := []string{}
					for j := i; j < len(rows) && len(ids) < legacyTranscriptBatch && len(ids) < budget; j++ {
						if needsTranscript(legacyTimelineItem(&rows[j])) {
							ids = append(ids, rows[j].SessionID)
						}
					}
					budget -= len(ids)
					got, err := s.loadLegacyTranscripts(ctx, lts, ids)
					if err != nil {
						return nil, nil, err
					}
					for _, id := range ids {
						transcripts[id] = got[id]
					}
					payload = transcripts[snap.SessionID]
				}
				// One unreadable snapshot only drops out of the search
				// results; it must not fail the whole timeline.
				idx, role, preview, found := searchLegacyTranscript(payload, query)
				if !found {
					continue
				}
				item.MatchTurnIdx, item.MatchRole, item.MatchPreview = &idx, role, preview
			}
			items = append(items, item)
			if len(items) == need {
				return items, nil, nil
			}
		}
		if len(rows) < batch {
			break
		}
		last := rows[len(rows)-1]
		cursorAt, cursorID = last.UpdatedAt, last.SessionID
	}
	return items, nil, nil
}

// loadLegacyTranscripts loads several latest transcripts in one query. If
// that fails it falls back to one read per snapshot, skipping any that fail,
// so one bad row cannot hide the rest.
func (s *Server) loadLegacyTranscripts(ctx context.Context, lts store.LegacyTimelineStore, ids []string) (map[string][]byte, error) {
	got, err := lts.LatestSnapshotTranscripts(ctx, ids)
	if err == nil {
		return got, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	got = map[string][]byte{}
	ps := provenanceStore(s.Store)
	for _, id := range ids {
		snap, err := ps.GetLatestSnapshot(ctx, id)
		if err != nil || snap == nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			continue
		}
		got[id] = snap.TranscriptPayload
	}
	return got, nil
}

// searchLegacyTranscript finds the first user or assistant turn containing
// query (case-insensitive) in a gzipped transcript.
func searchLegacyTranscript(payload []byte, query string) (int, string, string, bool) {
	if len(payload) == 0 {
		return 0, "", "", false
	}
	raw, err := gunzipSnapshot(payload)
	if err != nil {
		return 0, "", "", false
	}
	lowerQuery := strings.ToLower(query)
	if transcriptPrefilterSafe(query) && !containsFoldASCII(raw, lowerQuery) && !hasASCIILoweringRune(raw) {
		return 0, "", "", false
	}
	for _, turn := range ParseTranscriptJSONL(raw) {
		if (turn.Role == "user" || turn.Role == "assistant") && strings.Contains(strings.ToLower(turn.TextPreview), lowerQuery) {
			return turn.Idx, turn.Role, store.TimelineMatchPreview(turn.TextPreview, query), true
		}
	}
	return 0, "", "", false
}

// hasASCIILoweringRune reports whether raw holds, literally or as a JSON
// escape, one of the two non-ASCII runes that lower-case to an ASCII letter:
// the Kelvin sign (k) and capital I with dot (i). The ASCII-only prefilter
// cannot see those, so such transcripts are always parsed.
func hasASCIILoweringRune(raw []byte) bool {
	return bytes.Contains(raw, []byte("\u212a")) || bytes.Contains(raw, []byte("\u0130")) ||
		containsFoldASCII(raw, `\u212a`) || containsFoldASCII(raw, `\u0130`)
}

// containsFoldASCII reports whether s contains lowerQuery (lower-case ASCII)
// ignoring ASCII case, without copying s.
func containsFoldASCII(s []byte, lowerQuery string) bool {
	n := len(lowerQuery)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lowerQuery[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

// transcriptPrefilterSafe reports whether a query found in a parsed turn's
// text must also appear, byte for byte up to case, in the raw JSONL, so the
// JSON parse can be skipped when it does not. That holds for one ASCII word
// with a letter and no character a JSON encoder may escape: turn text is a
// JSON string (or integer) value, or several joined with newlines.
func transcriptPrefilterSafe(query string) bool {
	letter := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		case c >= '0' && c <= '9':
		case strings.IndexByte("-_.:@#$%^*()+=[]{}|;,?!~'", c) >= 0:
		default:
			return false
		}
	}
	return letter
}
