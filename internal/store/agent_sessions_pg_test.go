package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAgentSessionPostgresRoundTrip(t *testing.T) {
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

	var ownerID, projectID string
	err = s.pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no users")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM projects LIMIT 1`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	native := "ses_pg_roundtrip"
	cleanup := func() {
		bg := context.Background()
		var id string
		_ = s.pool.QueryRow(bg, `
			SELECT id::text FROM agent_sessions
			WHERE project_id = $1::uuid AND native_id = $2`, projectID, native).Scan(&id)
		if id != "" {
			_, _ = s.pool.Exec(bg, `DELETE FROM agent_sessions WHERE id = $1::uuid`, id)
		}
		_, _ = s.pool.Exec(bg, `DELETE FROM blobs WHERE project_id = $1::uuid AND sha256 LIKE 'aa%'`, projectID)
	}
	cleanup()
	t.Cleanup(cleanup)

	row, err := s.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: projectID, OwnerUserID: ownerID, Harness: "claude",
		NativeID: native, OriginMachineID: "m1", Title: "pg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.NativeID != native || !looksLikeUUID(row.ID) {
		t.Fatalf("row %+v", row)
	}
	ok, err := s.CanReadAgentSession(ctx, ownerID, row.ID)
	if err != nil || !ok {
		t.Fatalf("owner read %v %v", ok, err)
	}
	body := []byte("hello-cloud")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	missing, err := s.MissingBlobs(ctx, projectID, []string{hash})
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	if err := s.PutBlob(ctx, projectID, hash, "plain", "transcript", body); err != nil {
		t.Fatal(err)
	}
	ver, err := s.CreateSessionVersion(ctx, row.ID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"transcript":{"blob":"sha256:` + hash + `"}}`)
	done, err := s.CompleteSessionVersion(ctx, row.ID, ver.Version, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != "complete" {
		t.Fatalf("state %s", done.State)
	}
	if _, err := s.CompleteSessionVersion(ctx, row.ID, ver.Version, manifest); !errors.Is(err, ErrConflict) {
		t.Fatalf("second complete err=%v", err)
	}
	items, _, err := s.ListTimeline(ctx, TimelineQuery{UserID: ownerID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.NativeID == native {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline missing session: %+v", items)
	}
}
