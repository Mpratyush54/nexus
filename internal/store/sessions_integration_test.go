// Postgres integration tests for sessions (issue #12 acceptance:
// multi-user join, session isolation until promoted).
//
// Gated on TEST_POSTGRES_DSN: without a live database every test here
// skips, so `go test ./...` stays green on machines without Postgres.
// With a DSN the tests apply the real migrations (001 + 003) via
// RunMigrations and exercise Create/Join/Leave/ListActive plus the
// session-scoped memory promotion counter end-to-end.
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

// sessionTestDB connects to the TEST_POSTGRES_DSN database or skips.
func sessionTestDB(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping session integration test")
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

// sessionEnsureSchema applies the repo migrations. 001 uses plain CREATE
// TABLE so re-running against a migrated DB fails with "already exists";
// that is tolerated so tests stay re-runnable.
func sessionEnsureSchema(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	applied, err := store.RunMigrations(ctx, db, "../../migrations")
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("run migrations: %v (applied: %v)", err, applied)
	}
}

// sessionUser inserts a throwaway user for session FK references.
func sessionUser(t *testing.T, ctx context.Context, db *store.DB, suffix string) string {
	t.Helper()
	var id string
	err := db.QueryRow(ctx,
		`INSERT INTO users (username) VALUES ($1) RETURNING id::TEXT`,
		"issue12_"+suffix).Scan(&id)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	return id
}

// sessionProject inserts a throwaway project for session FK references.
func sessionProject(t *testing.T, ctx context.Context, db *store.DB, suffix string) string {
	t.Helper()
	var id string
	err := db.QueryRow(ctx,
		`INSERT INTO projects (folder_name) VALUES ($1) RETURNING id::TEXT`,
		"issue12-"+suffix).Scan(&id)
	if err != nil {
		t.Fatalf("insert test project: %v", err)
	}
	return id
}

func TestSessionIntegration_MultiUserJoin(t *testing.T) {
	db, ctx := sessionTestDB(t)
	sessionEnsureSchema(t, ctx, db)
	ss := store.NewSessionStore(db)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	alice := sessionUser(t, ctx, db, "alice_"+suffix)
	bob := sessionUser(t, ctx, db, "bob_"+suffix)
	proj := sessionProject(t, ctx, db, suffix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.Exec(ctx, `DELETE FROM sessions WHERE project_id = $1`, proj)
		_, _ = db.Exec(ctx, `DELETE FROM projects WHERE id = $1`, proj)
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE id IN ($1, $2)`, alice, bob)
	})

	sess, err := ss.Create(ctx, store.SessionParams{ProjectID: proj, Title: "Pairing", CreatedBy: alice})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !sess.IsActive {
		t.Fatal("new session must be active")
	}

	// Creator is seated as OWNER by Create.
	parts, err := ss.ListParticipants(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(parts) != 1 || parts[0].UserID != alice || parts[0].Role != store.RoleOwner {
		t.Fatalf("creator must be seated OWNER, got %+v", parts)
	}

	// Bob joins; both seats listed.
	if _, err := ss.Join(ctx, sess.ID, store.JoinParams{UserID: bob}); err != nil {
		t.Fatalf("bob join: %v", err)
	}
	parts, err = ss.ListParticipants(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("multi-user join must list 2 seats, got %+v", parts)
	}

	// Idempotent re-join: same row, still 2 seats.
	dup, err := ss.Join(ctx, sess.ID, store.JoinParams{UserID: bob, Role: "MEMBER"})
	if err != nil {
		t.Fatalf("re-join: %v", err)
	}
	if dup.UserID != bob {
		t.Fatalf("re-join must return bob's seat, got %+v", dup)
	}

	// Bob leaves; only Alice remains.
	if _, err := ss.Leave(ctx, sess.ID, bob, ""); err != nil {
		t.Fatalf("bob leave: %v", err)
	}
	parts, err = ss.ListParticipants(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	if len(parts) != 1 || parts[0].UserID != alice {
		t.Fatalf("after leave only alice must remain, got %+v", parts)
	}

	// End closes the session and releases Alice's seat.
	ended, err := ss.End(ctx, sess.ID)
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.IsActive || ended.EndedAt == nil {
		t.Fatalf("ended session must be inactive with ended_at: %+v", ended)
	}
	active, err := ss.ListActive(ctx, proj)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	for _, s := range active {
		if s.ID == sess.ID {
			t.Fatal("ended session must not list as active")
		}
	}
}

func TestSessionIntegration_IsolationUntilPromoted(t *testing.T) {
	db, ctx := sessionTestDB(t)
	sessionEnsureSchema(t, ctx, db)
	ss := store.NewSessionStore(db)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user := sessionUser(t, ctx, db, "iso_"+suffix)
	proj := sessionProject(t, ctx, db, "iso_"+suffix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.Exec(ctx, `DELETE FROM memory_items WHERE project_id = $1`, proj)
		_, _ = db.Exec(ctx, `DELETE FROM sessions WHERE project_id = $1`, proj)
		_, _ = db.Exec(ctx, `DELETE FROM projects WHERE id = $1`, proj)
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
	})

	mkSession := func(title string) string {
		s, err := ss.Create(ctx, store.SessionParams{ProjectID: proj, Title: title, CreatedBy: user})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return s.ID
	}
	s1, s2, s3 := mkSession("s1"), mkSession("s2"), mkSession("s3")

	seed := func(sessionID, key string) {
		content := "session memory content for key " + key + " recorded here"
		_, err := db.Exec(ctx,
			`INSERT INTO memory_items (project_id, session_id, key, content, level, scope, status)
			 VALUES ($1, $2, $3, $4, 'session', 'fact', 'CONFIRMED')`,
			proj, sessionID, key, content)
		if err != nil {
			t.Fatalf("seed memory: %v", err)
		}
	}
	seed(s1, "testing/framework")
	seed(s2, "testing/framework")
	seed(s3, "testing/framework")

	// Promotion signal: same key in 3 sessions.
	n, err := ss.CountKeySessionsDB(ctx, proj, "testing/framework")
	if err != nil {
		t.Fatalf("count key sessions: %v", err)
	}
	if n != 3 || !store.ShouldProposePromotion(n) {
		t.Fatalf("count = %d, want 3 crossing the promotion threshold", n)
	}

	// Isolation: s1's row is invisible inside s2 (pure predicate over the
	// fetched rows mirrors the read-path WHERE clause).
	rows, err := db.Query(ctx,
		`SELECT key, session_id::TEXT FROM memory_items WHERE project_id = $1`, proj)
	if err != nil {
		t.Fatalf("fetch memories: %v", err)
	}
	defer rows.Close()
	var items []store.MemoryItem
	for rows.Next() {
		var it store.MemoryItem
		if err := rows.Scan(&it.Key, &it.SessionID); err != nil {
			t.Fatalf("scan memory: %v", err)
		}
		it.Level = store.LevelSession
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("memory rows: %v", err)
	}
	visible := store.FilterVisibleToSession(items, s2)
	if len(visible) != 1 || visible[0].SessionID != s2 {
		t.Fatalf("s2 must see only its own session row, got %+v", visible)
	}

	// Promote: clear session_id, flip to project — now every session inherits it.
	if _, err := db.Exec(ctx,
		`UPDATE memory_items SET session_id = NULL, level = 'project'
		 WHERE project_id = $1 AND key = $2`, proj, "testing/framework"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	rows2, err := db.Query(ctx,
		`SELECT key FROM memory_items WHERE project_id = $1 AND session_id IS NULL`, proj)
	if err != nil {
		t.Fatalf("fetch promoted: %v", err)
	}
	defer rows2.Close()
	promoted := 0
	for rows2.Next() {
		var k string
		if err := rows2.Scan(&k); err != nil {
			t.Fatalf("scan promoted: %v", err)
		}
		promoted++
	}
	if promoted != 3 {
		t.Fatalf("promoted rows = %d, want 3 project-visible", promoted)
	}
}
