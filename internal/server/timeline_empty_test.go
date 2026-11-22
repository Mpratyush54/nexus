package server

import (
	"net/http"
	"net/url"
	"testing"

	"central-memory/internal/store"
)

// Timeline reports each chat's real turn count (mirrored chats have turns
// but no version), and hide_empty=1 drops chats that have none, modern or
// archived.
func TestTimelineTurnCountAndHideEmpty(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "empty-chats")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	full, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "real-chat"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: full.ID, Idx: i, Role: "user", TextPreview: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "cursor", NativeID: "state"})
	if err != nil {
		t.Fatal(err)
	}
	for id, n := range map[string]int{"legacy-full": 4, "legacy-empty": 0} {
		if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
			SessionID: id, ProjectID: project.ID, OwnerUserID: "owner",
			Harness: "claude", ConversationID: id, TurnCount: n,
		}); err != nil {
			t.Fatal(err)
		}
	}
	token := loginAs(t, s, "owner")
	list := func(extra string) map[string]int {
		t.Helper()
		rec := doJSON(t, s, http.MethodGet, "/v1/timeline?project_id="+url.QueryEscape(project.ID)+extra, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("timeline: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []store.TimelineItem `json:"items"`
		}
		decodeBody(t, rec, &body)
		out := map[string]int{}
		for _, item := range body.Items {
			out[item.SessionID] = item.TurnCount
		}
		return out
	}
	all := list("")
	if len(all) != 4 || all[full.ID] != 3 || all[empty.ID] != 0 || all["legacy-full"] != 4 || all["legacy-empty"] != 0 {
		t.Fatalf("turn counts: %+v", all)
	}
	shown := list("&hide_empty=1")
	if len(shown) != 2 || shown[full.ID] != 3 || shown["legacy-full"] != 4 {
		t.Fatalf("hide_empty: %+v", shown)
	}
}
