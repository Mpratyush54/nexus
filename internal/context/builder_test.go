package context

import (
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func mem(key, level, content string) *store.MemoryItem {
	return &store.MemoryItem{
		Key: key, Level: level, Content: content,
		Scope: "fact", Status: "CONFIRMED", Confidence: 1.0,
		UpdatedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestResolveOverrides(t *testing.T) {
	items := []*store.MemoryItem{
		mem("testing/framework", "organization", "org default"),
		mem("testing/framework", "project", "project choice"),
		mem("testing/framework", "personal", "personal pref"),
		mem("testing/framework", "session", "session pin"),
		mem("other/key", "project", "project only"),
	}
	got := ResolveOverrides(items)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Content != "session pin" {
		t.Fatalf("winner = %q (%s), want session pin", got[0].Content, got[0].Level)
	}
	if got[1].Key != "other/key" {
		t.Fatalf("second = %q, want other/key", got[1].Key)
	}

	// Personal beats project without a session present.
	got = ResolveOverrides(items[1:3])
	if len(got) != 1 || got[0].Level != "personal" {
		t.Fatalf("personal should beat project: %+v", got)
	}
}

func TestAssembleXMLBudgetTruncation(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	in := ContextInput{
		ProjectName: "central-memory",
		Branch:      "main",
		Task:        &store.Task{Title: "Add WebSocket auth", Status: "IN_PROGRESS", Description: "Verify JWT on upgrade."},
		SessionItems: []*store.MemoryItem{
			mem("scope/do_not_touch", "session", "Do not modify payments directory during this session right now."),
		},
		PersonalItems: []*store.MemoryItem{mem("style/errors", "personal", "Alice prefers detailed error messages with full stack context.")},
		ProjectItems: []*store.MemoryItem{
			mem("testing/framework", "project", "The team uses pytest with fixture-based setup and testcontainers for Postgres."),
			mem("db/pool", "project", "Pool max connections is twenty with a thirty minute lifetime for websockets."),
			mem("cache/choice", "project", "Team chose Redis over Memcached for pubsub support in caching layer."),
		},
		OrgItems: []*store.MemoryItem{mem("security/auth", "organization", "All APIs must use JWT authentication and never use API key auth.")},
		Budget:   4000,
		Now:      now,
	}
	full := AssembleXML(in)
	if full.TokenCount != len(full.XML) {
		t.Fatalf("token_count %d != len(xml) %d", full.TokenCount, len(full.XML))
	}
	if full.BudgetRemaining != 4000-len(full.XML) {
		t.Fatalf("budget_remaining = %d, want %d", full.BudgetRemaining, 4000-len(full.XML))
	}

	// Tight budget: task always survives, low-priority org section drops first.
	tight := AssembleXML(ContextInput{
		ProjectName: in.ProjectName, Branch: in.Branch, Task: in.Task,
		SessionItems: in.SessionItems, ProjectItems: in.ProjectItems,
		OrgItems: in.OrgItems, Budget: 700, Now: now,
	})
	if !strings.Contains(tight.XML, "Add WebSocket auth") {
		t.Error("tight budget dropped the active task")
	}
	if strings.Contains(tight.XML, "security/auth") {
		t.Error("tight budget should drop low-priority org items first")
	}
	if tight.TokenCount > 700 && len(in.Task.Description) < 700 {
		t.Errorf("budget overflow without justification: %d > 700", tight.TokenCount)
	}
	if !strings.HasPrefix(tight.XML, "<project_memory") || !strings.HasSuffix(tight.XML, "</project_memory>") {
		t.Error("truncated output is not well-formed at root level")
	}

	// Priority order: task, session, episodes, personal, project, org.
	ordered := AssembleXML(in)
	idx := func(s string) int { return strings.Index(ordered.XML, s) }
	if !(idx("active_task") < idx("<session") && idx("<session") < idx("<project>") && idx("<project>") < idx("<organization>")) {
		t.Error("sections out of priority order")
	}
}

func TestAssembleXMLSessionOverridesProject(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	in := ContextInput{
		ProjectName: "p", Branch: "main",
		SessionItems: []*store.MemoryItem{mem("testing/framework", "session", "Use unittest just for this spike session work.")},
		ProjectItems: []*store.MemoryItem{mem("testing/framework", "project", "The team uses pytest with fixture-based setup everywhere.")},
		Budget: 4000, Now: now,
	}
	got := AssembleXML(in)
	if strings.Contains(got.XML, "pytest") {
		t.Error("shadowed project memory leaked into XML")
	}
	if !strings.Contains(got.XML, "unittest") {
		t.Error("winning session memory missing from XML")
	}
	if got.ItemsIncluded != 1 {
		t.Errorf("items_included = %d, want 1", got.ItemsIncluded)
	}
}
