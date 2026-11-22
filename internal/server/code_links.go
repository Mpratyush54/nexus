package server

// code_links.go — memories linked to code (migration 044).
//
//   - POST /v1/projects/{id}/code-links: the desktop reports the files an
//     agent edited in a session (from Edit/Write/apply_patch tool calls),
//     with content hashes when it has them. Memories already harvested from
//     that session are linked to those files too.
//   - linkMemoryToCode: a memory harvested from a session is linked to its
//     files_affected (decided_about/llm) and to the files the session
//     touched (touched/tool_call).
//   - files-changed hashes (project_brief.go) drive the hash staleness rule.
//   - agentMCPStore.CodeLinksForFile serves related_to / file_history.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/mcp"
	"central-memory/internal/store"
)

// maxMemoryTouchLinks caps the session files linked to one memory.
const maxMemoryTouchLinks = 100

func (s *Server) registerCodeLinkRoutes() {
	s.Mux.HandleFunc("POST /v1/projects/{id}/code-links", s.requireAuth(s.handleCodeLinks))
}

func (s *Server) codeLinkStore() (store.CodeLinkStore, bool) {
	if s == nil || s.Store == nil {
		return nil, false
	}
	cl, ok := s.Store.(store.CodeLinkStore)
	return cl, ok
}

type codeLinkInput struct {
	Path       string  `json:"path"`
	Symbol     string  `json:"symbol,omitempty"`
	Relation   string  `json:"relation,omitempty"`
	Source     string  `json:"source,omitempty"`
	Hash       string  `json:"hash,omitempty"`
	Confidence float32 `json:"confidence,omitempty"`
}

type codeLinksRequest struct {
	SessionID       string          `json:"session_id,omitempty"`
	SessionNativeID string          `json:"session_native_id,omitempty"`
	Harness         string          `json:"harness,omitempty"`
	Links           []codeLinkInput `json:"links"`
}

// resolveCodeLinkSession maps a cloud session id or a harness-native id to
// the project's cloud session. It returns "" when neither resolves.
func (s *Server) resolveCodeLinkSession(ctx context.Context, cl store.CodeLinkStore, projectID, sessionID, nativeID string) (string, *store.AgentSession) {
	sessionID = strings.TrimSpace(sessionID)
	nativeID = strings.TrimSpace(nativeID)
	cs, hasCloud := s.Store.(store.AgentCloudStore)
	get := func(id string) *store.AgentSession {
		if !hasCloud || id == "" {
			return nil
		}
		row, err := cs.GetAgentSession(ctx, id)
		if err != nil || row == nil || row.ProjectID != projectID {
			return nil
		}
		return row
	}
	if row := get(sessionID); row != nil {
		return row.ID, row
	}
	for _, native := range []string{nativeID, sessionID} {
		if native == "" {
			continue
		}
		id, err := cl.FindAgentSessionByNativeID(ctx, projectID, native)
		if err != nil || id == "" {
			continue
		}
		return id, get(id)
	}
	return "", nil
}

// handleCodeLinks records the files an agent touched in one session.
func (s *Server) handleCodeLinks(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, projectID, store.PermMemoryWrite) {
		return
	}
	cl, ok := s.codeLinkStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "code links unavailable")
		return
	}
	var req codeLinksRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Links) == 0 {
		writeError(w, http.StatusBadRequest, "links required")
		return
	}
	if len(req.Links) > store.MaxCodeLinks {
		writeError(w, http.StatusBadRequest, "too many links")
		return
	}
	if strings.TrimSpace(req.SessionID) == "" && strings.TrimSpace(req.SessionNativeID) == "" {
		writeError(w, http.StatusBadRequest, "session_id or session_native_id required")
		return
	}
	ctx := r.Context()
	sid, row := s.resolveCodeLinkSession(ctx, cl, projectID, req.SessionID, req.SessionNativeID)
	if sid == "" {
		// Not mirrored yet: the desktop drops the batch (409 is not retried
		// as a missing route).
		writeError(w, http.StatusConflict, "session not found in this project")
		return
	}
	if cs, ok := s.Store.(store.AgentCloudStore); ok && row != nil {
		can, err := cs.CanReadAgentSession(ctx, authSubject(r), sid)
		if err != nil || !can {
			writeError(w, http.StatusForbidden, "session not readable")
			return
		}
	}

	type linked struct {
		entityID, hash string
	}
	var touched []linked
	added, skipped := 0, 0
	for _, in := range req.Links {
		path, ok := store.CleanCodePath(in.Path)
		if !ok {
			skipped++
			continue
		}
		kind := store.CodeKindFile
		if strings.TrimSpace(in.Symbol) != "" {
			kind = store.CodeKindSymbol
		}
		relation := strings.ToLower(strings.TrimSpace(in.Relation))
		if relation == "" {
			relation = store.RelationTouched
		}
		source := strings.ToLower(strings.TrimSpace(in.Source))
		if source == "" {
			source = store.LinkSourceToolCall
		}
		e, err := cl.UpsertCodeEntity(ctx, projectID, kind, path, in.Symbol)
		if err != nil {
			skipped++
			continue
		}
		hash := strings.TrimSpace(in.Hash)
		if hash != "" && kind == store.CodeKindFile {
			_, _ = cl.SetCodeEntityHash(ctx, projectID, e.Path, hash, time.Now().UTC())
		}
		link := &store.EntityLink{
			ProjectID: projectID, EntityID: e.ID, SrcKind: store.LinkSrcSession, SrcID: sid,
			Relation: relation, Source: source, Confidence: in.Confidence, ObservedHash: hash,
		}
		if err := cl.AddEntityLink(ctx, link); err != nil {
			if errors.Is(err, store.ErrBadCodeLink) {
				skipped++
				continue
			}
			writeError(w, http.StatusInternalServerError, "link failed: "+err.Error())
			return
		}
		added++
		if relation == store.RelationTouched {
			touched = append(touched, linked{entityID: e.ID, hash: link.ObservedHash})
		}
	}

	// Memories already harvested from this session get the new files.
	memLinks := 0
	if len(touched) > 0 {
		mems, err := s.Store.SearchMemory(ctx, projectID, "", []string{"session:" + sid}, 200)
		if err == nil {
			for _, m := range mems {
				if m == nil || m.ProjectID != projectID {
					continue
				}
				for _, t := range touched {
					if cl.AddEntityLink(ctx, &store.EntityLink{
						ProjectID: projectID, EntityID: t.entityID, SrcKind: store.LinkSrcMemory, SrcID: m.ID,
						Relation: store.RelationTouched, Source: store.LinkSourceToolCall, Confidence: 1,
						ObservedHash: t.hash,
					}) == nil {
						memLinks++
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sid, "linked": added, "skipped": skipped, "memory_links": memLinks,
	})
}

// codePathForMemoryFile maps one files_affected entry to an entity path:
// a path the session touched that it names (suffix match) wins, then the
// entry made relative to the session's workspace root, then the entry as
// given when it is already repo-relative. Absolute paths that match neither
// are dropped.
func codePathForMemoryFile(f, root string, touched []store.CodeLinkHit) (string, bool) {
	f = strings.TrimSpace(strings.ReplaceAll(f, `\`, "/"))
	if f == "" {
		return "", false
	}
	for _, t := range touched {
		if store.FilePathMatches(t.Path, f) {
			return t.Path, true
		}
	}
	if root = strings.TrimSuffix(strings.TrimSpace(strings.ReplaceAll(root, `\`, "/")), "/"); root != "" {
		if len(f) > len(root)+1 && strings.EqualFold(f[:len(root)], root) && f[len(root)] == '/' {
			f = f[len(root)+1:]
		}
	}
	return store.CleanCodePath(f)
}

// linkMemoryToCode links a memory harvested from sessionID to its
// files_affected (decided_about/llm) and to the files that session touched
// (touched/tool_call). Best-effort: it never fails the harvest.
func (s *Server) linkMemoryToCode(ctx context.Context, item *store.MemoryItem, sessionID string) {
	cl, ok := s.codeLinkStore()
	if !ok || item == nil || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.ProjectID) == "" {
		return
	}
	projectID := item.ProjectID
	sid, row := "", (*store.AgentSession)(nil)
	if strings.TrimSpace(sessionID) != "" {
		sid, row = s.resolveCodeLinkSession(ctx, cl, projectID, sessionID, sessionID)
	}
	var touched []store.CodeLinkHit
	if sid != "" {
		if hits, err := cl.ListCodeLinksForSource(ctx, projectID, store.LinkSrcSession, sid); err == nil {
			for _, h := range hits {
				if h.Relation == store.RelationTouched {
					touched = append(touched, h)
				}
			}
		}
	}
	root := ""
	if row != nil {
		root = row.WorkspaceRootHint
	}
	conf := item.Confidence
	for _, f := range item.FilesAffected {
		path, ok := codePathForMemoryFile(f, root, touched)
		if !ok {
			continue
		}
		e, err := cl.UpsertCodeEntity(ctx, projectID, store.CodeKindFile, path, "")
		if err != nil {
			continue
		}
		if err := cl.AddEntityLink(ctx, &store.EntityLink{
			ProjectID: projectID, EntityID: e.ID, SrcKind: store.LinkSrcMemory, SrcID: item.ID,
			Relation: store.RelationDecidedAbout, Source: store.LinkSourceLLM, Confidence: conf,
			ObservedHash: e.ContentHash,
		}); err != nil && s.Log != nil {
			s.Log.Printf("code links: memory=%s path=%s: %v", item.ID, path, err)
		}
	}
	for i, t := range touched {
		if i >= maxMemoryTouchLinks {
			break
		}
		_ = cl.AddEntityLink(ctx, &store.EntityLink{
			ProjectID: projectID, EntityID: t.EntityID, SrcKind: store.LinkSrcMemory, SrcID: item.ID,
			Relation: store.RelationTouched, Source: store.LinkSourceToolCall, Confidence: 1,
			ObservedHash: t.ObservedHash,
		})
	}
}

// ---- cloud MCP adapter ----

// CodeLinksForFile serves related_to / file_history (mcp.CodeLinkReader).
// Memories are filtered as the agent's user; sessions need read access.
func (a agentMCPStore) CodeLinksForFile(ctx context.Context, projectID, path string, limit int) ([]mcp.CodeLink, error) {
	cl, ok := a.Store.(store.CodeLinkStore)
	if !ok {
		return nil, mcp.ErrCodeLinksUnavailable
	}
	hits, err := cl.ListCodeLinksByPath(a.viewerCtx(ctx), projectID, path, limit)
	if err != nil {
		return nil, err
	}
	cs, hasCloud := a.Store.(store.AgentCloudStore)
	readable := map[string]bool{}
	canRead := func(id string) bool {
		if v, ok := readable[id]; ok {
			return v
		}
		ok := false
		if hasCloud {
			ok, _ = cs.CanReadAgentSession(ctx, a.UserID, id)
		}
		readable[id] = ok
		return ok
	}
	out := make([]mcp.CodeLink, 0, len(hits))
	for _, h := range hits {
		l := mcp.CodeLink{
			Path: h.Path, Relation: h.Relation, Source: h.Source, Confidence: h.Confidence,
			LinkedAt: h.LinkedAt, CodeChanged: h.CodeChanged(),
		}
		switch {
		case h.Memory != nil:
			l.Memory = storeToMCPMemory(h.Memory)
		case h.Session != nil:
			if !canRead(h.Session.ID) {
				continue
			}
			l.Session = &mcp.SessionRef{
				ID: h.Session.ID, Title: h.Session.Title, Harness: h.Session.Harness,
				StartedAt: h.Session.StartedAt, LastActiveAt: h.Session.LastActiveAt,
			}
		default:
			continue
		}
		out = append(out, l)
	}
	return out, nil
}
