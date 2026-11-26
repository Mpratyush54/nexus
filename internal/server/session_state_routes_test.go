package server

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"central-memory/internal/store"
)

type stateTimeline struct {
	Items      []store.TimelineItem `json:"items"`
	NextCursor string               `json:"next_cursor"`
}

func getStateTimeline(t *testing.T, s *Server, tok string, query url.Values) stateTimeline {
	t.Helper()
	path := "/v1/timeline"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	rec := doJSON(t, s, http.MethodGet, path, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("timeline %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var out stateTimeline
	decodeBody(t, rec, &out)
	return out
}

func stateTimelineIDs(items []store.TimelineItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.SessionID)
	}
	return ids
}

func TestAgentSessionStateRenamePinArchive(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	projectID, sid := sharedSession(t, s, "alice")
	alice, bob, carol := loginAs(t, s, "alice"), loginAs(t, s, "bob"), loginAs(t, s, "carol")
	// A second chat and an archived snapshot chat, both alice's.
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": projectID, "harness": "codex", "native_id": "second-chat", "title": "Second chat",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	var second store.AgentSession
	decodeBody(t, rec, &second)
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: "legacy-chat", ProjectID: projectID, OwnerUserID: "alice",
		Harness: "codex", ConversationID: "old-thread", TurnCount: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: sid, Idx: 1, Role: "user", TextPreview: "hello"}); err != nil {
		t.Fatal(err)
	}
	base := "/v1/agent-sessions/" + sid + "/state"

	// Validation and access.
	if rec := doJSON(t, s, http.MethodPatch, base, alice, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPatch, base, alice, map[string]any{"pinned": true, "archived": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("pin+archive: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPatch, base, carol, map[string]any{"pinned": true}); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPatch, "/v1/agent-sessions/nope/state", alice, map[string]any{"pinned": true}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPatch, "/v1/agent-sessions/legacy-chat/state", carol, map[string]any{"pinned": true}); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger legacy: %d %s", rec.Code, rec.Body.String())
	}

	// Rename and pin the first chat.
	rec = doJSON(t, s, http.MethodPatch, base, alice, map[string]any{"title": "  My\tfavourite\x07 chat  ", "pinned": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename+pin: %d %s", rec.Code, rec.Body.String())
	}
	var st map[string]any
	decodeBody(t, rec, &st)
	if st["session_id"] != sid || st["title"] != "My favourite chat" || st["pinned"] != true || st["archived"] != false || st["pinned_at"] == nil {
		t.Fatalf("state = %v", st)
	}
	// Pin the snapshot chat; archive the second chat.
	if rec := doJSON(t, s, http.MethodPatch, "/v1/agent-sessions/legacy-chat/state", alice, map[string]any{"pinned": true}); rec.Code != http.StatusOK {
		t.Fatalf("pin legacy: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPatch, "/v1/agent-sessions/"+second.ID+"/state", alice, map[string]any{"archived": true}); rec.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body.String())
	}

	// Default timeline hides the archived chat and shows the rename.
	tl := getStateTimeline(t, s, alice, nil)
	found := false
	for _, item := range tl.Items {
		if item.SessionID == second.ID {
			t.Fatalf("archived chat listed: %v", stateTimelineIDs(tl.Items))
		}
		if item.SessionID == sid {
			found = true
			if item.Title != "My favourite chat" || !item.Renamed || !item.Pinned || item.Archived {
				t.Fatalf("renamed item = %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("renamed chat missing: %v", stateTimelineIDs(tl.Items))
	}
	if all := getStateTimeline(t, s, alice, url.Values{"archived": {"include"}}); len(all.Items) != len(tl.Items)+1 {
		t.Fatalf("archived=include: %v vs %v", stateTimelineIDs(all.Items), stateTimelineIDs(tl.Items))
	}
	arch := getStateTimeline(t, s, alice, url.Values{"archived": {"only"}})
	if len(arch.Items) != 1 || arch.Items[0].SessionID != second.ID || !arch.Items[0].Archived || arch.Items[0].Title != "Second chat" || arch.NextCursor != "" {
		t.Fatalf("archived=only: %+v", arch)
	}
	pinned := getStateTimeline(t, s, alice, url.Values{"pinned": {"only"}})
	if got := stateTimelineIDs(pinned.Items); fmt.Sprint(got) != fmt.Sprint([]string{"legacy-chat", sid}) {
		t.Fatalf("pinned=only order: %v", got)
	}
	if pinned.Items[1].TurnCount != 1 || pinned.Items[0].TurnCount != 7 || pinned.Items[0].VersionState != "legacy_snapshot" {
		t.Fatalf("pinned turn counts: %+v", pinned.Items)
	}
	// q matches the custom title in the only modes; project_id filters.
	if got := getStateTimeline(t, s, alice, url.Values{"pinned": {"only"}, "q": {"FAVOURITE"}}); len(got.Items) != 1 || got.Items[0].SessionID != sid {
		t.Fatalf("pinned q: %v", stateTimelineIDs(got.Items))
	}
	other, err := mem.ResolveProject(ctx, "", "", "state-other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, other.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := getStateTimeline(t, s, alice, url.Values{"pinned": {"only"}, "project_id": {other.ID}}); len(got.Items) != 0 {
		t.Fatalf("pinned other project: %v", stateTimelineIDs(got.Items))
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/timeline?archived=bogus", alice, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad archived: %d", rec.Code)
	}

	// GET shows the rename and marks; another reader sees none of it.
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, alice, nil)
	var got map[string]any
	decodeBody(t, rec, &got)
	if got["title"] != "My favourite chat" || got["pinned"] != true || got["renamed"] != true || got["id"] != sid {
		t.Fatalf("get = %v", got)
	}
	if rec := doJSON(t, s, http.MethodPut, "/v1/agent-sessions/"+sid+"/grants/bob", alice, map[string]any{"live": true}); rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+sid, bob, nil)
	got = nil
	decodeBody(t, rec, &got)
	if got["title"] == "My favourite chat" || got["pinned"] != nil || got["renamed"] != nil {
		t.Fatalf("bob sees alice's state: %v", got)
	}
	// Bob can keep his own state on a chat shared with him.
	if rec := doJSON(t, s, http.MethodPatch, base, bob, map[string]any{"archived": true}); rec.Code != http.StatusOK {
		t.Fatalf("grantee archive: %d %s", rec.Code, rec.Body.String())
	}

	// Archiving unpins; pinning un-archives; clearing everything empties.
	rec = doJSON(t, s, http.MethodPatch, base, alice, map[string]any{"archived": true})
	st = nil
	decodeBody(t, rec, &st)
	if st["pinned"] != false || st["archived"] != true || st["title"] != "My favourite chat" {
		t.Fatalf("archive pinned: %v", st)
	}
	rec = doJSON(t, s, http.MethodPatch, base, alice, map[string]any{"pinned": true, "title": ""})
	st = nil
	decodeBody(t, rec, &st)
	if st["pinned"] != true || st["archived"] != false || st["title"] != "" {
		t.Fatalf("pin archived: %v", st)
	}
	if rec := doJSON(t, s, http.MethodPatch, base, alice, map[string]any{"pinned": false}); rec.Code != http.StatusOK {
		t.Fatalf("unpin: %d", rec.Code)
	}
	states, err := mem.ListSessionUserStates(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := states[sid]; ok || len(states) != 2 {
		t.Fatalf("alice states = %+v", states)
	}

	// Deleting a chat drops everyone's state for it.
	if rec := doJSON(t, s, http.MethodDelete, "/v1/agent-sessions/"+sid, alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if states, _ := mem.ListSessionUserStates(ctx, "bob"); len(states) != 0 {
		t.Fatalf("bob states after delete = %+v", states)
	}
}
