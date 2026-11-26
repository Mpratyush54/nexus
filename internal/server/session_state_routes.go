package server

// Per-person chat state: rename, pin and archive.
//
//	PATCH /v1/agent-sessions/{id}/state  {"title"?: string, "pinned"?: bool, "archived"?: bool}
//	  -> {session_id, title, pinned, pinned_at, archived, archived_at}
//
// The state is the caller's own: anyone who can read the chat (its owner,
// people it is shared with, the owner of an archived snapshot) can rename,
// pin or archive it for themselves; nobody else sees the change. An empty
// title clears the rename. Pinning un-archives and archiving unpins.
//
// GET /v1/timeline shows the caller's titles and marks items pinned,
// archived and renamed. Archived chats are hidden unless archived=include
// (all) or archived=only (just them); pinned=only lists just pinned chats.
// The two "only" lists are built from the state rows, newest mark first,
// on one page (no cursor).

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"central-memory/internal/store"
)

// Most chats an "only" list returns.
const (
	timelinePinnedMax   = 50
	timelineArchivedMax = 200
)

func (s *Server) registerSessionStateRoutes() {
	s.Mux.HandleFunc("PATCH /v1/agent-sessions/{id}/state", s.requireAuth(s.handleAgentSessionState))
}

func (s *Server) sessionUserStateStore() (store.SessionUserStateStore, bool) {
	st, ok := s.Store.(store.SessionUserStateStore)
	return st, ok
}

// sessionStateJSON is the PATCH response.
func sessionStateJSON(st store.SessionUserState) map[string]any {
	return map[string]any{
		"session_id":  st.SessionID,
		"title":       st.Title,
		"pinned":      st.PinnedAt != nil,
		"pinned_at":   st.PinnedAt,
		"archived":    st.ArchivedAt != nil,
		"archived_at": st.ArchivedAt,
	}
}

func (s *Server) handleAgentSessionState(w http.ResponseWriter, r *http.Request) {
	states, ok := s.sessionUserStateStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "chat pin, archive and rename are unavailable")
		return
	}
	var body struct {
		Title    *string `json:"title"`
		Pinned   *bool   `json:"pinned"`
		Archived *bool   `json:"archived"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Title == nil && body.Pinned == nil && body.Archived == nil {
		writeError(w, http.StatusBadRequest, "send title, pinned or archived")
		return
	}
	if body.Pinned != nil && body.Archived != nil && *body.Pinned && *body.Archived {
		writeError(w, http.StatusBadRequest, "a chat cannot be pinned and archived at once")
		return
	}
	row, ok := s.requireAgentRead(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	st, err := states.SetSessionUserState(r.Context(), authSubject(r), row.ID, store.SessionUserStatePatch{
		Title: body.Title, Pinned: body.Pinned, Archived: body.Archived,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the chat's state")
		return
	}
	writeJSON(w, http.StatusOK, sessionStateJSON(*st))
}

// loadSessionUserStates returns the caller's chat states, or nil when the
// store keeps none.
func (s *Server) loadSessionUserStates(ctx context.Context, userID string) (map[string]store.SessionUserState, error) {
	states, ok := s.sessionUserStateStore()
	if !ok {
		return nil, nil
	}
	return states.ListSessionUserStates(ctx, userID)
}

// applySessionUserState overlays the caller's state on a timeline item.
func applySessionUserState(item *store.TimelineItem, st store.SessionUserState, ok bool) {
	if !ok {
		return
	}
	if st.Title != "" {
		item.Title = st.Title
		item.Renamed = true
	}
	item.Pinned = st.PinnedAt != nil
	item.Archived = st.ArchivedAt != nil
}

// Values of the timeline's archived parameter.
const (
	timelineArchivedHide    = ""
	timelineArchivedInclude = "include"
	timelineArchivedOnly    = "only"
)

func parseTimelineArchived(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "exclude", "hide", "0", "false", "no":
		return timelineArchivedHide, true
	case "include", "all":
		return timelineArchivedInclude, true
	case "only", "1", "true", "yes":
		return timelineArchivedOnly, true
	}
	return "", false
}

func parseTimelinePinnedOnly(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "include", "all", "0", "false", "no":
		return false, true
	case "only", "1", "true", "yes":
		return true, true
	}
	return false, false
}

// filterTimelineByState overlays states on items and drops archived chats
// unless archivedMode includes them.
func filterTimelineByState(items []store.TimelineItem, states map[string]store.SessionUserState, archivedMode string) []store.TimelineItem {
	if len(states) == 0 {
		return items
	}
	kept := items[:0]
	for _, item := range items {
		st, ok := states[item.SessionID]
		applySessionUserState(&item, st, ok)
		if item.Archived && archivedMode == timelineArchivedHide {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

// handleTimelineStateOnly answers pinned=only or archived=only from the
// caller's state rows: each chat is loaded and read-checked like
// GET /v1/agent-sessions/{id}; chats deleted since or no longer readable
// are skipped. project_id, harness and q (case-insensitive, on the shown
// title and summary) filter the list.
func (s *Server) handleTimelineStateOnly(w http.ResponseWriter, r *http.Request, cs store.AgentCloudStore, projectID, harness, query string, pinned bool) {
	userID := authSubject(r)
	states, err := s.loadSessionUserStates(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load pinned and archived chats")
		return
	}
	type mark struct {
		st store.SessionUserState
		at time.Time
	}
	marks := make([]mark, 0, len(states))
	for _, st := range states {
		at := st.ArchivedAt
		if pinned {
			at = st.PinnedAt
		}
		if at != nil {
			marks = append(marks, mark{st, *at})
		}
	}
	sort.Slice(marks, func(i, j int) bool {
		return timelineNewer(marks[i].at, marks[i].st.SessionID, marks[j].at, marks[j].st.SessionID)
	})
	most := timelineArchivedMax
	if pinned {
		most = timelinePinnedMax
	}
	lowerQuery := strings.ToLower(query)
	items := []store.TimelineItem{}
	var modern []string
	for _, m := range marks {
		if len(items) >= most {
			break
		}
		item, ok := s.stateTimelineItem(r.Context(), cs, userID, m.st.SessionID)
		if !ok {
			continue
		}
		if projectID != "" && item.ProjectID != projectID {
			continue
		}
		if harness != "" && !strings.EqualFold(item.Harness, harness) {
			continue
		}
		applySessionUserState(&item, m.st, true)
		if lowerQuery != "" && !strings.Contains(strings.ToLower(item.Title+"\n"+item.Summary), lowerQuery) {
			continue
		}
		if item.VersionState != "legacy_snapshot" {
			modern = append(modern, item.SessionID)
		}
		items = append(items, item)
	}
	if len(modern) > 0 {
		if states, ok := s.sessionUserStateStore(); ok {
			if counts, err := states.SessionTurnCounts(r.Context(), modern); err == nil {
				for i := range items {
					if n, ok := counts[items[i].SessionID]; ok {
						items[i].TurnCount = n
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": "", "turn_counts": "exact"})
}

// stateTimelineItem builds the timeline card of one chat the caller can
// read: an agent session, or an archived snapshot. TurnCount is left for
// the caller to fill for agent sessions.
func (s *Server) stateTimelineItem(ctx context.Context, cs store.AgentCloudStore, userID, sessionID string) (store.TimelineItem, bool) {
	row, err := cs.GetAgentSession(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return store.TimelineItem{}, false
		}
		snap := s.readableSnapshotMeta(ctx, userID, sessionID)
		if snap == nil {
			return store.TimelineItem{}, false
		}
		return legacyTimelineItem(snap), true
	}
	if allowed, err := cs.CanReadAgentSession(ctx, userID, row.ID); err != nil || !allowed {
		return store.TimelineItem{}, false
	}
	live, err := cs.CanReadAgentSessionLive(ctx, userID, row.ID)
	if err != nil {
		return store.TimelineItem{}, false
	}
	item := store.TimelineItem{
		SessionID: row.ID, ProjectID: row.ProjectID, Harness: row.Harness, NativeID: row.NativeID,
		Title: row.Title, Visibility: row.Visibility, OwnerUserID: row.OwnerUserID,
		LastActiveAt: row.LastActiveAt, ParentSessionID: row.ParentSessionID,
	}
	if row.LineageKind == "subagent" {
		item.LineageKind = row.LineageKind
	}
	if live {
		item.Summary = row.Summary
		if store.IsPlaceholderTitle(row.Title) {
			if src, ok := s.Store.(store.SessionTitleSource); ok {
				if turns, err := src.FirstUserTurns(ctx, row.ID); err == nil {
					item.Title = store.PresentTitle(row.Title, turns)
				}
			}
		}
	}
	return item, true
}

// readableSnapshotMeta returns the latest archived snapshot of sessionID
// (metadata only) when the caller may read it.
func (s *Server) readableSnapshotMeta(ctx context.Context, userID, sessionID string) *store.SessionSnapshot {
	ps := provenanceStore(s.Store)
	if ps == nil {
		return nil
	}
	var snap *store.SessionSnapshot
	var err error
	if lts, ok := s.Store.(store.LegacyTimelineStore); ok {
		snap, err = lts.GetLatestSnapshotMeta(ctx, sessionID)
	} else {
		snap, err = ps.GetLatestSnapshot(ctx, sessionID)
	}
	if err != nil || !s.canReadSnapshot(ctx, userID, snap) {
		return nil
	}
	return snap
}

// agentSessionView is GET /v1/agent-sessions/{id}: the chat with the
// caller's own state. The extra fields are omitted when unset, so the
// response is unchanged for anyone who has not renamed, pinned or archived
// the chat.
type agentSessionView struct {
	*store.AgentSession
	Pinned   bool `json:"pinned,omitempty"`
	Archived bool `json:"archived,omitempty"`
	Renamed  bool `json:"renamed,omitempty"`
}

// agentSessionWithState overlays the caller's state on row.
func (s *Server) agentSessionWithState(ctx context.Context, userID string, row *store.AgentSession) any {
	states, err := s.loadSessionUserStates(ctx, userID)
	if err != nil || states == nil {
		return row
	}
	st, ok := states[row.ID]
	if !ok {
		return row
	}
	view := agentSessionView{AgentSession: row, Pinned: st.PinnedAt != nil, Archived: st.ArchivedAt != nil}
	if st.Title != "" {
		shown := *row
		shown.Title = st.Title
		view.AgentSession = &shown
		view.Renamed = true
	}
	return view
}
