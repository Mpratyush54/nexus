package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const hmExternal = `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>`

func TestRequestTextStripsExternalHarnessTags(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"only external tag", hmExternal, ""},
		{"external tag then request", hmExternal + "\nship the deploy fix", "ship the deploy fix"},
		{"request then external tag", "ship it\n<external_codex_note kind=\"x\">\nmeta\n</external_codex_note>", "ship it"},
		{"self-closing", "<external_codex_apps_open_page page_id=\"1\"/>\nopen the page", "open the page"},
		{"other external tag", "<external_tool_state>{}</external_tool_state>", ""},
		{"several tags only", hmExternal + "\n<external_codex_ping/>\n" + hmExternal, ""},
		{"cut mid tag", "<external_codex_apps_open_page>{\"page_", ""},
		{"flattened", hmExternal + " fix the header", "fix the header"},
	} {
		if got := requestText(tc.raw); got != tc.want {
			t.Errorf("%s: requestText = %q; want %q", tc.name, got, tc.want)
		}
	}
	// Quoted in code it is text, not a wrapper.
	for _, raw := range []string{"see `" + hmExternal + "` here", "```\n" + hmExternal + "\n```"} {
		if got := requestText(raw); got != raw {
			t.Errorf("requestText(%q) = %q; want unchanged", raw, got)
		}
	}
	if got := timelineFirstRequestPreview([]string{hmExternal, hmEnvironment, "real question"}); got != "real question" {
		t.Fatalf("falls to the next user turn: %q", got)
	}
}

func TestChatTitles(t *testing.T) {
	for _, title := range []string{"", "Captured codex session", "captured claude snapshot", "New chat", "Codex chat", "Claude Code chat", "  untitled chat "} {
		if !IsPlaceholderTitle(title) {
			t.Errorf("%q should be a placeholder", title)
		}
	}
	for _, title := range []string{"Team chat", "Fix deploy", "Captured codex session notes"} {
		if IsPlaceholderTitle(title) {
			t.Errorf("%q is not a placeholder", title)
		}
	}
	long := "please refactor the deployment pipeline so that staging and production share one config file"
	if got := TitleFromRequest(long); got != "please refactor the deployment pipeline so that staging and…" {
		t.Fatalf("long title = %q", got)
	}
	if got := TitleFromRequest(hmDesktop); got != "this part also needs to be revamped" {
		t.Fatalf("wrapped title = %q", got)
	}
	if got := TitleFromRequest("first line\nsecond line"); got != "first line" {
		t.Fatalf("first line = %q", got)
	}
	if got := TitleFromRequest(hmExternal); got != "" {
		t.Fatalf("wrapper title = %q", got)
	}
	turns := []string{hmExternal, hmAgents, "fix the build"}
	if got := PresentTitle("Captured codex session", turns); got != "fix the build" {
		t.Fatalf("present = %q", got)
	}
	if got := PresentTitle("My own name", turns); got != "My own name" {
		t.Fatalf("user title changed: %q", got)
	}
	if got := PresentTitle("New chat", []string{hmExternal}); got != "New chat" {
		t.Fatalf("no request keeps placeholder: %q", got)
	}
}

func TestMemChatTitleFromFirstRequest(t *testing.T) {
	m := NewMemStore()
	ctx := t.Context()
	project, err := m.ResolveProject(ctx, "", "", "title-project")
	if err != nil {
		t.Fatal(err)
	}
	// A row stored with a placeholder before this change: presented, not rewritten.
	legacy, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "old", Title: "Captured codex session"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sessionTurns[legacy.ID] = []SessionTurn{{SessionID: legacy.ID, Idx: 0, Role: "user", TextPreview: hmExternal},
		{SessionID: legacy.ID, Idx: 1, Role: "user", TextPreview: "why is deploy slow"}}
	m.mu.Unlock()
	// A new capture takes its title from the first real request.
	fresh, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "new", Title: "Captured codex session"})
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{hmExternal, hmDesktop, "later request"} {
		if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: fresh.ID, Idx: i, Role: "user", TextPreview: text}); err != nil {
			t.Fatal(err)
		}
	}
	// The capture resends its placeholder: the derived title stays.
	again, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "new", Title: "Captured codex session"})
	if err != nil || again.Title != "this part also needs to be revamped" {
		t.Fatalf("stored title = %q %v", again.Title, err)
	}
	named, err := m.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "named", Title: "Release plan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSessionTurn(ctx, SessionTurn{SessionID: named.ID, Idx: 0, Role: "user", TextPreview: "something else"}); err != nil {
		t.Fatal(err)
	}
	items, _, err := m.ListTimeline(ctx, TimelineQuery{UserID: "owner", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{legacy.ID: "why is deploy slow", fresh.ID: "this part also needs to be revamped", named.ID: "Release plan"}
	for _, it := range items {
		if it.Title != want[it.SessionID] {
			t.Errorf("%s title = %q; want %q", it.NativeID, it.Title, want[it.SessionID])
		}
	}
	if turns, _ := m.FirstUserTurns(ctx, legacy.ID); PresentTitle("Captured codex session", turns) != "why is deploy slow" {
		t.Fatalf("first user turns = %q", turns)
	}
	if m.agentSessions[legacy.ID].Title != "Captured codex session" {
		t.Fatalf("read must not rewrite the stored title")
	}
}

// Titles and both search paths against a real database.
func TestPostgresChatTitlesAndTurnSearch(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no isolated TEST_POSTGRES_DSN")
	}
	ctx := t.Context()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrations := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrations == "" {
		migrations = "../../migrations"
	}
	if err := s.RunMigrations(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("codex-title-%d", time.Now().UnixNano())
	var owner string
	if err := s.pool.QueryRow(ctx, `INSERT INTO users(username) VALUES ($1) RETURNING id::text`, name).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) }()
	project, err := s.ResolveProject(ctx, "", "", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1::uuid`, project.ID) }()
	upsert := func(native, title string) *AgentSession {
		t.Helper()
		row, err := s.UpsertAgentSession(ctx, &AgentSession{ProjectID: project.ID, OwnerUserID: owner, Harness: "codex", NativeID: name + "-" + native, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	turn := func(id string, idx int, role, text string) {
		t.Helper()
		if err := s.AppendSessionTurn(ctx, SessionTurn{SessionID: id, Idx: idx, Role: role, TextPreview: text}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := upsert("fresh", "Captured codex session")
	turn(fresh.ID, 0, "user", hmExternal)
	turn(fresh.ID, 1, "user", "Deploy_the 100% build please")
	turn(fresh.ID, 2, "assistant", "done")
	if again := upsert("fresh", "Captured codex session"); again.Title != "Deploy_the 100% build please" {
		t.Fatalf("stored title = %q", again.Title)
	}
	// A row stored before this change keeps its placeholder; reads present it.
	legacy := upsert("legacy", "Captured codex session")
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_turns (session_id, idx, role, ts, text_preview, tool_calls) VALUES ($1::uuid, 5, 'user', now(), $2, '[]')`,
		legacy.ID, hmExternal+"\nwhy does the deploy hang"); err != nil {
		t.Fatal(err)
	}
	named := upsert("named", "Release plan")
	turn(named.ID, 0, "user", "unrelated words")
	if renamed := upsert("named", "Captured codex session"); renamed.Title != "Release plan" {
		t.Fatalf("placeholder replaced a user title: %q", renamed.Title)
	}

	titles := func(query string) map[string]string {
		t.Helper()
		items, _, err := s.ListTimeline(ctx, TimelineQuery{UserID: owner, ProjectID: project.ID, Query: query, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, it := range items {
			out[it.SessionID] = it.Title
		}
		return out
	}
	all := titles("")
	if all[fresh.ID] != "Deploy_the 100% build please" || all[legacy.ID] != "why does the deploy hang" || all[named.ID] != "Release plan" {
		t.Fatalf("titles = %v", all)
	}
	var stored string
	_ = s.pool.QueryRow(ctx, `SELECT title FROM agent_sessions WHERE id=$1::uuid`, legacy.ID).Scan(&stored)
	if stored != "Captured codex session" {
		t.Fatalf("read rewrote the stored title: %q", stored)
	}
	got, err := s.FirstUserTurns(ctx, legacy.ID)
	if err != nil || PresentTitle(stored, got) != "why does the deploy hang" {
		t.Fatalf("first user turns = %q %v", got, err)
	}
	// Indexed path (3+ chars): turn text, case-insensitive.
	if hits := titles("DEPLOY"); len(hits) != 2 || hits[named.ID] != "" {
		t.Fatalf("deploy hits = %v", hits)
	}
	if hits := titles("unrelated"); len(hits) != 1 || hits[named.ID] == "" {
		t.Fatalf("unrelated hits = %v", hits)
	}
	// LIKE metacharacters are literal.
	if hits := titles("100%"); len(hits) != 1 || hits[fresh.ID] == "" {
		t.Fatalf("100%% hits = %v", hits)
	}
	if hits := titles("y_the"); len(hits) != 1 || hits[fresh.ID] == "" {
		t.Fatalf("y_the hits = %v", hits)
	}
	if hits := titles("x%z"); len(hits) != 0 {
		t.Fatalf("x%%z hits = %v", hits)
	}
	// Short q keeps the per-session probe.
	if hits := titles("0%"); len(hits) != 1 || hits[fresh.ID] == "" {
		t.Fatalf("short hits = %v", hits)
	}
	if hits := titles(strings.ToUpper("release")); len(hits) != 1 {
		t.Fatalf("title hits = %v", hits)
	}
}
