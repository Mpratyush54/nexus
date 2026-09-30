package store

import (
	"context"
	"os"
	"testing"
)

func TestOrgAdminPostgresCaptureAndStorage(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	var userID string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	org, err := s.CreateOrganization(ctx, "Org Admin PG", "org-admin-pg", userID)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := s.CreateOrgProject(ctx, org.ID, "org-admin-pg", "Org Admin PG", "", "", userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.pool.Exec(bg, `DELETE FROM projects WHERE id = $1::uuid`, proj.ID)
		_, _ = s.pool.Exec(bg, `DELETE FROM organizations WHERE id = $1::uuid`, org.ID)
	})

	on, err := s.CaptureEnabled(ctx, proj.ID)
	if err != nil || !on {
		t.Fatalf("default capture = %v %v", on, err)
	}
	if err := s.SetCaptureEnabled(ctx, proj.ID, false, userID); err != nil {
		t.Fatal(err)
	}
	on, err = s.CaptureEnabled(ctx, proj.ID)
	if err != nil || on {
		t.Fatalf("capture after off = %v %v", on, err)
	}
	if _, err := s.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: proj.ID, OwnerUserID: userID, Harness: "claude",
		NativeID: "org-admin-pg", OriginMachineID: "pg", Title: "hidden-title",
	}); err != nil {
		t.Fatal(err)
	}
	report, err := s.OrgStorageAggregates(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range report.Members {
		if row.UserID == userID && row.SessionCount >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("member aggregate missing: %+v", report.Members)
	}
	var sawProject bool
	for _, p := range report.Projects {
		if p.ProjectID != proj.ID {
			continue
		}
		sawProject = true
		if p.CaptureEnabled {
			t.Fatalf("project capture still on: %+v", p)
		}
	}
	if !sawProject {
		t.Fatalf("project missing from storage report: %+v", report.Projects)
	}
}
