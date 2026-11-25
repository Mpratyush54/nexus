package server

// diagnostics_routes.go — problem reports from clients.
//
//	POST /v1/diagnostics                     any client; auth optional
//	GET  /admin/diagnostics                  platform admins: groups by fingerprint
//	GET  /admin/diagnostics/{fingerprint}    platform admins: one group's reports
//	GET  /admin/diagnostics/reports/{id}     platform admins: one report
//
// Reports are capped and redacted by internal/diagnostics before they are
// stored. A valid bearer links the report to the account; a missing or
// invalid one (an expired session should still deliver its crash) makes it
// anonymous, which is rate-limited per IP and globally.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"central-memory/internal/diagnostics"
	"central-memory/internal/store"
)

// Report rate limits (fixed one-hour windows).
const (
	diagAnonPerIPPerHour   = 10
	diagAnonGlobalPerHour  = 300
	diagUserPerHour        = 60
	diagLimiterWindow      = time.Hour
	diagLimiterMaxKeys     = 8192
	defaultDiagPrunePeriod = 6 * time.Hour
)

func (s *Server) registerDiagnosticsRoutes() {
	s.Mux.HandleFunc("POST /v1/diagnostics", s.handleDiagnosticsPost)
	s.Mux.HandleFunc("GET /admin/diagnostics", s.requireAuth(s.requirePlatformAdmin(s.handleAdminDiagnosticsList)))
	s.Mux.HandleFunc("GET /admin/diagnostics/reports/{id}", s.requireAuth(s.requirePlatformAdmin(s.handleAdminDiagnosticsReport)))
	s.Mux.HandleFunc("GET /admin/diagnostics/{fingerprint}", s.requireAuth(s.requirePlatformAdmin(s.handleAdminDiagnosticsGroup)))
}

func (s *Server) diagnosticsStore() (store.DiagnosticsStore, bool) {
	ds, ok := s.Store.(store.DiagnosticsStore)
	return ds, ok
}

// windowLimiter counts events per key in fixed windows. Keys are
// attacker-controlled (IPs), so the map is bounded: past maxKeys the
// stalest window is dropped.
type windowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	counts  map[string]*windowCount
}

type windowCount struct {
	start time.Time
	n     int
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{limit: limit, window: window, maxKeys: diagLimiterMaxKeys, now: time.Now, counts: map[string]*windowCount{}}
}

// allow records one event for key and reports whether it is within the
// limit; when it is not, retry is how long until the window resets.
func (l *windowLimiter) allow(key string) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	c, found := l.counts[key]
	if !found || now.Sub(c.start) >= l.window {
		if !found && len(l.counts) >= l.maxKeys {
			l.evictLocked(now)
		}
		c = &windowCount{start: now}
		l.counts[key] = c
	}
	if c.n >= l.limit {
		return false, c.start.Add(l.window).Sub(now)
	}
	c.n++
	return true, 0
}

func (l *windowLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, c := range l.counts {
		if now.Sub(c.start) >= l.window {
			delete(l.counts, k)
			continue
		}
		if oldestKey == "" || c.start.Before(oldest) {
			oldestKey, oldest = k, c.start
		}
	}
	if len(l.counts) >= l.maxKeys && oldestKey != "" {
		delete(l.counts, oldestKey)
	}
}

// diagLimiters holds the three gates; built once per Server.
type diagLimiters struct {
	anonIP     *windowLimiter
	anonGlobal *windowLimiter
	user       *windowLimiter
}

func (s *Server) diagGates() *diagLimiters {
	s.diagOnce.Do(func() {
		s.diagLimits = &diagLimiters{
			anonIP:     newWindowLimiter(diagAnonPerIPPerHour, diagLimiterWindow),
			anonGlobal: newWindowLimiter(diagAnonGlobalPerHour, diagLimiterWindow),
			user:       newWindowLimiter(diagUserPerHour, diagLimiterWindow),
		}
	})
	return s.diagLimits
}

// diagnosticsResponse is the body of a successful POST /v1/diagnostics.
type diagnosticsResponse struct {
	ReportID    string `json:"report_id"`
	Fingerprint string `json:"fingerprint"`
	// Duplicate is true when this report_id was already stored (a retry).
	Duplicate bool `json:"duplicate"`
	// RequestID is this upload's request ID, also in X-Request-ID.
	RequestID string `json:"request_id"`
}

func (s *Server) handleDiagnosticsPost(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.diagnosticsStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "problem reports are not available on this server")
		return
	}

	// Optional auth: link signed-in users, never reject a report because
	// its session expired.
	userID := ""
	if r.Header.Get("Authorization") != "" {
		if id, err := s.resolveBearer(r); err == nil {
			userID = id.Sub
		}
	}

	gates := s.diagGates()
	if userID != "" {
		if ok, retry := gates.user.allow("user:" + userID); !ok {
			writeRateLimited(w, retry, "too many problem reports from this account; try again later")
			return
		}
	} else {
		if ok, retry := gates.anonIP.allow("ip:" + clientIP(r)); !ok {
			writeRateLimited(w, retry, "too many problem reports from this address; sign in or try again later")
			return
		}
		if ok, retry := gates.anonGlobal.allow("anon"); !ok {
			writeRateLimited(w, retry, "too many anonymous problem reports right now; sign in or try again later")
			return
		}
	}

	if r.ContentLength > diagnostics.MaxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "report is larger than 256 KB")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, diagnostics.MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "report is larger than 256 KB")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read report body")
		return
	}
	var rep diagnostics.Report
	// Unknown fields are allowed: newer clients may send more than this
	// server stores.
	if err := json.Unmarshal(body, &rep); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := diagnostics.Normalize(&rep, time.Now()); err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	rid := requestIDOf(r)
	row := &store.DiagnosticReport{
		ReportID:    rep.ReportID,
		Fingerprint: diagnostics.Fingerprint(&rep),
		Kind:        rep.Kind,
		Source:      rep.Source,
		AppVersion:  rep.AppVersion,
		OS:          rep.OS,
		Message:     rep.Message,
		Stack:       rep.Stack,
		Logs:        rep.Logs,
		UserNote:    rep.UserNote,
		Context:     rep.Context,
		UserID:      userID,
		RequestID:   rid,
		ClientTime:  *rep.Timestamp,
	}
	inserted, err := ds.InsertDiagnosticReport(r.Context(), row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not store report")
		s.Log.Printf("diagnostics: store report rid=%s: %v", rid, err)
		return
	}
	if inserted {
		s.Log.Printf("diagnostics: report=%s fp=%s kind=%s source=%s version=%q signed_in=%t rid=%s",
			row.ReportID, row.Fingerprint, row.Kind, row.Source, row.AppVersion, userID != "", rid)
	}
	status := http.StatusAccepted
	if !inserted {
		status = http.StatusOK
	}
	writeJSON(w, status, diagnosticsResponse{ReportID: row.ReportID, Fingerprint: row.Fingerprint, Duplicate: !inserted, RequestID: rid})
}

func writeRateLimited(w http.ResponseWriter, retry time.Duration, msg string) {
	secs := int(retry.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, msg)
}

func (s *Server) handleAdminDiagnosticsList(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.diagnosticsStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": []*store.DiagnosticGroup{}, "count": 0})
		return
	}
	q := r.URL.Query()
	f := store.DiagnosticFilter{
		Source: strings.ToLower(strings.TrimSpace(q.Get("source"))),
		Kind:   strings.ToLower(strings.TrimSpace(q.Get("kind"))),
	}
	if f.Source != "" && !diagnostics.ValidSource(f.Source) {
		writeError(w, http.StatusBadRequest, "source must be desktop, core, web or cli")
		return
	}
	if f.Kind != "" && !diagnostics.ValidKind(f.Kind) {
		writeError(w, http.StatusBadRequest, "kind must be crash, error or user_report")
		return
	}
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC 3339 time")
			return
		}
		f.Since = t
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	items, err := ds.ListDiagnosticGroups(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list problem reports")
		s.Log.Printf("diagnostics: list groups rid=%s: %v", requestIDOf(r), err)
		return
	}
	if items == nil {
		items = []*store.DiagnosticGroup{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "retention_days": int(store.DiagnosticsRetention.Hours() / 24)})
}

func (s *Server) handleAdminDiagnosticsGroup(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.diagnosticsStore()
	if !ok {
		writeError(w, http.StatusNotFound, "no reports")
		return
	}
	fp := strings.TrimSpace(r.PathValue("fingerprint"))
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	reports, err := ds.ListDiagnosticReports(r.Context(), fp, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load reports")
		s.Log.Printf("diagnostics: list reports rid=%s: %v", requestIDOf(r), err)
		return
	}
	if len(reports) == 0 {
		writeError(w, http.StatusNotFound, "no reports with that fingerprint")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp, "items": reports, "count": len(reports)})
}

func (s *Server) handleAdminDiagnosticsReport(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.diagnosticsStore()
	if !ok {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	rep, err := ds.GetDiagnosticReport(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load report")
		s.Log.Printf("diagnostics: get report rid=%s: %v", requestIDOf(r), err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// StartDiagnosticsRetention deletes problem reports older than
// store.DiagnosticsRetention once at start and then every interval (6h by
// default), like StartSnapshotRetention. Stores without diagnostics
// support make it a no-op.
func StartDiagnosticsRetention(ctx context.Context, st store.Store, interval time.Duration) (stop func()) {
	ds, ok := st.(store.DiagnosticsStore)
	if !ok {
		return func() {}
	}
	if interval <= 0 {
		interval = defaultDiagPrunePeriod
	}
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	go func() {
		pruneDiagnostics(ctx, ds, time.Now())
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case now := <-t.C:
				pruneDiagnostics(ctx, ds, now)
			}
		}
	}()
	return stop
}

// pruneDiagnostics runs one retention pass relative to now.
func pruneDiagnostics(ctx context.Context, ds store.DiagnosticsStore, now time.Time) int64 {
	if ctx.Err() != nil {
		return 0
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	n, err := ds.PruneDiagnosticReports(pctx, now.Add(-store.DiagnosticsRetention))
	if err != nil {
		log.Printf("server: diagnostics retention: %v", err)
		return 0
	}
	if n > 0 {
		log.Printf("server: diagnostics retention deleted=%d", n)
	}
	return n
}
