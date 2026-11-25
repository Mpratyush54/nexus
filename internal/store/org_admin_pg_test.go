package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestOrgAdminPostgresCaptureAndStorage(t *testing.T) {
	dsn := testPostgresDSN(t)
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.RunMigrations(ctx, "../../migrations"); err != nil {
		t.Fatal(err)
	}

	// Own every row this test touches: a CI database (TEST_POSTGRES_DSN) is
	// migrated but not seeded, so "the first user" may not exist, and fixed
	// slugs would collide with leftovers from an earlier run.
	name := fmt.Sprintf("org-admin-pg-%d", time.Now().UnixNano())
	var userID string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users (username) VALUES ($1) RETURNING id::text`, name).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, userID)
	})
	org, err := s.CreateOrganization(ctx, "Org Admin PG", name, userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1::uuid`, org.ID)
	})
	proj, err := s.CreateOrgProject(ctx, org.ID, name, "Org Admin PG", "", "", userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1::uuid`, proj.ID)
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
		NativeID: name, OriginMachineID: "pg", Title: "hidden-title",
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
