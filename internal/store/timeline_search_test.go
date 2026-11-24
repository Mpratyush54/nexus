package store

import (
	"strings"
	"testing"
)

func TestTimelineSearchFindsUserAndAssistantTextWithoutLeakingPrivateChats(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	project, err := m.ResolveProject(ctx, "", "", "search-project")
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "search-chat", Title: "Unrelated title"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("padding ", 100) + "the UNIQUE phrase is 100% reliable" + strings.Repeat(" ending", 100)
	for _, turn := range []SessionTurn{
		{SessionID: session.ID, Idx: 0, Role: "user", TextPreview: "Question about invisible islands"},
		{SessionID: session.ID, Idx: 1, Role: "assistant", TextPreview: text},
		{SessionID: session.ID, Idx: 2, Role: "tool", TextPreview: "tool-only-secret"},
	} {
		if err := m.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"invisible islands", "unique phrase", "100% reliable"} {
		items, _, err := m.ListTimeline(ctx, TimelineQuery{UserID: "owner", ProjectID: project.ID, Query: q})
		if err != nil || len(items) != 1 {
			t.Fatalf("search %q: %+v %v", q, items, err)
		}
		if items[0].MatchTurnIdx == nil || !strings.Contains(strings.ToLower(items[0].MatchPreview), q) {
			t.Fatalf("missing match excerpt: %+v", items[0])
		}
	}
	if items, _, _ := m.ListTimeline(ctx, TimelineQuery{UserID: "outsider", Query: "unique phrase"}); len(items) != 0 {
		t.Fatal("private transcript leaked through search")
	}
	if items, _, _ := m.ListTimeline(ctx, TimelineQuery{UserID: "owner", Query: "tool-only-secret"}); len(items) != 0 {
		t.Fatal("metadata treated as chat")
	}
	if err := m.GrantAgentSession(ctx, session.ID, "reader", "owner"); err != nil {
		t.Fatal(err)
	}
	if items, _, _ := m.ListTimeline(ctx, TimelineQuery{UserID: "reader", Query: "unique phrase"}); len(items) != 1 {
		t.Fatal("authorized shared conversation missing from search")
	}
}
