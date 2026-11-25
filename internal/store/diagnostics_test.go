package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// exerciseDiagnosticsStore runs the same contract against any DiagnosticsStore.
func exerciseDiagnosticsStore(t *testing.T, ds DiagnosticsStore, userID, prefix string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	fp := prefix + "fp-a"
	mk := func(id, version, user string, age time.Duration) *DiagnosticReport {
		return &DiagnosticReport{
			ReportID: prefix + id, Fingerprint: fp, Kind: "crash", Source: "web",
			AppVersion: version, OS: "Windows 11", Message: "boom " + id, Stack: "at x",
			Logs: []string{"one", "two"}, Context: map[string]string{"route": "/app"},
			UserID: user, RequestID: "REQ" + id, ClientTime: now, ReceivedAt: now.Add(-age),
		}
	}
	for _, r := range []*DiagnosticReport{
		mk("r1", "1.0.0", userID, 40*24*time.Hour),
		mk("r2", "1.0.0", "", 2*time.Hour),
		mk("r3", "1.1.0", userID, time.Hour),
	} {
		ok, err := ds.InsertDiagnosticReport(ctx, r)
		if err != nil || !ok {
			t.Fatalf("insert %s: %v %v", r.ReportID, ok, err)
		}
	}
	if ok, err := ds.InsertDiagnosticReport(ctx, mk("r3", "1.1.0", userID, 0)); err != nil || ok {
		t.Fatalf("duplicate insert = %v %v, want false nil", ok, err)
	}
	got, err := ds.GetDiagnosticReport(ctx, prefix+"r2")
	if err != nil || got.Message != "boom r2" || len(got.Logs) != 2 || got.Context["route"] != "/app" || got.UserID != "" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := ds.GetDiagnosticReport(ctx, prefix+"nope"); err != ErrNotFound {
		t.Fatalf("missing = %v", err)
	}

	groups, err := ds.ListDiagnosticGroups(ctx, DiagnosticFilter{Source: "web", Since: now.Add(-50 * 24 * time.Hour), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var g *DiagnosticGroup
	for _, x := range groups {
		if x.Fingerprint == fp {
			g = x
		}
	}
	if g == nil || g.Count != 3 || g.LatestReportID != prefix+"r3" || g.Message != "boom r3" || len(g.Versions) != 2 {
		t.Fatalf("group = %+v", g)
	}
	if userID != "" && g.Users != 1 {
		t.Fatalf("distinct users = %d", g.Users)
	}
	reports, err := ds.ListDiagnosticReports(ctx, fp, 2)
	if err != nil || len(reports) != 2 || reports[0].ReportID != prefix+"r3" {
		t.Fatalf("reports = %+v %v", reports, err)
	}

	n, err := ds.PruneDiagnosticReports(ctx, now.Add(-DiagnosticsRetention))
	if err != nil || n < 1 {
		t.Fatalf("prune = %d %v", n, err)
	}
	if _, err := ds.GetDiagnosticReport(ctx, prefix+"r1"); err != ErrNotFound {
		t.Fatalf("40-day-old report survived prune: %v", err)
	}
	if _, err := ds.GetDiagnosticReport(ctx, prefix+"r3"); err != nil {
		t.Fatalf("recent report pruned: %v", err)
	}
}

func TestMemStoreDiagnostics(t *testing.T) {
	exerciseDiagnosticsStore(t, NewMemStore(), "alice", "mem-")
}

func TestPostgresDiagnostics(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("diag-%d", time.Now().UnixNano())
	var user string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&user); err != nil {
		t.Fatal(err)
	}
	prefix := name + "-"
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.pool.Exec(bg, `DELETE FROM diagnostic_reports WHERE report_id LIKE $1`, prefix+"%")
		_, _ = s.pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, user)
	})
	exerciseDiagnosticsStore(t, s, user, prefix)

	// Deleting the account keeps the report but unlinks it.
	if _, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, user); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDiagnosticReport(ctx, prefix+"r3")
	if err != nil || got.UserID != "" {
		t.Fatalf("after account delete = %+v %v", got, err)
	}
	// A non-UUID subject (dev/test tokens) is stored unlinked, not rejected.
	if ok, err := s.InsertDiagnosticReport(ctx, &DiagnosticReport{ReportID: prefix + "devsub", Fingerprint: "x", Kind: "error", Source: "cli", Message: "m", UserID: "alice"}); err != nil || !ok {
		t.Fatalf("non-uuid subject: %v %v", ok, err)
	}
}
