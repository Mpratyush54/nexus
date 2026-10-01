package server

import (
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
	s.Mux.HandleFunc("GET /v1/projects/{id}/knowledge", s.requireAuth(s.handleProjectKnowledge))
}

// handleAgentSessionFetch returns one page of raw turns. ListSessionTurns
// returns the whole session, so the page is applied here. The cursor is the
// last idx already returned, as a decimal string; turns with idx <= cursor
// are omitted. next_cursor is that idx when another page remains.
func (s *Server) handleAgentSessionFetch(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if _, ok := s.requireAgentRead(w, r, sessionID); !ok {
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
	cs, ok := s.agentCloud()
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent sessions unavailable")
		return
	}
	turns, err := cs.ListSessionTurns(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items, next := pageSessionTurns(turns, after, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": next,
	})
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

// handleAgentSessionSummary returns the summary for a session the caller
// can read. Owner and grantee pass; project or org admin does not.
func (s *Server) handleAgentSessionSummary(w http.ResponseWriter, r *http.Request) {
	row, ok := s.requireAgentRead(w, r, strings.TrimSpace(r.PathValue("id")))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": row.Summary})
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
	viewerCtx := store.WithViewer(r.Context(), authSubject(r))
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
