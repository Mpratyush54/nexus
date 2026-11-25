package desktopbridge

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"central-memory/internal/store"
)

// This test uses an explicitly supplied disposable database, never the
// production DSN. Its generated schema is isolated from all existing tables.
func TestPostgresDurableExecution(t *testing.T) {
	dsn := os.Getenv("NEXUS_DESKTOP_TEST_DSN")
	if dsn == "" {
		// CI's disposable Postgres service (see .github/workflows/ci.yml).
		dsn = os.Getenv("TEST_POSTGRES_DSN")
	}
	if dsn == "" {
		t.Skip("set NEXUS_DESKTOP_TEST_DSN or TEST_POSTGRES_DSN to a disposable local Postgres")
	}
	ctx := context.Background()
	admin, e := store.NewPostgresStore(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := fmt.Sprintf("nexus_desktop_test_%d", time.Now().UnixNano())
	if _, e = admin.DB().Exec(ctx, `CREATE SCHEMA `+schema); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if !strings.HasPrefix(schema, "nexus_desktop_test_") {
			t.Fatal("unsafe test schema")
		}
		_, _ = admin.DB().Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	}()
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := store.NewPostgresStore(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	for _, name := range []string{"050_desktop_execution.up.sql", "053_desktop_attachments.up.sql"} {
		sql, e := os.ReadFile("../../migrations/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.DB().Exec(ctx, string(sql)); e != nil {
			t.Fatal(e)
		}
	}
	repo := NewPostgres(db.DB())
	now := time.Now().UTC()
	d := Device{ID: "device", Owner: "owner", KeyHash: HashKey("test-secret"), Name: "Desktop", Projects: []string{"project"}, Harnesses: []string{"codex"}, Seen: now}
	if e = repo.Register(ctx, d); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Device(ctx, "other", d.ID); e != ErrNotFound {
		t.Fatal("cross-account device", e)
	}
	j := Job{Request: Request{ID: "job", DeviceID: d.ID, Kind: "turn", ProjectID: "project", SessionID: "session", Agent: "codex", Prompt: "hello"}, Owner: d.Owner, State: "queued", Revision: 1, Created: now, Updated: now, Expires: now.Add(time.Minute)}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := j
			candidate.ID = fmt.Sprint("job-", i)
			if _, e := repo.Create(ctx, candidate); e == nil {
				admitted.Add(1)
			} else if e != ErrConflict {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("concurrent admission: %d", admitted.Load())
	}
	claimed, e := repo.Claim(ctx, d.Owner, d.ID, now)
	if e != nil || claimed == nil {
		t.Fatal("claim", e)
	}
	if claimed.State != "awaiting_approval" || claimed.Revision != 2 {
		t.Fatal("claim data", claimed)
	}
	if next, e := repo.Claim(ctx, d.Owner, d.ID, now); e != nil || next != nil {
		t.Fatal("duplicate claim", e)
	}
	running, e := repo.Update(ctx, d.Owner, d.ID, claimed.ID, Update{Revision: claimed.Revision, State: "running", Text: "partial"}, now)
	if e != nil {
		t.Fatal("running", e)
	}
	// A new repository instance sees the durable progress, not process memory.
	restarted := NewPostgres(db.DB())
	saved, e := restarted.Get(ctx, d.Owner, claimed.ID)
	if e != nil || saved.Text != "partial" || saved.Revision != running.Revision {
		t.Fatal("not durable", saved, e)
	}
	if _, e = restarted.Cancel(ctx, d.Owner, claimed.ID, now); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Update(ctx, d.Owner, d.ID, claimed.ID, Update{Revision: running.Revision, State: "completed"}, now); e != ErrConflict {
		t.Fatal("late completion overwrote cancellation", e)
	}
	j.ID = "after-cancel"
	if _, e = repo.Create(ctx, j); e != nil {
		t.Fatal("slot not freed", e)
	}
	if _, e = repo.Create(ctx, j); e != nil {
		t.Fatal("idempotent retry", e)
	}
}
