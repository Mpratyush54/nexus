package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAgentActivityPostgresSourceDateAndDuplicateRepair(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	s, err := NewPostgresStore(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RunMigrations(t.Context(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var owner string
	name := fmt.Sprintf("source-activity-%d", time.Now().UnixNano())
	if err := s.pool.QueryRow(t.Context(), `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(t.Context(), "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID) }()
	row, err := s.UpsertAgentSession(t.Context(), &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: "codex", NativeID: name})
	if err != nil {
		t.Fatal(err)
	}
	turn := SessionTurn{SessionID: row.ID, Idx: 1, Role: "user", TextPreview: "Original source λ date"}
	if err := s.AppendSessionTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -20).Add(21 * time.Hour)
	turn.SourceAt = &at
	if err := s.AppendSessionTurn(t.Context(), turn); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate repair: %v", err)
	}
	stored, err := s.ListSessionTurns(t.Context(), row.ID)
	if err != nil || len(stored) != 1 || stored[0].SourceAt == nil || !stored[0].SourceAt.Equal(at) {
		t.Fatalf("source repair: %+v %v", stored, err)
	}
	days, err := s.ListAgentActivity(t.Context(), project.ID, owner, at.AddDate(0, 0, -1), time.Now().AddDate(0, 0, 1), 330)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Count != 1 || days[0].Date != at.Add(330*time.Minute).Format("2006-01-02") || len(days[0].Fingerprints) != 1 || days[0].Fingerprints[0] != ActivityFingerprint(at, turn.Role, turn.TextPreview) {
		t.Fatalf("source calendar SQL/hash mismatch: %+v", days)
	}
}
