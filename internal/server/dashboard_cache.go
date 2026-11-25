package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Dashboard answers are cached per project, caller and UTC offset: Home
// asks for every project on each visit, and a heavy project's numbers do
// not change second to second. A complete answer lives dashboardTTL; one
// with a missing section or archives still being read lives
// dashboardPartialTTL, so it is retried soon without each poll rebuilding
// it. Memory and project writes drop the cache (withDashboardInvalidation),
// so confirming a memory shows in the tiles at once.
var (
	dashboardTTL        = 60 * time.Second
	dashboardPartialTTL = 5 * time.Second
)

// dashboardCacheMax bounds the number of cached answers.
const dashboardCacheMax = 2048

var errDashboardUnsupported = errors.New("this store has no dashboard queries")

type dashboardResult struct {
	metrics  map[string]any // every tile except "online", which is live
	body     map[string]any // everything else; never mutated once built
	complete bool
}

type dashboardEntry struct {
	project string
	result  *dashboardResult
	expires time.Time
}

type dashboardFlight struct {
	done   chan struct{}
	result *dashboardResult
}

func dashboardKey(project, owner string, offset int) string {
	return project + "|" + owner + "|" + strconv.Itoa(offset)
}

// dashboardData returns the cached answer or builds it once for everyone
// asking at the same time. ok is false only when ctx ended while waiting.
func (s *Server) dashboardData(ctx context.Context, project, owner string, offset int) (*dashboardResult, bool) {
	key := dashboardKey(project, owner, offset)
	now := time.Now()
	s.dashMu.Lock()
	if entry, found := s.dashEntries[key]; found && now.Before(entry.expires) {
		s.dashMu.Unlock()
		return entry.result, true
	}
	flight, building := s.dashFlights[key]
	if !building {
		flight = &dashboardFlight{done: make(chan struct{})}
		if s.dashFlights == nil {
			s.dashFlights = map[string]*dashboardFlight{}
		}
		s.dashFlights[key] = flight
	}
	s.dashMu.Unlock()

	if !building {
		go func() {
			var result *dashboardResult
			func() {
				defer func() {
					if p := recover(); p != nil {
						s.Log.Printf("dashboard %s: build panicked: %v", project, p)
						result = &dashboardResult{metrics: map[string]any{}, body: map[string]any{
							"project_id": project, "partial": true, "unavailable": []string{"dashboard"}}}
					}
				}()
				result = s.buildDashboard(project, owner, offset)
			}()
			ttl := dashboardTTL
			if !result.complete {
				ttl = dashboardPartialTTL
			}
			s.dashMu.Lock()
			if s.dashEntries == nil || len(s.dashEntries) >= dashboardCacheMax {
				s.dashEntries = map[string]dashboardEntry{}
			}
			s.dashEntries[key] = dashboardEntry{project: project, result: result, expires: time.Now().Add(ttl)}
			delete(s.dashFlights, key)
			flight.result = result
			s.dashMu.Unlock()
			close(flight.done)
		}()
	}
	select {
	case <-flight.done:
		return flight.result, true
	case <-ctx.Done():
		return nil, false
	}
}

// invalidateDashboard drops cached answers for one project ("" = all).
func (s *Server) invalidateDashboard(project string) {
	s.dashMu.Lock()
	defer s.dashMu.Unlock()
	if project == "" {
		s.dashEntries = nil
		return
	}
	for key, entry := range s.dashEntries {
		if entry.project == project {
			delete(s.dashEntries, key)
		}
	}
}

// resetDashboardCache empties the answer cache (tests, perf measurement).
func (s *Server) resetDashboardCache() { s.invalidateDashboard("") }

// withDashboardInvalidation drops cached dashboards after a successful
// write that can change their numbers: memory writes (any project, since a
// memory's project is in its row, not its URL) and project changes.
func (s *Server) withDashboardInvalidation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return
		}
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/memory"), strings.Contains(path, "/memory/"), strings.HasSuffix(path, "/memory"):
			s.invalidateDashboard("")
		case strings.HasPrefix(path, "/projects/"):
			rest := strings.TrimPrefix(path, "/projects/")
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				rest = rest[:i]
			}
			s.invalidateDashboard(rest)
		}
	})
}
