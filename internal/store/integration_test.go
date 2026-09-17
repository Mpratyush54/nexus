// Postgres integration tests for the store layer (issue #2 acceptance:
// deterministic resolution, heartbeat expiry end-to-end).
//
// Gated on TEST_POSTGRES_DSN: without a live database every test here
// skips, so `go test ./...` stays green on machines without Postgres.
// With a DSN (e.g. a local Postgres 16 + pgvector container) the tests apply
// the real migrations/001_initial.up.sql via RunMigrations and exercise
// Resolve/Register/Heartbeat against the planned schema.
package store_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

// issue2TestDB connects to the TEST_POSTGRES_DSN database or skips.
func issue2TestDB(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := store.Connect(ctx, store.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.Health(ctx); err != nil {
		t.Fatalf("test database unhealthy: %v", err)
	}
	return db, ctx
}

// issue2EnsureSchema applies the repo migrations. Re-running 001 against an
// already-migrated database fails with "already exists" (001 uses plain
// CREATE TABLE); that is tolerated so tests stay re-runnable.
func issue2EnsureSchema(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	applied, err := store.RunMigrations(ctx, db, "../../migrations")
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("run migrations: %v (applied: %v)", err, applied)
	}
}

// issue2User inserts a throwaway user for workspace FK references.
func issue2User(t *testing.T, ctx context.Context, db *store.DB, suffix string) string {
	t.Helper()
	var id string
	err := db.QueryRow(ctx,
		`INSERT INTO users (username) VALUES ($1) RETURNING id::TEXT`,
		"issue2_"+suffix).Scan(&id)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	return id
}

func TestIntegration_ProjectResolveDedup(t *testing.T) {
	db, ctx := issue2TestDB(t)
	issue2EnsureSchema(t, ctx, db)
	ps := store.NewProjectStore(db)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	origin := "https://github.com/test-org/issue2-" + suffix + ".git"
	root := "issue2root" + suffix
	folder := "issue2-" + suffix

	p1, err := ps.Resolve(ctx, store.ProjectParams{
		Origin: origin, RootCommit: root, FolderName: folder, DisplayName: "first",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p1.ID == "" || p1.CanonicalURL != "github.com/test-org/issue2-"+suffix {
		t.Fatalf("unexpected resolved project: %+v", p1)
	}

	// Same remote, renamed folder, no root: canonical_url priority, same row.
	p2, err := ps.Resolve(ctx, store.ProjectParams{
		Origin: origin, FolderName: "renamed-" + suffix,
	})
	if err != nil {
		t.Fatalf("resolve by url: %v", err)
	}
	if p2.ID != p1.ID {
		t.Fatalf("url re-resolve created a duplicate: %q vs %q", p2.ID, p1.ID)
	}

	// Same root commit, no remote: root_commit priority, same row.
	p3, err := ps.Resolve(ctx, store.ProjectParams{
		RootCommit: root, FolderName: "other-" + suffix,
	})
	if err != nil {
		t.Fatalf("resolve by root: %v", err)
	}
	if p3.ID != p1.ID {
		t.Fatalf("root re-resolve created a duplicate: %q vs %q", p3.ID, p1.ID)
	}

	// Folder only: fallback chain, same row.
	p4, err := ps.Resolve(ctx, store.ProjectParams{FolderName: folder})
	if err != nil {
		t.Fatalf("resolve by folder: %v", err)
	}
	if p4.ID != p1.ID {
		t.Fatalf("folder re-resolve created a duplicate: %q vs %q", p4.ID, p1.ID)
	}

	got, err := ps.GetByID(ctx, p1.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != p1.ID || got.FolderName != folder {
		t.Fatalf("get mismatch: %+v", got)
	}

	renamed, err := ps.UpdateDisplayName(ctx, p1.ID, "renamed-display")
	if err != nil {
		t.Fatalf("update display name: %v", err)
	}
	if renamed.DisplayName != "renamed-display" {
		t.Fatalf("display name not updated: %+v", renamed)
	}

	if err := ps.Delete(ctx, p1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestIntegration_WorkspaceLifecycle(t *testing.T) {
	db, ctx := issue2TestDB(t)
	issue2EnsureSchema(t, ctx, db)
	ps := store.NewProjectStore(db)
	ws := store.NewWorkspaceStore(db)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := issue2User(t, ctx, db, "ws_"+suffix)
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	proj, err := ps.Resolve(ctx, store.ProjectParams{
		Origin:     "git@github.com:test-org/issue2-ws-" + suffix + ".git",
		FolderName: "issue2-ws-" + suffix,
	})
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	t.Cleanup(func() {
		_ = ps.Delete(context.Background(), proj.ID)
	})

	reg, err := ws.Register(ctx, store.WorkspaceParams{
		ProjectID: proj.ID, UserID: userID,
		MachineID: "issue2-machine-" + suffix,
		Path:      "/tmp/issue2-ws-" + suffix,
		Branch:    "main", CommitSHA: "abc",
		DaemonURL: "ws://localhost:49152",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !reg.IsOnline || reg.LastSeen == nil {
		t.Fatalf("fresh registration must be online with last_seen: %+v", reg)
	}

	// Re-register (daemon restart): same row, refreshed state, no duplicate.
	reg2, err := ws.Register(ctx, store.WorkspaceParams{
		ProjectID: proj.ID, UserID: userID,
		MachineID: "issue2-machine-" + suffix,
		Path:      "/tmp/issue2-ws-" + suffix,
		Branch:    "feature", CommitSHA: "def", IsDirty: true,
		DaemonURL: "ws://localhost:49153",
	})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if reg2.ID != reg.ID || reg2.Branch != "feature" || !reg2.IsDirty {
		t.Fatalf("re-register must upsert in place: %+v", reg2)
	}

	hb, err := ws.Heartbeat(ctx, reg.ID, store.HeartbeatParams{
		Branch: "feature", CommitSHA: "deadbee", IsDirty: false,
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if hb.CommitSHA != "deadbee" || hb.IsDirty || !hb.IsOnline {
		t.Fatalf("heartbeat state wrong: %+v", hb)
	}

	active, err := ws.ListActive(ctx, proj.ID)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].ID != reg.ID {
		t.Fatalf("expected 1 active workspace, got %+v", active)
	}

	// Simulate 5 minutes of silence: the timestamp predicate alone must
	// exclude the row even before the sweeper runs.
	if _, err := db.Exec(ctx,
		`UPDATE workspaces SET last_seen = now() - interval '5 minutes', is_online = true WHERE id = $1`,
		reg.ID); err != nil {
		t.Fatalf("backdate last_seen: %v", err)
	}
	active, err = ws.ListActive(ctx, proj.ID)
	if err != nil {
		t.Fatalf("list active after silence: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("silent workspace must not list as active: %+v", active)
	}

	n, err := ws.MarkStaleOffline(ctx)
	if err != nil {
		t.Fatalf("mark stale offline: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 row marked offline, got %d", n)
	}
	stale, err := ws.GetByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("get after sweep: %v", err)
	}
	if stale.IsOnline {
		t.Fatal("workspace must be offline after sweep")
	}

	// A new heartbeat revives the row without re-registering.
	if _, err := ws.Heartbeat(ctx, reg.ID, store.HeartbeatParams{Branch: "feature"}); err != nil {
		t.Fatalf("revive heartbeat: %v", err)
	}
	if err := ws.SetDesignatedProcessor(ctx, reg.ID, true); err != nil {
		t.Fatalf("set designated processor: %v", err)
	}
	revived, err := ws.GetByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("get revived: %v", err)
	}
	if !revived.IsOnline || !revived.IsDesignatedProcessor {
		t.Fatalf("revive state wrong: %+v", revived)
	}

	if err := ws.Delete(ctx, reg.ID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
}
