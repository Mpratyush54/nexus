package server

import (
	"net/http"
	"strings"
	"time"

	"central-memory/internal/store"
)

func (s *Server) registerDashboardRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/dashboard", s.requireAuth(s.handleProjectDashboard))
}

func (s *Server) handleProjectDashboard(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if !s.authorizeProject(w, r, id) {
		return
	}

	now := time.Now().UTC()
	since := now.AddDate(0, 0, -364) // ~52 weeks

	events, err := s.Store.ListEvents(r.Context(), id, 0, store.MaxEventsLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load events: "+err.Error())
		return
	}

	dayCounts := map[string]int{}
	byType := map[string]int{}
	var weekEvents, agentsHints int
	for _, ev := range events {
		if ev == nil {
			continue
		}
		day := ev.CreatedAt.UTC().Format("2006-01-02")
		if !ev.CreatedAt.Before(since) {
			dayCounts[day]++
		}
		if ev.CreatedAt.After(now.AddDate(0, 0, -7)) {
			weekEvents++
		}
		byType[ev.EventType]++
		if ev.EventType == "MCP_TOOL_CALL" || strings.TrimSpace(ev.AgentID) != "" {
			agentsHints++
		}
	}

	heatmap := make([]map[string]any, 0, 371)
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		heatmap = append(heatmap, map[string]any{"date": key, "count": dayCounts[key]})
	}

	typeRows := make([]map[string]any, 0, len(byType))
	for t, n := range byType {
		typeRows = append(typeRows, map[string]any{"type": t, "count": n})
	}
	// sort by count desc
	for i := 0; i < len(typeRows); i++ {
		for j := i + 1; j < len(typeRows); j++ {
			if typeRows[j]["count"].(int) > typeRows[i]["count"].(int) {
				typeRows[i], typeRows[j] = typeRows[j], typeRows[i]
			}
		}
	}
	if len(typeRows) > 8 {
		typeRows = typeRows[:8]
	}

	memories := 0
	proposed := 0
	confirmed := 0
	if items, serr := s.Store.SearchMemory(r.Context(), id, "", nil, 500); serr == nil {
		for _, m := range items {
			if m == nil {
				continue
			}
			memories++
			switch m.Status {
			case "PROPOSED":
				proposed++
			case "CONFIRMED":
				confirmed++
			}
		}
	}

	members := 0
	if ids, merr := s.Store.ListMembers(r.Context(), id); merr == nil {
		members = len(ids)
	}

	online := 0
	s.steerMu.RLock()
	h := s.hub
	s.steerMu.RUnlock()
	for range h.PresenceForProject(id) {
		online++
	}
	if online == 0 {
		online = 1 // caller is always online for the tile
	}

	githubConnected := false
	githubRepo := ""
	if gs, ok := s.githubStore(); ok {
		if link, gerr := gs.GetGitHubLink(r.Context(), id); gerr == nil && link != nil {
			githubConnected = true
			githubRepo = link.Owner + "/" + link.Repo
		}
	}

	recent := make([]*store.Event, 0, 12)
	for i := len(events) - 1; i >= 0 && len(recent) < 12; i-- {
		if events[i] != nil {
			recent = append(recent, events[i])
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": id,
		"metrics": map[string]any{
			"memories":         memories,
			"proposed":         proposed,
			"confirmed":        confirmed,
			"members":          members,
			"online":           online,
			"events_7d":        weekEvents,
			"agent_calls":      agentsHints,
			"github_connected": githubConnected,
			"github_repo":      githubRepo,
		},
		"heatmap": heatmap,
		"by_type": typeRows,
		"recent":  recent,
	})
}
