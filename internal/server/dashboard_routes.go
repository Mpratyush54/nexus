package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/internal/store"
)

func (s *Server) registerDashboardRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/dashboard", s.requireAuth(s.handleProjectDashboard))
}

// dashboardBudget bounds one dashboard build. Clients give up after 20 s;
// a section still running at the budget is left out (named in
// "unavailable") instead of failing the whole answer.
var dashboardBudget = 6 * time.Second

// handleProjectDashboard answers GET /projects/{id}/dashboard: tiles,
// the agent activity calendar, event counts and the latest events. Every
// section is an aggregate query; answers are cached per project, caller and
// UTC offset (dashboard_cache.go), and a section that fails or runs out of
// time is named in "unavailable" while the rest still answers 200.
func (s *Server) handleProjectDashboard(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if !s.authorizeProject(w, r, id) {
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("utc_offset_minutes"))
	if offset < -840 || offset > 840 {
		offset = 0
	}
	data, ok := s.dashboardData(r.Context(), id, authSubject(r), offset)
	if !ok {
		// The caller went away while another request was building it.
		return
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
	metrics := make(map[string]any, len(data.metrics)+1)
	for k, v := range data.metrics {
		metrics[k] = v
	}
	metrics["online"] = online
	out := make(map[string]any, len(data.body)+1)
	for k, v := range data.body {
		out[k] = v
	}
	out["metrics"] = metrics
	writeJSON(w, http.StatusOK, out)
}

// buildDashboard runs the sections concurrently under one budget. It is
// detached from the request: a client that gives up does not waste the work,
// the next request is served from the cache.
func (s *Server) buildDashboard(project, owner string, offset int) *dashboardResult {
	ctx, cancel := context.WithTimeout(context.Background(), dashboardBudget)
	defer cancel()
	ctx = store.WithoutEmbeddings(ctx)
	now := time.Now().UTC()
	since := now.AddDate(0, 0, -364) // ~52 weeks

	var (
		mu          sync.Mutex
		unavailable []string
		wg          sync.WaitGroup
	)
	fail := func(section string, err error) {
		s.Log.Printf("dashboard %s: %s unavailable: %v", project, section, err)
		mu.Lock()
		unavailable = append(unavailable, section)
		mu.Unlock()
	}
	// Sections run on their own goroutines, outside net/http's panic
	// recovery: a panic in one is reported as that section missing.
	run := func(section string, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					fail(section, fmt.Errorf("panic: %v", p))
				}
			}()
			fn()
		}()
	}
	ds, _ := s.Store.(store.DashboardStore)

	var events store.DashboardEventStats
	eventsOK := false
	run("events", func() {
		if ds == nil {
			fail("events", errDashboardUnsupported)
			return
		}
		got, err := ds.DashboardEvents(ctx, project, since, now)
		if err != nil {
			fail("events", err)
			return
		}
		events, eventsOK = got, true
	})
	var memories store.DashboardMemoryCounts
	memoriesOK := false
	run("memories", func() {
		if ds == nil {
			fail("memories", errDashboardUnsupported)
			return
		}
		got, err := ds.DashboardMemories(ctx, project)
		if err != nil {
			fail("memories", err)
			return
		}
		memories, memoriesOK = got, true
	})
	var activity map[string]any
	run("agent_activity", func() {
		got, err := s.activityCalendar(ctx, project, owner, offset, now)
		if err != nil {
			fail("agent_activity", err)
			return
		}
		activity = got
	})
	members := 0
	run("members", func() {
		ids, err := s.Store.ListMembers(ctx, project)
		if err != nil {
			fail("members", err)
			return
		}
		members = len(ids)
	})
	githubConnected, githubRepo := false, ""
	run("github", func() {
		if gs, ok := s.githubStore(); ok {
			if link, err := gs.GetGitHubLink(ctx, project); err == nil && link != nil {
				githubConnected = true
				githubRepo = link.Owner + "/" + link.Repo
			}
		}
	})
	wg.Wait()

	heatmap := make([]map[string]any, 0, 371)
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		heatmap = append(heatmap, map[string]any{"date": key, "count": events.Days[key]})
	}
	byType := events.ByType
	if byType == nil {
		byType = []store.DashboardTypeCount{}
	}
	recent := events.Recent
	if recent == nil {
		recent = []*store.Event{}
	}
	metrics := map[string]any{
		"members":          members,
		"github_connected": githubConnected,
		"github_repo":      githubRepo,
	}
	// A section that could not be read is left out rather than shown as 0.
	if memoriesOK {
		metrics["memories"] = memories.Total
		metrics["proposed"] = memories.Proposed
		metrics["confirmed"] = memories.Confirmed
	}
	if eventsOK {
		metrics["events_7d"] = events.Week
		metrics["agent_calls"] = events.AgentCalls
	}
	body := map[string]any{
		"project_id":    project,
		"event_heatmap": heatmap,
		"by_type":       byType,
		"recent":        recent,
		"partial":       len(unavailable) > 0,
	}
	if activity != nil {
		body["heatmap"] = activity["days"]
		body["agent_activity"] = activity
	}
	if len(unavailable) > 0 {
		body["unavailable"] = unavailable
	}
	complete := len(unavailable) == 0
	if pending, _ := activity["archive_pending"].(bool); pending {
		complete = false
	}
	return &dashboardResult{metrics: metrics, body: body, complete: complete}
}
