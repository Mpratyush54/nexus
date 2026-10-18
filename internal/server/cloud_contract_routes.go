package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"central-memory/internal/blobs"
	"central-memory/internal/capture"
	"central-memory/internal/continuex"
	"central-memory/internal/secrets"
	"central-memory/internal/store"
	"central-memory/internal/teleport"
)

var (
	cloudContractOnce sync.Map
	secretBoxOnce     sync.Once
	secretBox         *secrets.Box
	secretBoxErr      error
)

func dataKeyBox() (*secrets.Box, error) {
	secretBoxOnce.Do(func() {
		secretBox, secretBoxErr = secrets.NewBox()
	})
	return secretBox, secretBoxErr
}

func (s *Server) registerCloudContractRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := cloudContractOnce.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("POST /v1/blobs/presign", s.requireAuth(s.handleBlobPresign))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/versions/{version}/preflight", s.requireAuth(s.handleVersionPreflight))
	s.Mux.HandleFunc("PUT /v1/agent-sessions/{id}/grants/team", s.requireAuth(s.handleGrantTeam))
	s.Mux.HandleFunc("DELETE /v1/agent-sessions/{id}/grants/team", s.requireAuth(s.handleUngrantTeam))
	s.Mux.HandleFunc("PUT /v1/agent-sessions/{id}/grants/{user}", s.requireAuth(s.handleGrantUser))
	s.Mux.HandleFunc("DELETE /v1/agent-sessions/{id}/grants/{user}", s.requireAuth(s.handleUngrantUser))
	s.Mux.HandleFunc("GET /v1/storage/usage", s.requireAuth(s.handleStorageUsage))
	s.Mux.HandleFunc("GET /v1/audit", s.requireAuth(s.handleAuditQuery))
	s.Mux.HandleFunc("GET /v1/timeline/stream", s.requireAuth(s.handleTimelineStream))
	s.Mux.HandleFunc("POST /v1/secrets/data-key", s.requireAuth(s.handleSecretDataKey))
	s.Mux.HandleFunc("POST /v1/secrets/{blob}/decrypt", s.requireAuth(s.handleSecretDecrypt))
	s.Mux.HandleFunc("PUT /v1/secrets/{blob}/grants/{user}", s.requireAuth(s.handleSecretGrant))
	s.Mux.HandleFunc("DELETE /v1/secrets/{blob}/grants/{user}", s.requireAuth(s.handleSecretUngrant))
	s.Mux.HandleFunc("GET /v1/secrets/audit", s.requireAuth(s.handleSecretAudit))
	s.Mux.HandleFunc("POST /v1/continue", s.requireAuth(s.handleContinue))
	s.Mux.HandleFunc("POST /v1/teleports", s.requireAuth(s.handleTeleportSend))
	s.Mux.HandleFunc("GET /v1/teleports/inbox", s.requireAuth(s.handleTeleportInbox))
	s.Mux.HandleFunc("GET /v1/teleports/sent", s.requireAuth(s.handleTeleportSent))
	s.Mux.HandleFunc("POST /v1/teleports/{id}/revoke", s.requireAuth(s.handleTeleportRevoke))
	s.Mux.HandleFunc("POST /v1/teleports/{id}/accept", s.requireAuth(s.handleTeleportAccept))
	s.Mux.HandleFunc("GET /ops/v1/status", s.requireAuth(s.requirePlatformAdmin(s.handleOpsStatus)))
	s.Mux.HandleFunc("GET /ops/v1/tenants", s.requireAuth(s.requirePlatformAdmin(s.handleOpsTenants)))
	s.Mux.HandleFunc("GET /ops/v1/queues", s.requireAuth(s.requirePlatformAdmin(s.handleOpsQueues)))
	s.Mux.HandleFunc("GET /ops/v1/health", s.requireAuth(s.requirePlatformAdmin(s.handleOpsHealth)))
	s.Mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthASMetadata)
	s.Mux.HandleFunc("GET /v1/agent/mcp/.well-known/oauth-authorization-server", s.handleOAuthASMetadata)
	// P7 expansions (users/plans/flags/releases/suspend) live in ops_routes.go.
	s.registerOpsRoutes()
}

func (s *Server) requireSessionOwner(w http.ResponseWriter, r *http.Request, sessionID string) (*store.AgentSession, store.AgentCloudStore, bool) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return nil, nil, false
	}
	row, err := cs.GetAgentSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return nil, nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}
	if row.OwnerUserID != authSubject(r) {
		writeError(w, http.StatusForbidden, "only the session owner can share it")
		return nil, nil, false
	}
	return row, cs, true
}

func (s *Server) handleBlobPresign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"project_id"`
		Files     []struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		} `json:"files"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizeProject(w, r, body.ProjectID) {
		return
	}
	uploads := make([]map[string]any, 0, len(body.Files))
	for _, f := range body.Files {
		plan, err := blobs.PlanUpload(f.SHA256, f.Size)
		if errors.Is(err, blobs.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "file exceeds 10 GiB")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "sha256 must be 64 hex characters")
			return
		}
		uploads = append(uploads, map[string]any{
			"sha256":    plan.SHA256,
			"method":    "PUT",
			"url":       "/v1/blobs/" + plan.SHA256,
			"mode":      plan.Mode,
			"part_size": plan.PartSize,
			"parts":     plan.Parts,
			"headers": map[string]string{
				"x-amz-checksum-sha256": plan.ChecksumB64,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": uploads})
}

func (s *Server) handleVersionPreflight(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("id"))
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "version must be an integer")
		return
	}
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	row, ok := s.requireAgentRead(w, r, sessionID)
	if !ok {
		return
	}
	caller := authSubject(r)
	var ver *store.SessionVersion
	if row.OwnerUserID == caller {
		// Owners may preflight uploading versions (not yet in the visible set).
		ver, err = getVersion(r, s, sessionID, version)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "version not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		ver, ok = s.requireVisibleVersion(w, r, sessionID, version)
		if !ok {
			return
		}
	}
	refs, _, merr := manifestRefs(ver.Manifest)
	if merr != nil && len(ver.Manifest) > 0 {
		writeError(w, http.StatusBadRequest, merr.Error())
		return
	}
	missing, err := cs.MissingBlobs(r.Context(), row.ProjectID, refs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": ver.Version,
		"state":   ver.State,
		"ready":   len(missing) == 0 && (ver.State == "complete" || ver.State == "transcript_only"),
		"missing": missing,
	})
}

func getVersion(r *http.Request, s *Server, sessionID string, version int) (*store.SessionVersion, error) {
	if m, ok := s.Store.(*store.MemStore); ok {
		return m.GetSessionVersion(r.Context(), sessionID, version)
	}
	if p, ok := s.Store.(*store.PostgresStore); ok {
		return p.GetSessionVersion(r.Context(), sessionID, version)
	}
	return nil, store.ErrNotFound
}

func manifestRefs(raw json.RawMessage) ([]string, int, error) {
	if len(raw) == 0 {
		return []string{}, 0, nil
	}
	var doc struct {
		Transcript struct {
			Blob string `json:"blob"`
		} `json:"transcript"`
		Files []struct {
			Blob string `json:"blob"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, err
	}
	var refs []string
	files := 0
	if h := strings.TrimSpace(doc.Transcript.Blob); h != "" {
		refs = append(refs, strings.TrimPrefix(h, "sha256:"))
	}
	for _, f := range doc.Files {
		if h := strings.TrimSpace(f.Blob); h != "" {
			refs = append(refs, strings.TrimPrefix(h, "sha256:"))
			files++
		}
	}
	if refs == nil {
		refs = []string{}
	}
	return refs, files, nil
}

func (s *Server) handleGrantUser(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	userID := strings.TrimSpace(r.PathValue("user"))
	if _, _, ok := s.requireSessionOwner(w, r, sessionID); !ok {
		return
	}
	cs, _ := s.agentCloud()
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user is required")
		return
	}
	opts := store.SessionGrantOpts{Live: true}
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			var body struct {
				Live      *bool  `json:"live"`
				VersionID string `json:"version_id"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				return
			}
			if body.Live != nil {
				opts.Live = *body.Live
			}
			opts.VersionID = body.VersionID
		}
	}
	if err := cs.GrantAgentSessionOpts(r.Context(), sessionID, userID, authSubject(r), opts); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.grant_added", ResourceKind: "session", ResourceID: sessionID,
		Metadata: map[string]any{"grantee_user_id": userID, "live": opts.Live, "version_id": opts.VersionID},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID, "user_id": userID, "live": opts.Live, "version_id": opts.VersionID,
	})
}

func (s *Server) handleUngrantUser(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	userID := strings.TrimSpace(r.PathValue("user"))
	if _, _, ok := s.requireSessionOwner(w, r, sessionID); !ok {
		return
	}
	if err := revokeGrant(r, s, sessionID, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "grant not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.grant_revoked", ResourceKind: "session", ResourceID: sessionID,
		Metadata: map[string]any{"grantee_user_id": userID},
	})
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "user_id": userID, "revoked": true})
}

func revokeGrant(r *http.Request, s *Server, sessionID, userID string) error {
	if m, ok := s.Store.(*store.MemStore); ok {
		return m.RevokeAgentSessionGrant(r.Context(), sessionID, userID)
	}
	if p, ok := s.Store.(*store.PostgresStore); ok {
		return p.RevokeAgentSessionGrant(r.Context(), sessionID, userID)
	}
	return store.ErrNotFound
}

func setVisibility(r *http.Request, s *Server, sessionID, visibility string) error {
	if m, ok := s.Store.(*store.MemStore); ok {
		return m.SetAgentSessionVisibility(r.Context(), sessionID, visibility)
	}
	if p, ok := s.Store.(*store.PostgresStore); ok {
		return p.SetAgentSessionVisibility(r.Context(), sessionID, visibility)
	}
	return store.ErrNotFound
}

func (s *Server) handleGrantTeam(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	row, _, ok := s.requireSessionOwner(w, r, sessionID)
	if !ok {
		return
	}
	var body struct {
		Confirm bool  `json:"confirm"`
		Live    *bool `json:"live"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "team share needs explicit confirmation")
		return
	}
	// D15: team shares are always live.
	if body.Live != nil && !*body.Live {
		writeError(w, http.StatusBadRequest, "team shares must be live")
		return
	}
	if err := setVisibility(r, s, sessionID, "team"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "session.shared_team", ResourceKind: "session", ResourceID: sessionID,
		ProjectID: row.ProjectID, Metadata: map[string]any{"live": true},
	})
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "visibility": "team", "live": true})
}

func (s *Server) handleUngrantTeam(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, _, ok := s.requireSessionOwner(w, r, sessionID); !ok {
		return
	}
	if err := setVisibility(r, s, sessionID, "private"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "visibility": "private"})
}

func (s *Server) handleStorageUsage(w http.ResponseWriter, r *http.Request) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "user:" + authSubject(r)
	}
	var used, cap int64
	var err error
	switch st := s.Store.(type) {
	case *store.MemStore:
		used, cap, err = st.StorageUsage(r.Context(), scope)
	case *store.PostgresStore:
		used, cap, err = st.StorageUsage(r.Context(), scope)
	default:
		writeError(w, http.StatusNotImplemented, "storage usage unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope": scope,
		"used":  used,
		"cap":   cap,
		"state": capture.UsageState(used, cap),
	})
}

func (s *Server) handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimSpace(r.URL.Query().Get("resource"))
	events, err := listAudit(r, s)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if resource != "" {
		filtered := events[:0]
		for _, ev := range events {
			if ev.ResourceKind == resource || ev.ResourceID == resource || ev.Action == resource {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	if events == nil {
		events = []store.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events, "count": len(events)})
}

func (s *Server) handleTimelineStream(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	items, _, err := cs.ListTimeline(r.Context(), store.TimelineQuery{UserID: authSubject(r), Limit: 30})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	payload, _ := json.Marshal(map[string]any{"items": items})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, "event: timeline\ndata: %s\n\n", payload)
}

func (s *Server) handleSecretDataKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BlobID string `json:"blob_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	blobID := strings.TrimSpace(body.BlobID)
	if blobID == "" {
		writeError(w, http.StatusBadRequest, "blob_id is required")
		return
	}
	box, err := dataKeyBox()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key service unavailable")
		return
	}
	plain, wrapped, err := box.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate data key")
		return
	}
	if err := putSecret(r, s, blobID, authSubject(r), wrapped); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "data key already exists")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "secrets.data_key", ResourceKind: "secret", ResourceID: blobID,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"blob_id":  blobID,
		"data_key": base64.StdEncoding.EncodeToString(plain),
	})
}

func putSecret(r *http.Request, s *Server, blobID, owner string, wrapped []byte) error {
	switch st := s.Store.(type) {
	case *store.MemStore:
		return st.PutSecretKey(r.Context(), blobID, owner, wrapped)
	case *store.PostgresStore:
		return st.PutSecretKey(r.Context(), blobID, owner, wrapped)
	default:
		return errors.New("secret keys unavailable")
	}
}

func loadSecret(r *http.Request, s *Server, blobID string) (string, []byte, []string, error) {
	switch st := s.Store.(type) {
	case *store.MemStore:
		return st.SecretKey(r.Context(), blobID)
	case *store.PostgresStore:
		return st.SecretKey(r.Context(), blobID)
	default:
		return "", nil, nil, store.ErrNotFound
	}
}

func (s *Server) handleSecretDecrypt(w http.ResponseWriter, r *http.Request) {
	blobID := strings.TrimSpace(r.PathValue("blob"))
	owner, wrapped, grants, err := loadSecret(r, s, blobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "secret not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	caller := authSubject(r)
	allowed := caller == owner
	for _, g := range grants {
		if g == caller {
			allowed = true
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "secret value is not granted")
		return
	}
	box, err := dataKeyBox()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key service unavailable")
		return
	}
	plain, err := box.Unwrap(wrapped)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not unwrap data key")
		return
	}
	s.recordAudit(r, store.AuditEvent{
		Action: "secrets.decrypt", ResourceKind: "secret", ResourceID: blobID,
		ActorUserID: caller,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"blob_id":  blobID,
		"data_key": base64.StdEncoding.EncodeToString(plain),
	})
}

func (s *Server) handleSecretGrant(w http.ResponseWriter, r *http.Request) {
	s.secretGrant(w, r, true)
}

func (s *Server) handleSecretUngrant(w http.ResponseWriter, r *http.Request) {
	s.secretGrant(w, r, false)
}

func (s *Server) secretGrant(w http.ResponseWriter, r *http.Request, grant bool) {
	blobID := strings.TrimSpace(r.PathValue("blob"))
	userID := strings.TrimSpace(r.PathValue("user"))
	owner, _, _, err := loadSecret(r, s, blobID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if owner != authSubject(r) {
		writeError(w, http.StatusForbidden, "only the secret owner can change grants")
		return
	}
	var errGrant error
	switch st := s.Store.(type) {
	case *store.MemStore:
		errGrant = st.GrantSecretKey(r.Context(), blobID, userID, grant)
	case *store.PostgresStore:
		errGrant = st.GrantSecretKey(r.Context(), blobID, userID, grant)
	default:
		writeError(w, http.StatusNotImplemented, "secret grants unavailable")
		return
	}
	if errGrant != nil {
		writeError(w, http.StatusBadRequest, errGrant.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blob_id": blobID, "user_id": userID, "granted": grant})
}

func (s *Server) handleSecretAudit(w http.ResponseWriter, r *http.Request) {
	events, err := listAudit(r, s)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var items []store.AuditEvent
	for _, ev := range events {
		if ev.ResourceKind == "secret" || strings.HasPrefix(ev.Action, "secrets.") {
			items = append(items, ev)
		}
	}
	if items == nil {
		items = []store.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	var req continuex.Request
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.requireAgentRead(w, r, req.SessionID); !ok {
		return
	}
	argv, err := continuex.Command(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"op_id":   "op_" + req.SessionID,
		"command": argv,
		"mode":    req.Mode,
	})
}

func (s *Server) handleTeleportSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		ToUserID  string `json:"to_user_id"`
		Home      string `json:"home"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	row, cs, ok := s.requireSessionOwner(w, r, body.SessionID)
	if !ok {
		return
	}
	versions, err := cs.ListVisibleSessionVersions(r.Context(), row.ID, authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(versions) == 0 {
		writeError(w, http.StatusConflict, "teleport requires a completed session version")
		return
	}
	if strings.TrimSpace(body.ToUserID) == "" {
		writeError(w, http.StatusBadRequest, "to_user_id is required")
		return
	}
	preview := teleport.RedactPreview(row.Summary, body.Home)
	tp, err := createTeleport(r, s, body.SessionID, authSubject(r), body.ToUserID, preview)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, tp)
}

func (s *Server) handleTeleportInbox(w http.ResponseWriter, r *http.Request) {
	s.listTeleports(w, r, "inbox")
}

func (s *Server) handleTeleportSent(w http.ResponseWriter, r *http.Request) {
	s.listTeleports(w, r, "sent")
}

func (s *Server) listTeleports(w http.ResponseWriter, r *http.Request, box string) {
	items, err := listTeleportBox(r, s, box)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleTeleportRevoke(w http.ResponseWriter, r *http.Request) {
	s.mutateTeleport(w, r, true)
}

func (s *Server) handleTeleportAccept(w http.ResponseWriter, r *http.Request) {
	s.mutateTeleport(w, r, false)
}

func (s *Server) mutateTeleport(w http.ResponseWriter, r *http.Request, revoke bool) {
	id := r.PathValue("id")
	tp, err := getTeleport(r, s, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "teleport not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	caller := authSubject(r)
	if revoke {
		if tp.FromUserID != caller {
			writeError(w, http.StatusForbidden, "only the sender can revoke")
			return
		}
		if err := setTeleportStatus(r, s, id, "revoked", true); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "revoked"})
		return
	}
	if tp.ToUserID != caller {
		writeError(w, http.StatusForbidden, "only the recipient can accept")
		return
	}
	if err := setTeleportStatus(r, s, id, "accepted", false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "accepted"})
}

func createTeleport(r *http.Request, s *Server, sessionID, fromUser, toUser, preview string) (*store.Teleport, error) {
	switch st := s.Store.(type) {
	case *store.MemStore:
		return st.CreateTeleport(r.Context(), sessionID, fromUser, toUser, preview)
	case *store.PostgresStore:
		return st.CreateTeleport(r.Context(), sessionID, fromUser, toUser, preview)
	default:
		return nil, errors.New("teleport unavailable")
	}
}

func listTeleportBox(r *http.Request, s *Server, box string) ([]store.Teleport, error) {
	switch st := s.Store.(type) {
	case *store.MemStore:
		return st.ListTeleports(r.Context(), authSubject(r), box)
	case *store.PostgresStore:
		return st.ListTeleports(r.Context(), authSubject(r), box)
	default:
		return nil, errors.New("teleport unavailable")
	}
}

func getTeleport(r *http.Request, s *Server, id string) (*store.Teleport, error) {
	switch st := s.Store.(type) {
	case *store.MemStore:
		return st.GetTeleport(r.Context(), id)
	case *store.PostgresStore:
		return st.GetTeleport(r.Context(), id)
	default:
		return nil, store.ErrNotFound
	}
}

func setTeleportStatus(r *http.Request, s *Server, id, status string, revoke bool) error {
	switch st := s.Store.(type) {
	case *store.MemStore:
		return st.SetTeleportStatus(r.Context(), id, status, revoke)
	case *store.PostgresStore:
		return st.SetTeleportStatus(r.Context(), id, status, revoke)
	default:
		return store.ErrNotFound
	}
}

func listAudit(r *http.Request, s *Server) ([]store.AuditEvent, error) {
	if m, ok := s.Store.(*store.MemStore); ok {
		return m.ListAuditEvents(r.Context(), "", "", 100)
	}
	if p, ok := s.Store.(*store.PostgresStore); ok {
		return p.ListAuditEvents(r.Context(), "", "", 100)
	}
	return []store.AuditEvent{}, nil
}

func (s *Server) handleOpsStatus(w http.ResponseWriter, r *http.Request) {
	// Operators see health only. Session titles and bodies are not loaded.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"session_content": false,
	})
}

func (s *Server) handleOpsTenants(w http.ResponseWriter, r *http.Request) {
	// D22: org id + name only. Never session titles or content.
	// suspended comes from P7 ops suspend map (ops_routes.go).
	tenants := []map[string]any{}
	switch st := s.Store.(type) {
	case *store.MemStore:
		for _, o := range st.ListAllOrganizations(r.Context()) {
			row := map[string]any{"id": o.ID, "name": o.Name, "suspended": false}
			if reason, ok := tenantSuspended(o.ID); ok {
				row["suspended"] = true
				row["suspend_reason"] = reason
			}
			tenants = append(tenants, row)
		}
	case *store.PostgresStore:
		for _, o := range st.ListAllOrganizations(r.Context()) {
			row := map[string]any{"id": o.ID, "name": o.Name, "suspended": false}
			if reason, ok := tenantSuspended(o.ID); ok {
				row["suspended"] = true
				row["suspend_reason"] = reason
			}
			tenants = append(tenants, row)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenants":         tenants,
		"session_content": false,
	})
}

func (s *Server) handleOpsQueues(w http.ResponseWriter, r *http.Request) {
	counts := store.HarvestJobCounts{}
	if s.Harvest != nil {
		if c, err := s.Harvest.CountHarvestJobs(r.Context(), ""); err == nil {
			counts = c
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"harvest": map[string]any{
			"queued":     counts.Queued,
			"processing": counts.Processing,
			"done":       counts.Done,
			"failed":     counts.Failed,
			"duplicate":  counts.Duplicate,
			"total":      counts.Total,
		},
		"session_content": false,
	})
}

func (s *Server) handleOpsHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"status":          "ok",
		"session_content": false,
	})
}

// handleOAuthASMetadata is an incomplete RFC 8414 stub for MCP clients.
// It points at existing portal login / GitHub OAuth surfaces; device and
// token issuance for MCP are not finished yet.
func (s *Server) handleOAuthASMetadata(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(publicAPIBase(r), "/")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                           base,
		"authorization_endpoint":           base + "/projects/{id}/github/oauth/start",
		"token_endpoint":                   base + "/auth/tokens",
		"response_types_supported":         []string{"code"},
		"grant_types_supported":            []string{"authorization_code"},
		"code_challenge_methods_supported": []string{"S256"},
		"nexus_status":                     "incomplete",
		"nexus_note":                       "MCP OAuth is scaffolding; use portal API tokens or GitHub OAuth device/link flows until complete.",
		"nexus_github_oauth_status":        base + "/projects/{id}/github/oauth/status",
	})
}
