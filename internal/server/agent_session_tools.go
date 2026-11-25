package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"central-memory/internal/store"
)

// sessionToolRoutes marks muxes that already have the spec 2.3 session
// tool routes, so registerAgentSessionToolRoutes can be called from
// registerRoutes and from tests without a duplicate-pattern panic.
var sessionToolRoutes sync.Map

// registerAgentSessionToolRoutes mounts the remote MCP reads from spec 2.3:
// paged raw turns, the session summary, and top-N project knowledge.
// Wire it from registerRoutes. Pin, forget, and scope stay on other routes.
func (s *Server) registerAgentSessionToolRoutes() {
	if s == nil || s.Mux == nil {
		return
	}
	if _, loaded := sessionToolRoutes.LoadOrStore(s.Mux, struct{}{}); loaded {
		return
	}
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/fetch", s.requireAuth(s.handleAgentSessionFetch))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/summary", s.requireAuth(s.handleAgentSessionSummary))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/files", s.requireAuth(s.handleAgentSessionFiles))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/operations", s.requireAuth(s.handleAgentSessionOperations))
	s.Mux.HandleFunc("GET /v1/agent-sessions/{id}/memories", s.requireAuth(s.handleAgentSessionMemories))
	s.Mux.HandleFunc("GET /v1/projects/{id}/knowledge", s.requireAuth(s.handleProjectKnowledge))
}

// handleAgentSessionFetch returns one page of raw turns. ListSessionTurns
// returns the whole session, so the page is applied here. The cursor is the
// last idx already returned, as a decimal string; turns with idx <= cursor
// are omitted. next_cursor is that idx when another page remains.
// direction=backward pages newest-first (cursor is an exclusive upper idx)
// and q filters turns by text or tool calls; see the branch below.
func (s *Server) handleAgentSessionFetch(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("id"))
	row, ok := s.requireAgentLive(w, r, sessionID)
	if !ok {
		return
	}
	limit := clampSearchLimit(r.URL.Query().Get("limit"), w)
	if limit < 0 {
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	var after *int
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cursor must be a decimal turn index")
			return
		}
		after = &n
	}
	direction := r.URL.Query().Get("direction")
	if direction != "" && direction != "forward" && direction != "backward" {
		writeError(w, http.StatusBadRequest, "direction must be forward or backward")
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(query) > 512 {
		writeError(w, http.StatusBadRequest, "search query must be at most 512 bytes")
		return
	}
	var items []store.SessionTurn
	var next string
	if direction == "backward" || query != "" {
		// Newest-first pages and in-chat search need the whole session.
		// direction=backward: cursor is an exclusive upper idx. With q,
		// matches page forward from the cursor (last idx returned).
		var turns []store.SessionTurn
		if row.LineageKind == "legacy_snapshot" {
			turns = s.legacySnapshotTurns(r.Context(), row)
		} else {
			cs, ok := s.agentCloud()
			if !ok {
				writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
				return
			}
			var err error
			turns, err = cs.ListSessionTurns(r.Context(), sessionID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if query != "" {
			matches := make([]store.SessionTurn, 0)
			for _, turn := range turns {
				if strings.Contains(strings.ToLower(turn.TextPreview), query) || strings.Contains(strings.ToLower(string(turn.ToolCalls)), query) {
					matches = append(matches, turn)
				}
			}
			turns = matches
		}
		if direction == "backward" {
			items, next = pageSessionTurnsBefore(turns, after, limit)
		} else {
			items, next = pageSessionTurns(turns, after, limit)
		}
	} else if row.LineageKind == "legacy_snapshot" {
		// Archived sessions have no session_turns rows; serve the redacted
		// transcript stored in their snapshot so Chat is never empty.
		items, next = pageSessionTurns(s.legacySnapshotTurns(r.Context(), row), after, limit)
	} else {
		cs, ok := s.agentCloud()
		if !ok {
			writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
			return
		}
		var err error
		items, next, err = pageAgentSessionTurns(r.Context(), cs, sessionID, after, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Some historical indexes exceed JavaScript's exact-integer range. Keep
	// the numeric idx for existing clients and add a lossless string key.
	// The browser also reads cursor_idx as "this server pages backward and
	// filters ?q=", so it stops paging whole transcripts locally.
	type browserTurn struct {
		store.SessionTurn
		CursorIndex string `json:"cursor_idx"`
	}
	browserItems := make([]browserTurn, 0, len(items))
	for _, turn := range items {
		browserItems = append(browserItems, browserTurn{SessionTurn: turn, CursorIndex: strconv.Itoa(turn.Idx)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       browserItems,
		"next_cursor": next,
	})
}

// pageAgentSessionTurns reads one page of a session's turns. Stores with a
// keyset reader return just the page; others load the session and page it.
func pageAgentSessionTurns(ctx context.Context, cs store.AgentCloudStore, sessionID string, after *int, limit int) ([]store.SessionTurn, string, error) {
	if reader, ok := cs.(store.SessionTurnReader); ok {
		page, more, err := reader.ListSessionTurnsPage(ctx, sessionID, after, limit)
		if err != nil {
			return nil, "", err
		}
		next := ""
		if more && len(page) > 0 {
			next = strconv.Itoa(page[len(page)-1].Idx)
		}
		return page, next, nil
	}
	turns, err := cs.ListSessionTurns(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	page, next := pageSessionTurns(turns, after, limit)
	return page, next, nil
}

// pageSessionTurns keeps turns in idx order, drops idx <= cursor, and
// returns at most limit of them. next is the last idx on this page when
// further turns remain, otherwise empty.
func pageSessionTurns(turns []store.SessionTurn, after *int, limit int) ([]store.SessionTurn, string) {
	sorted := append([]store.SessionTurn(nil), turns...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Idx < sorted[j].Idx
	})
	filtered := make([]store.SessionTurn, 0, len(sorted))
	for _, turn := range sorted {
		if after != nil && turn.Idx <= *after {
			continue
		}
		filtered = append(filtered, turn)
	}
	if limit > len(filtered) {
		limit = len(filtered)
	}
	page := filtered[:limit]
	if page == nil {
		page = []store.SessionTurn{}
	}
	next := ""
	if len(page) > 0 && len(filtered) > len(page) {
		next = strconv.Itoa(page[len(page)-1].Idx)
	}
	return page, next
}

// pageSessionTurnsBefore returns the newest limit turns below the exclusive
// upper bound before, in ascending idx order. next is the lowest idx on the
// page when older turns remain.
func pageSessionTurnsBefore(turns []store.SessionTurn, before *int, limit int) ([]store.SessionTurn, string) {
	filtered := make([]store.SessionTurn, 0, len(turns))
	for _, turn := range turns {
		if before == nil || turn.Idx < *before {
			filtered = append(filtered, turn)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Idx < filtered[j].Idx })
	next := ""
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
		next = strconv.Itoa(filtered[0].Idx)
	}
	return filtered, next
}

// handleAgentSessionSummary returns the summary for a session the caller
// can read. Owner and grantee pass; project or org admin does not.
func (s *Server) handleAgentSessionSummary(w http.ResponseWriter, r *http.Request) {
	row, ok := s.requireAgentRead(w, r, strings.TrimSpace(r.PathValue("id")))
	if !ok {
		return
	}
	if row.LineageKind != "legacy_snapshot" {
		if cs, ok := s.agentCloud(); ok {
			live, err := cs.CanReadAgentSessionLive(r.Context(), authSubject(r), row.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not check live access")
				return
			}
			if !live {
				// Stored and extracted summaries may describe turns created after
				// the shared version. They are live content, not pinned metadata.
				writeJSON(w, http.StatusOK, map[string]any{"summary": "", "version_limited": true})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": s.sessionSummaryText(r.Context(), row)})
}

// sessionSummaryText is a readable session's summary: the stored summary,
// else the harvest worker's session/<id> memory, else an extractive overview
// of the captured turns (first request and latest agent reply). The caller
// has already checked that the session is readable. Shared by GET
// /v1/agent-sessions/{id}/summary and the MCP session_summary_get tool.
func (s *Server) sessionSummaryText(ctx context.Context, row *store.AgentSession) string {
	if row == nil {
		return ""
	}
	summary := strings.TrimSpace(row.Summary)
	if row.LineageKind == "legacy_snapshot" && summary == "Archived snapshot" {
		// Placeholder text, not a real summary: fall through to the memory
		// lookup and transcript overview below.
		summary = ""
	}
	if summary == "" {
		// The harvest worker stores its processed summary as a project memory
		// keyed session/<id> (the session_id column stays NULL: those rows are
		// level 'project'). Prefer that over the extractive fallback so the
		// Summary tab shows the processed memory once extraction has run.
		for _, sid := range []string{row.NativeID, row.ID} {
			sid = strings.TrimSpace(sid)
			if sid == "" {
				continue
			}
			if item := s.findMemoryByKey(ctx, row.ProjectID, "session/"+sid); item != nil {
				summary = strings.TrimSpace(item.Content)
				break
			}
		}
	}
	if summary == "" {
		var turns []store.SessionTurn
		if row.LineageKind == "legacy_snapshot" {
			turns = s.legacySnapshotTurns(ctx, row)
		} else if reader, ok := s.Store.(store.SessionTurnReader); ok {
			// The overview needs two turns, not the whole session.
			if first, last, err := reader.SessionTurnOverview(ctx, row.ID); err == nil {
				turns = []store.SessionTurn{first, last}
			}
		} else if cs, ok := s.agentCloud(); ok {
			if found, err := cs.ListSessionTurns(ctx, row.ID); err == nil {
				turns = found
			}
		}
		summary = capturedTurnOverview(turns)
	}
	return summary
}

// An extractive fallback is more honest and useful than an empty Summary tab.
// It is explicitly labelled so it is never confused with a processed memory.
func capturedTurnOverview(turns []store.SessionTurn) string {
	var request, response string
	for _, turn := range turns {
		text := strings.Join(strings.Fields(turn.TextPreview), " ")
		if text == "" {
			continue
		}
		if chars := []rune(text); len(chars) > 480 {
			text = string(chars[:480]) + "…"
		}
		switch strings.ToLower(turn.Role) {
		case "user":
			if request == "" {
				request = text
			}
		case "assistant":
			response = text
		}
	}
	if request == "" && response == "" {
		return ""
	}
	parts := []string{"Conversation overview · from captured chat"}
	if request != "" {
		parts = append(parts, "Request: "+request)
	}
	if response != "" {
		parts = append(parts, "Latest agent response: "+response)
	}
	return strings.Join(parts, "\n\n")
}

// knowledgeItem is the project-knowledge row agents see. Status is the
// public lifecycle word, never the stored PROPOSED/CONFIRMED column.
type knowledgeItem struct {
	ID         string   `json:"id"`
	Key        string   `json:"key"`
	Content    string   `json:"content"`
	Level      string   `json:"level"`
	Scope      string   `json:"scope,omitempty"`
	Status     string   `json:"status"`
	Confidence float32  `json:"confidence"`
	Tags       []string `json:"tags,omitempty"`
	Source     string   `json:"source,omitempty"`
}

// handleProjectKnowledge returns the top project and organization memories
// a member can already see through SearchMemory. Personal and session rows
// are left out. limit caps the page after that filter.
func (s *Server) handleProjectKnowledge(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, projectID) {
		return
	}
	limit := clampSearchLimit(r.URL.Query().Get("limit"), w)
	if limit < 0 {
		return
	}
	viewerCtx := store.WithoutEmbeddings(store.WithViewer(r.Context(), authSubject(r)))
	found, err := s.Store.SearchMemory(viewerCtx, projectID, "", nil, MaxSearchLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}
	items := make([]knowledgeItem, 0, len(found))
	for _, item := range found {
		if item == nil || !projectKnowledgeLevel(item.Level) {
			continue
		}
		items = append(items, knowledgeItem{
			ID:         item.ID,
			Key:        item.Key,
			Content:    item.Content,
			Level:      item.Level,
			Scope:      item.Scope,
			Status:     publicMemoryStatus(item.Status),
			Confidence: item.Confidence,
			Tags:       item.Tags,
			Source:     item.Source,
		})
		if len(items) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

func projectKnowledgeLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case store.LevelProject, store.LevelOrganization:
		return true
	default:
		return false
	}
}

// publicMemoryStatus maps the stored lifecycle onto the public words from
// spec 2.2. Callers must send this value, not the stored column.
func publicMemoryStatus(stored string) string {
	switch strings.ToUpper(strings.TrimSpace(stored)) {
	case store.StatusProposed, store.StatusConfirmed:
		return "active"
	case store.StatusRejected:
		return "forgotten"
	case store.StatusSuperseded:
		return "superseded"
	default:
		lower := strings.ToLower(strings.TrimSpace(stored))
		upper := strings.ToUpper(lower)
		if strings.Contains(upper, store.StatusProposed) || strings.Contains(upper, store.StatusConfirmed) {
			return "active"
		}
		return lower
	}
}
