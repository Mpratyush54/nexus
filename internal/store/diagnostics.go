package store

// diagnostics.go — problem reports from clients (POST /v1/diagnostics,
// migration 052). Reports arrive already redacted and capped by
// internal/diagnostics; the store only persists, groups and prunes them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DiagnosticsRetention is how long problem reports are kept.
const DiagnosticsRetention = 30 * 24 * time.Hour

// DiagnosticReport is one stored problem report.
type DiagnosticReport struct {
	ReportID    string            `json:"report_id"`
	Fingerprint string            `json:"fingerprint"`
	Kind        string            `json:"kind"`
	Source      string            `json:"source"`
	AppVersion  string            `json:"app_version"`
	OS          string            `json:"os"`
	Message     string            `json:"message"`
	Stack       string            `json:"stack,omitempty"`
	Logs        []string          `json:"logs,omitempty"`
	UserNote    string            `json:"user_note,omitempty"`
	Context     map[string]string `json:"context,omitempty"`
	UserID      string            `json:"user_id,omitempty"`
	RequestID   string            `json:"request_id,omitempty"`
	ClientTime  time.Time         `json:"timestamp"`
	ReceivedAt  time.Time         `json:"received_at"`
}

// DiagnosticGroup summarises the reports sharing one fingerprint.
type DiagnosticGroup struct {
	Fingerprint    string    `json:"fingerprint"`
	Kind           string    `json:"kind"`
	Source         string    `json:"source"`
	Message        string    `json:"message"`
	Count          int       `json:"count"`
	Users          int       `json:"users"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	Versions       []string  `json:"versions"`
	LatestReportID string    `json:"latest_report_id"`
}

// DiagnosticFilter narrows the admin group list.
type DiagnosticFilter struct {
	Source string
	Kind   string
	Since  time.Time
	Limit  int
}

// DiagnosticsStore persists problem reports.
type DiagnosticsStore interface {
	// InsertDiagnosticReport stores r and fills ReceivedAt (now unless the
	// caller preset it). A report_id
	// already stored is not stored again: inserted is false.
	InsertDiagnosticReport(ctx context.Context, r *DiagnosticReport) (inserted bool, err error)
	ListDiagnosticGroups(ctx context.Context, f DiagnosticFilter) ([]*DiagnosticGroup, error)
	// ListDiagnosticReports returns the newest reports for a fingerprint.
	ListDiagnosticReports(ctx context.Context, fingerprint string, limit int) ([]*DiagnosticReport, error)
	GetDiagnosticReport(ctx context.Context, reportID string) (*DiagnosticReport, error)
	// PruneDiagnosticReports deletes reports received before cutoff.
	PruneDiagnosticReports(ctx context.Context, cutoff time.Time) (int64, error)
}

const maxDiagnosticVersions = 10

func clampDiagLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func cloneDiagnosticReport(r *DiagnosticReport) *DiagnosticReport {
	out := *r
	out.Logs = append([]string(nil), r.Logs...)
	if r.Context != nil {
		out.Context = make(map[string]string, len(r.Context))
		for k, v := range r.Context {
			out.Context[k] = v
		}
	}
	return &out
}

// --- MemStore ---

func (s *MemStore) InsertDiagnosticReport(_ context.Context, r *DiagnosticReport) (bool, error) {
	if r == nil || strings.TrimSpace(r.ReportID) == "" || r.Fingerprint == "" {
		return false, fmt.Errorf("store: report_id and fingerprint are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.diagReports == nil {
		s.diagReports = map[string]*DiagnosticReport{}
	}
	if _, ok := s.diagReports[r.ReportID]; ok {
		return false, nil
	}
	if r.ReceivedAt.IsZero() {
		r.ReceivedAt = time.Now().UTC()
	}
	s.diagReports[r.ReportID] = cloneDiagnosticReport(r)
	return true, nil
}

func (s *MemStore) ListDiagnosticGroups(_ context.Context, f DiagnosticFilter) ([]*DiagnosticGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	groups := map[string]*DiagnosticGroup{}
	users := map[string]map[string]bool{}
	for _, r := range s.diagReports {
		if f.Source != "" && r.Source != f.Source {
			continue
		}
		if f.Kind != "" && r.Kind != f.Kind {
			continue
		}
		if !f.Since.IsZero() && r.ReceivedAt.Before(f.Since) {
			continue
		}
		g, ok := groups[r.Fingerprint]
		if !ok {
			g = &DiagnosticGroup{Fingerprint: r.Fingerprint, Kind: r.Kind, Source: r.Source, FirstSeen: r.ReceivedAt}
			groups[r.Fingerprint] = g
			users[r.Fingerprint] = map[string]bool{}
		}
		g.Count++
		if r.UserID != "" {
			users[r.Fingerprint][r.UserID] = true
		}
		if r.ReceivedAt.Before(g.FirstSeen) {
			g.FirstSeen = r.ReceivedAt
		}
		if !r.ReceivedAt.Before(g.LastSeen) {
			g.LastSeen = r.ReceivedAt
			g.Message = r.Message
			g.LatestReportID = r.ReportID
		}
		if r.AppVersion != "" && !containsString(g.Versions, r.AppVersion) && len(g.Versions) < maxDiagnosticVersions {
			g.Versions = append(g.Versions, r.AppVersion)
		}
	}
	out := make([]*DiagnosticGroup, 0, len(groups))
	for fp, g := range groups {
		g.Users = len(users[fp])
		sort.Strings(g.Versions)
		if g.Versions == nil {
			g.Versions = []string{}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if limit := clampDiagLimit(f.Limit, 50, 200); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *MemStore) ListDiagnosticReports(_ context.Context, fingerprint string, limit int) ([]*DiagnosticReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*DiagnosticReport
	for _, r := range s.diagReports {
		if r.Fingerprint == fingerprint {
			out = append(out, cloneDiagnosticReport(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.After(out[j].ReceivedAt) })
	if limit = clampDiagLimit(limit, 20, 100); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemStore) GetDiagnosticReport(_ context.Context, reportID string) (*DiagnosticReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.diagReports[reportID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneDiagnosticReport(r), nil
}

func (s *MemStore) PruneDiagnosticReports(_ context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, r := range s.diagReports {
		if r.ReceivedAt.Before(cutoff) {
			delete(s.diagReports, id)
			n++
		}
	}
	return n, nil
}

// --- PostgresStore ---

func (s *PostgresStore) InsertDiagnosticReport(ctx context.Context, r *DiagnosticReport) (bool, error) {
	if r == nil || strings.TrimSpace(r.ReportID) == "" || r.Fingerprint == "" {
		return false, fmt.Errorf("store: report_id and fingerprint are required")
	}
	logs := r.Logs
	if logs == nil {
		logs = []string{}
	}
	logsJSON, err := json.Marshal(logs)
	if err != nil {
		return false, fmt.Errorf("store: marshal logs: %w", err)
	}
	ctxMap := r.Context
	if ctxMap == nil {
		ctxMap = map[string]string{}
	}
	ctxJSON, err := json.Marshal(ctxMap)
	if err != nil {
		return false, fmt.Errorf("store: marshal context: %w", err)
	}
	clientTS := r.ClientTime
	if clientTS.IsZero() {
		clientTS = time.Now().UTC()
	}
	// ReceivedAt is normally left zero (the database clock); imports and
	// tests may preset it.
	var receivedAt any
	if !r.ReceivedAt.IsZero() {
		receivedAt = r.ReceivedAt
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO diagnostic_reports
		  (report_id, fingerprint, kind, source, app_version, os, message, stack,
		   logs, user_note, context, user_id, request_id, client_ts, received_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11::jsonb, $12::uuid, $13, $14,
		        COALESCE($15::timestamptz, now()))
		ON CONFLICT (report_id) DO NOTHING
		RETURNING received_at`,
		r.ReportID, r.Fingerprint, r.Kind, r.Source, r.AppVersion, r.OS, r.Message, r.Stack,
		string(logsJSON), r.UserNote, string(ctxJSON), nullUUIDStrict(r.UserID), r.RequestID, clientTS, receivedAt,
	).Scan(&r.ReceivedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: insert diagnostic report: %w", err)
	}
	return true, nil
}

func (s *PostgresStore) ListDiagnosticGroups(ctx context.Context, f DiagnosticFilter) ([]*DiagnosticGroup, error) {
	q := `
		SELECT fingerprint,
		       (array_agg(kind ORDER BY received_at DESC))[1],
		       (array_agg(source ORDER BY received_at DESC))[1],
		       (array_agg(message ORDER BY received_at DESC))[1],
		       COUNT(*)::int,
		       COUNT(DISTINCT user_id)::int,
		       MIN(received_at), MAX(received_at),
		       COALESCE(array_agg(DISTINCT app_version) FILTER (WHERE app_version <> ''), '{}'),
		       (array_agg(report_id ORDER BY received_at DESC))[1]
		  FROM diagnostic_reports
		 WHERE 1=1`
	args := []any{}
	if f.Source != "" {
		args = append(args, f.Source)
		q += fmt.Sprintf(" AND source = $%d", len(args))
	}
	if f.Kind != "" {
		args = append(args, f.Kind)
		q += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since)
		q += fmt.Sprintf(" AND received_at >= $%d", len(args))
	}
	args = append(args, clampDiagLimit(f.Limit, 50, 200))
	q += fmt.Sprintf(" GROUP BY fingerprint ORDER BY MAX(received_at) DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list diagnostic groups: %w", err)
	}
	defer rows.Close()
	out := []*DiagnosticGroup{}
	for rows.Next() {
		g := &DiagnosticGroup{}
		if err := rows.Scan(&g.Fingerprint, &g.Kind, &g.Source, &g.Message, &g.Count, &g.Users,
			&g.FirstSeen, &g.LastSeen, &g.Versions, &g.LatestReportID); err != nil {
			return nil, fmt.Errorf("store: scan diagnostic group: %w", err)
		}
		sort.Strings(g.Versions)
		if len(g.Versions) > maxDiagnosticVersions {
			g.Versions = g.Versions[len(g.Versions)-maxDiagnosticVersions:]
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const diagReportColumns = `report_id, fingerprint, kind, source, app_version, os, message, stack,
	logs::text, user_note, context::text, COALESCE(user_id::text, ''), request_id, client_ts, received_at`

type diagScanner interface {
	Scan(dest ...any) error
}

func scanDiagnosticReport(row diagScanner) (*DiagnosticReport, error) {
	r := &DiagnosticReport{}
	var logsJSON, ctxJSON string
	if err := row.Scan(&r.ReportID, &r.Fingerprint, &r.Kind, &r.Source, &r.AppVersion, &r.OS, &r.Message, &r.Stack,
		&logsJSON, &r.UserNote, &ctxJSON, &r.UserID, &r.RequestID, &r.ClientTime, &r.ReceivedAt); err != nil {
		return nil, err
	}
	if logsJSON != "" {
		_ = json.Unmarshal([]byte(logsJSON), &r.Logs)
	}
	if ctxJSON != "" && ctxJSON != "{}" {
		_ = json.Unmarshal([]byte(ctxJSON), &r.Context)
	}
	return r, nil
}

func (s *PostgresStore) ListDiagnosticReports(ctx context.Context, fingerprint string, limit int) ([]*DiagnosticReport, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+diagReportColumns+`
		  FROM diagnostic_reports WHERE fingerprint = $1
		 ORDER BY received_at DESC LIMIT $2`, fingerprint, clampDiagLimit(limit, 20, 100))
	if err != nil {
		return nil, fmt.Errorf("store: list diagnostic reports: %w", err)
	}
	defer rows.Close()
	var out []*DiagnosticReport
	for rows.Next() {
		r, err := scanDiagnosticReport(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan diagnostic report: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetDiagnosticReport(ctx context.Context, reportID string) (*DiagnosticReport, error) {
	r, err := scanDiagnosticReport(s.pool.QueryRow(ctx, `SELECT `+diagReportColumns+`
		  FROM diagnostic_reports WHERE report_id = $1`, reportID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get diagnostic report: %w", err)
	}
	return r, nil
}

func (s *PostgresStore) PruneDiagnosticReports(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM diagnostic_reports WHERE received_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("store: prune diagnostic reports: %w", err)
	}
	return tag.RowsAffected(), nil
}

var (
	_ DiagnosticsStore = (*MemStore)(nil)
	_ DiagnosticsStore = (*PostgresStore)(nil)
)
