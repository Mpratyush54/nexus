package store

import (
	"context"
	"testing"
	"time"
)

// Importing old history must not make old chats look new.
func TestSessionActivityUsesSourceTime(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	sess, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: "p", OwnerUserID: "u", Harness: "claude", NativeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	older := old.Add(-time.Hour)
	for i, at := range []time.Time{old, older} {
		src := at
		if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: i + 1, Role: "user", TextPreview: "x", SourceAt: &src}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: "p", OwnerUserID: "u", Harness: "claude", NativeID: "n1"})
	if !got.LastActiveAt.Equal(old) {
		t.Fatalf("last_active_at = %v, want the newest source time %v", got.LastActiveAt, old)
	}
	// A new message (no source time: written now) moves it forward.
	if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: sess.ID, Idx: 3, Role: "user", TextPreview: "y"}); err != nil {
		t.Fatal(err)
	}
	got, _ = m.UpsertAgentSession(ctx, &AgentSession{ProjectID: "p", OwnerUserID: "u", Harness: "claude", NativeID: "n1"})
	if time.Since(got.LastActiveAt) > time.Minute {
		t.Fatalf("new turn did not bump activity: %v", got.LastActiveAt)
	}
}
