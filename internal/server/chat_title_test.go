package server

import (
	"net/http"
	"testing"

	"central-memory/internal/store"
)

// A chat stored with a placeholder title is presented by its first request
// on the session page; the stored title and user-set titles are kept.
func TestAgentSessionGetPresentsPlaceholderTitle(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "titled-chats")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	upsert := func(native, title string) *store.AgentSession {
		t.Helper()
		row, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: native, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: row.ID, Idx: 0, Role: "assistant", TextPreview: "hi"}); err != nil {
			t.Fatal(err)
		}
		return row
	}
	captured := upsert("captured", "Captured codex session")
	named := upsert("named", "Release plan")
	token := loginAs(t, s, "owner")
	get := func(id string) string {
		t.Helper()
		rec := doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+id, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
		}
		var body store.AgentSession
		decodeBody(t, rec, &body)
		return body.Title
	}
	// No request yet: the placeholder is all there is.
	if got := get(captured.ID); got != "Captured codex session" {
		t.Fatalf("before request: %q", got)
	}
	for _, id := range []string{captured.ID, named.ID} {
		if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: id, Idx: 1, Role: "user",
			TextPreview: `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>`}); err != nil {
			t.Fatal(err)
		}
		if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: id, Idx: 2, Role: "user", TextPreview: "make search fast"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := get(captured.ID); got != "make search fast" {
		t.Fatalf("captured title = %q", got)
	}
	if got := get(named.ID); got != "Release plan" {
		t.Fatalf("named title = %q", got)
	}
}
