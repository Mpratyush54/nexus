package server

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"central-memory/internal/store"
)

// The timeline leaves sub-agent runs out (modern and archived) unless
// include_subagents=1, pages over the listed rows only, and the upsert route
// records the parent the daemon sends.
func TestTimelineSubagentRuns(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "subagent-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	token := loginAs(t, s, "owner")
	upsert := func(native, parent string) string {
		t.Helper()
		body := map[string]any{"project_id": project.ID, "harness": "claude", "native_id": native, "origin_machine_id": "m1"}
		if parent != "" {
			body["parent_native_id"] = parent
		}
		rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", token, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("upsert %s: %d %s", native, rec.Code, rec.Body.String())
		}
		var row store.AgentSession
		decodeBody(t, rec, &row)
		return row.ID
	}
	parentNative := "0f8e2c1a-6d2b-4f6e-9a51-3b7c2d1e0f9a"
	real := []string{upsert(parentNative, ""), upsert("5b1f0e7d-2a3c-4d5e-8f90-1a2b3c4d5e6f", "")}
	for i := 0; i < 5; i++ {
		upsert(fmt.Sprintf("agent-%017x", i+1), parentNative)
	}
	// An archived snapshot of a sub-agent run is left out too.
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: "legacy-sub", ProjectID: project.ID, OwnerUserID: "owner",
		Harness: "claude", ConversationID: "agent-abcdef0123456789", TurnCount: 2,
	}); err != nil {
		t.Fatal(err)
	}

	list := func(extra string) ([]store.TimelineItem, string) {
		t.Helper()
		rec := doJSON(t, s, http.MethodGet, "/v1/timeline?project_id="+url.QueryEscape(project.ID)+extra, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("timeline: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []store.TimelineItem `json:"items"`
			Next  string               `json:"next_cursor"`
		}
		decodeBody(t, rec, &body)
		return body.Items, body.Next
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		extra := "&limit=1"
		if cursor != "" {
			extra += "&cursor=" + url.QueryEscape(cursor)
		}
		items, next := list(extra)
		for _, it := range items {
			if it.LineageKind == "subagent" || store.IsSubagentRun(it.NativeID, it.Title, "") {
				t.Fatalf("sub-agent run listed: %+v", it)
			}
			if it.SessionID == real[0] && it.SubagentCount != 5 {
				t.Fatalf("parent subagent_count = %d", it.SubagentCount)
			}
			seen[it.SessionID] = true
		}
		if cursor = next; cursor == "" {
			break
		}
	}
	if len(seen) != 2 || !seen[real[0]] || !seen[real[1]] {
		t.Fatalf("default timeline = %v", seen)
	}
	all, _ := list("&include_subagents=1")
	subs := 0
	for _, it := range all {
		if it.LineageKind == "subagent" {
			subs++
			if it.SessionID != "legacy-sub" && it.ParentSessionID != real[0] {
				t.Fatalf("run without parent link: %+v", it)
			}
		}
	}
	if len(all) != 8 || subs != 6 {
		t.Fatalf("include_subagents: %d rows, %d marked", len(all), subs)
	}
}
