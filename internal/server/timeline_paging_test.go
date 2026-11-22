package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

type timelinePage struct {
	Items []store.TimelineItem `json:"items"`
	Next  string               `json:"next_cursor"`
}

// walkTimeline follows next_cursor from the first page and returns every
// item in order, failing on a duplicate, an out-of-order item or a loop.
func walkTimeline(t *testing.T, s *Server, token, query string, limit int) ([]store.TimelineItem, int) {
	t.Helper()
	var all []store.TimelineItem
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for ; pages < 200; pages++ {
		path := "/v1/timeline?limit=" + strconv.Itoa(limit) + query
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := doJSON(t, s, http.MethodGet, path, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("timeline page %d: %d %s", pages, rec.Code, rec.Body.String())
		}
		var page timelinePage
		decodeBody(t, rec, &page)
		if len(page.Items) > limit {
			t.Fatalf("page %d has %d items, limit %d", pages, len(page.Items), limit)
		}
		for _, item := range page.Items {
			if seen[item.SessionID] {
				t.Fatalf("session %s listed twice (limit %d)", item.SessionID, limit)
			}
			seen[item.SessionID] = true
			if n := len(all); n > 0 && !timelineNewer(all[n-1].LastActiveAt, all[n-1].SessionID, item.LastActiveAt, item.SessionID) {
				t.Fatalf("session %s out of order after %s (limit %d)", item.SessionID, all[n-1].SessionID, limit)
			}
			all = append(all, item)
		}
		if page.Next == "" {
			return all, pages + 1
		}
		if page.Next == cursor {
			t.Fatalf("cursor did not advance: %q", cursor)
		}
		cursor = page.Next
	}
	t.Fatalf("timeline did not end after %d pages", pages)
	return nil, 0
}

func timelineIDs(items []store.TimelineItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.SessionID
	}
	return out
}

// Archived snapshots are paged in the store together with agent sessions:
// every page size lists the same chats in the same order as one big page,
// and only snapshots the viewer owns or was granted, in projects the viewer
// can open, appear.
func TestTimelinePagesLegacyAndModernWithSnapshotACL(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "paging-acl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := mem.GrantMember(ctx, project.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	elsewhere, err := mem.ResolveProject(ctx, "", "", "paging-acl-elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, elsewhere.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	snapshot := func(id, projectID, owner string) {
		t.Helper()
		if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
			SessionID: id, ProjectID: projectID, OwnerUserID: owner,
			Harness: "codex", ConversationID: id, TurnCount: 3,
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct, ordered timestamps
	}
	for i := 0; i < 7; i++ {
		snapshot(fmt.Sprintf("bob-own-%d", i), project.ID, "bob")
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("alice-granted-%d", i)
		snapshot(id, project.ID, "alice")
		if err := mem.GrantSessionContent(ctx, id, project.ID, "bob", "alice"); err != nil {
			t.Fatal(err)
		}
	}
	snapshot("alice-private-0", project.ID, "alice")
	snapshot("alice-private-1", project.ID, "alice")
	snapshot("ownerless", project.ID, "")
	// A grant in a project bob cannot open does not list the snapshot.
	snapshot("alice-elsewhere", elsewhere.ID, "alice")
	if err := mem.GrantSessionContent(ctx, "alice-elsewhere", elsewhere.ID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	// Two versions of one session list once, at the newer version's time.
	snapshot("bob-own-3", project.ID, "bob")
	modern := map[string]bool{}
	for i := 0; i < 4; i++ {
		native := fmt.Sprintf("bob-new-%d", i)
		if i == 0 {
			native = "bob-own-0" // re-captured: hides that snapshot on every page
		}
		row, err := mem.UpsertAgentSession(ctx, &store.AgentSession{
			ProjectID: project.ID, OwnerUserID: "bob", Harness: "codex", NativeID: native, OriginMachineID: "pc",
		})
		if err != nil {
			t.Fatal(err)
		}
		modern[row.ID] = true
		time.Sleep(time.Millisecond)
	}
	bob := loginAs(t, s, "bob")
	want, _ := walkTimeline(t, s, bob, "", 100)
	wantIDs := map[string]bool{}
	for _, id := range timelineIDs(want) {
		wantIDs[id] = true
	}
	for _, id := range []string{"bob-own-1", "bob-own-2", "bob-own-3", "bob-own-4", "bob-own-5", "bob-own-6",
		"alice-granted-0", "alice-granted-1", "alice-granted-2"} {
		if !wantIDs[id] {
			t.Errorf("bob's timeline is missing %s", id)
		}
	}
	for id := range modern {
		if !wantIDs[id] {
			t.Errorf("bob's timeline is missing modern session %s", id)
		}
	}
	for _, id := range []string{"bob-own-0", "alice-private-0", "alice-private-1", "ownerless", "alice-elsewhere"} {
		if wantIDs[id] {
			t.Errorf("bob's timeline lists %s", id)
		}
	}
	if len(want) != 13 {
		t.Fatalf("bob's timeline = %v; want 13 sessions", timelineIDs(want))
	}
	for _, limit := range []int{1, 2, 3, 5, 12, 13, 14} {
		got, _ := walkTimeline(t, s, bob, "", limit)
		if fmt.Sprint(timelineIDs(got)) != fmt.Sprint(timelineIDs(want)) {
			t.Fatalf("limit %d pages = %v; want %v", limit, timelineIDs(got), timelineIDs(want))
		}
	}
	// The project and harness filters page the same way.
	for _, query := range []string{"&project_id=" + url.QueryEscape(project.ID), "&harness=CODEX"} {
		got, _ := walkTimeline(t, s, bob, query, 4)
		legacy := 0
		for _, item := range got {
			if item.VersionState == "legacy_snapshot" {
				legacy++
			}
		}
		if legacy != 9 {
			t.Fatalf("%s: %d snapshots listed, want 9: %v", query, legacy, timelineIDs(got))
		}
	}
	// Alice sees her own snapshots (granted or not) and none of bob's.
	alice, _ := walkTimeline(t, s, loginAs(t, s, "alice"), "", 4)
	aliceIDs := map[string]bool{}
	for _, id := range timelineIDs(alice) {
		aliceIDs[id] = true
	}
	for _, id := range []string{"alice-granted-0", "alice-private-0", "alice-elsewhere"} {
		if !aliceIDs[id] {
			t.Errorf("alice's timeline is missing %s", id)
		}
	}
	for _, id := range []string{"bob-own-1", "ownerless"} {
		if aliceIDs[id] {
			t.Errorf("alice's timeline lists %s", id)
		}
	}
	// Reading a snapshot follows the same rule.
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/alice-granted-1", bob, nil); rec.Code != http.StatusOK {
		t.Fatalf("granted snapshot read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/alice-private-1", bob, nil); rec.Code == http.StatusOK {
		t.Fatal("bob read alice's private snapshot")
	}
}

// A search that runs out of its transcript budget ends the page early and
// resumes from its cursor: walking the pages finds every match exactly once.
func TestTimelineLegacySearchResumesAfterBudget(t *testing.T) {
	oldBudget := legacySearchTranscriptBudget
	legacySearchTranscriptBudget = 3
	defer func() { legacySearchTranscriptBudget = oldBudget }()

	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "search-budget")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	// An older modern match must not be returned ahead of unchecked snapshots.
	modern, err := mem.UpsertAgentSession(ctx, &store.AgentSession{
		ProjectID: project.ID, OwnerUserID: "owner", Harness: "claude", NativeID: "modern-zebra", OriginMachineID: "pc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: modern.ID, Idx: 0, Role: "user", TextPreview: "a zebra in the modern chat"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	matches := map[string]bool{}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("snap-%02d", i)
		text := "nothing to see"
		if i == 0 || i == 1 || i == 7 {
			text = "look, a Zebra here"
			matches[id] = true
		}
		line, _ := json.Marshal(map[string]any{"speaker": "user", "content": text})
		if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
			SessionID: id, ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", ConversationID: id,
			TranscriptPayload: gzipJSONL(t, string(line)),
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	matches[modern.ID] = true
	token := loginAs(t, s, "owner")
	for _, limit := range []int{1, 2, 30} {
		got, pages := walkTimeline(t, s, token, "&q=zebra", limit)
		ids := map[string]bool{}
		for _, item := range got {
			ids[item.SessionID] = true
			if item.VersionState == "legacy_snapshot" && (item.MatchTurnIdx == nil || item.MatchPreview == "") {
				t.Fatalf("snapshot match %s has no preview", item.SessionID)
			}
		}
		if fmt.Sprint(ids) != fmt.Sprint(matches) {
			t.Fatalf("limit %d search found %v; want %v", limit, ids, matches)
		}
		if pages < 4 {
			t.Fatalf("limit %d: %d pages; the budget of 3 transcripts should split the search", limit, pages)
		}
	}
}

// The chat page reads a page of turns, not the session: the result matches
// paging the full turn list, whatever order the turns arrived in.
func TestAgentSessionFetchPagesMatchFullList(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "fetch-pages")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	row, err := mem.UpsertAgentSession(ctx, &store.AgentSession{
		ProjectID: project.ID, OwnerUserID: "owner", Harness: "claude", NativeID: "fetch", OriginMachineID: "pc",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int{4, 0, 9, 2, 7, 1, 3, 8, 6, 5, 12} {
		role := "user"
		if idx%2 == 1 {
			role = "assistant"
		}
		turn := store.SessionTurn{SessionID: row.ID, Idx: idx, Role: role, TextPreview: fmt.Sprintf("turn %d", idx)}
		if idx == 3 {
			turn.ToolCalls = json.RawMessage(`[{"name":"bash","args":{"command":"go test ./internal/server"}}]`)
		}
		if idx == 6 {
			turn.TextPreview = `{"tool_calls":[{"name":"edit","args":{"file_path":"internal/server/x.go"}}]}`
		}
		if err := mem.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	all, err := mem.ListSessionTurns(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := loginAs(t, s, "owner")
	for _, limit := range []int{1, 3, 11, 20} {
		for _, cursor := range []string{"", "0", "4", "9", "12", "40"} {
			var after *int
			if cursor != "" {
				n, _ := strconv.Atoi(cursor)
				after = &n
			}
			wantItems, wantNext := pageSessionTurns(all, after, limit)
			path := fmt.Sprintf("/v1/agent-sessions/%s/fetch?limit=%d", row.ID, limit)
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			rec := doJSON(t, s, http.MethodGet, path, token, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("fetch: %d %s", rec.Code, rec.Body.String())
			}
			var page struct {
				Items []store.SessionTurn `json:"items"`
				Next  string              `json:"next_cursor"`
			}
			decodeBody(t, rec, &page)
			got, want := []int{}, []int{}
			for _, turn := range page.Items {
				got = append(got, turn.Idx)
			}
			for _, turn := range wantItems {
				want = append(want, turn.Idx)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) || page.Next != wantNext {
				t.Fatalf("limit %d cursor %q: got %v next %q; want %v next %q", limit, cursor, got, page.Next, want, wantNext)
			}
		}
	}
	// Summary and commands read only the turns they need, with the same result.
	rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+row.ID+"/summary", token, nil)
	var summary struct {
		Summary string `json:"summary"`
	}
	decodeBody(t, rec, &summary)
	if want := capturedTurnOverview(all); summary.Summary != want {
		t.Fatalf("summary = %q; want %q", summary.Summary, want)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+row.ID+"/operations", token, nil)
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) || !strings.Contains(rec.Body.String(), "go test ./internal/server") {
		t.Fatalf("operations: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+row.ID+"/files", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "internal/server/x.go") {
		t.Fatalf("files: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTranscriptPrefilterIsConservative(t *testing.T) {
	for query, want := range map[string]bool{
		"deploy": true, "x.go": true, "a-b_c": true, "123": false, "two words": false,
		`a"b`: false, `a\b`: false, "a<b": false, "a&b": false, "café": false, "a/b": false, "": false,
	} {
		if got := transcriptPrefilterSafe(query); got != want {
			t.Errorf("transcriptPrefilterSafe(%q) = %v; want %v", query, got, want)
		}
	}
	for _, tc := range []struct {
		lines []string
		query string
		found bool
	}{
		{[]string{`{"speaker":"user","content":"Deploy the API"}`}, "deploy", true},
		{[]string{`{"speaker":"user","content":"nothing"}`}, "deploy", false},
		// Kelvin sign lower-cases to k; the raw bytes never hold "kb".
		{[]string{`{"speaker":"user","content":"5 KB"}`}, "kb", true},
		{[]string{`{"speaker":"user","content":"5 ` + "K" + `B"}`}, "kb", true},
		// Tool turns never match.
		{[]string{`{"speaker":"tool","content":"deploy"}`}, "deploy", false},
	} {
		_, _, _, found := searchLegacyTranscript(gzipJSONL(t, tc.lines...), tc.query)
		if found != tc.found {
			t.Errorf("search %q in %v = %v; want %v", tc.query, tc.lines, found, tc.found)
		}
	}
}
