package store

import (
	"testing"
	"time"
)

func TestPruneSessionVersionsKeepsRecent(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	row, err := m.UpsertAgentSession(ctx, &AgentSession{
		ProjectID: "p", OwnerUserID: "u", Harness: "claude", NativeID: "n",
	})
	if err != nil {
		t.Fatal(err)
	}
	old, err := m.CreateSessionVersion(ctx, row.ID, "u")
	if err != nil {
		t.Fatal(err)
	}
	older, err := m.CreateSessionVersion(ctx, row.ID, "u")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2020, 1, 2, 15, 0, 0, 0, time.UTC)
	m.mu.Lock()
	for i := range m.sessionVersions[row.ID] {
		if m.sessionVersions[row.ID][i].ID == old.ID {
			m.sessionVersions[row.ID][i].CreatedAt = base
		}
		if m.sessionVersions[row.ID][i].ID == older.ID {
			m.sessionVersions[row.ID][i].CreatedAt = base.Add(time.Hour)
		}
	}
	m.mu.Unlock()
	n, err := m.PruneSessionVersions(ctx, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dropped %d, want 1 (same UTC day keeps the latest)", n)
	}
	left, err := m.ListSessionVersions(ctx, row.ID)
	if err != nil || len(left) != 1 || left[0].ID != older.ID {
		t.Fatalf("left=%v err=%v", left, err)
	}
}
