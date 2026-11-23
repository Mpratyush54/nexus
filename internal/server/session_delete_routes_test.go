package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
	"central-memory/internal/usage"
)

func TestAgentSessionDeleteOwnerOnly(t *testing.T) {
	s := newTestServer()
	projectID, sid := sharedSession(t, s, "alice")
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	alice, bob, carol := loginAs(t, s, "alice"), loginAs(t, s, "bob"), loginAs(t, s, "carol")
	base := "/v1/agent-sessions/" + sid
	if rec := doJSON(t, s, http.MethodPut, base+"/grants/bob", alice, map[string]any{"live": true}); rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	if err := mem.AppendSessionTurn(ctx, store.SessionTurn{SessionID: sid, Idx: 1, Role: "user", TextPreview: "hello"}); err != nil {
		t.Fatal(err)
	}
	idx := int64(1)
	if err := mem.RecordSessionUsage(ctx, []store.SessionUsage{{SessionID: sid, Key: "m1", TurnIdx: &idx, Entry: usage.Entry{Model: "m", Input: 3}}}); err != nil {
		t.Fatal(err)
	}

	if rec := doJSON(t, s, http.MethodDelete, base, bob, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("grantee delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodDelete, base, carol, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodDelete, "/v1/agent-sessions/does-not-exist", alice, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing delete: %d %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, s, http.MethodDelete, base, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner delete: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deleted bool                      `json:"deleted"`
		Removed store.SessionDeleteResult `json:"removed"`
	}
	decodeBody(t, rec, &out)
	if !out.Deleted || out.Removed.Turns != 1 || out.Removed.Usage != 1 || out.Removed.Grants != 1 {
		t.Fatalf("delete result = %+v", out)
	}
	for _, tok := range []string{alice, bob} {
		if rec := doJSON(t, s, http.MethodGet, base, tok, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("get after delete: %d", rec.Code)
		}
	}
	if rec := doJSON(t, s, http.MethodDelete, base, alice, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
	events, err := mem.ListAuditEvents(ctx, projectID, "session.deleted", 10)
	if err != nil || len(events) != 1 || events[0].ResourceID != sid {
		t.Fatalf("audit = %+v %v", events, err)
	}
	// The same native id can be captured again as a fresh chat.
	again := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", alice, map[string]any{
		"project_id": projectID, "harness": "codex", "native_id": "grants-chat",
	})
	if again.Code != http.StatusCreated {
		t.Fatalf("recreate: %d %s", again.Code, again.Body.String())
	}
}

func TestAgentSessionTurnDedupeOwnerOnly(t *testing.T) {
	s := newTestServer()
	_, sid := sharedSession(t, s, "alice")
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	alice, bob, carol := loginAs(t, s, "alice"), loginAs(t, s, "bob"), loginAs(t, s, "carol")
	base := "/v1/agent-sessions/" + sid
	if rec := doJSON(t, s, http.MethodPut, base+"/grants/bob", alice, map[string]any{"live": true}); rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at2 := at.Add(time.Minute)
	calls := json.RawMessage(`[{"name":"Read","input":{"path":"a.go"}}]`)
	turns := []store.SessionTurn{
		{Idx: 1, Role: "user", TextPreview: "fix it", SourceAt: &at},
		{Idx: 2, Role: "assistant", TextPreview: "done", ToolCalls: calls, SourceAt: &at2},
		{Idx: 3, Role: "user", TextPreview: "fix it"},                                                                               // copy of 1
		{Idx: 4, Role: "assistant", TextPreview: "done", ToolCalls: json.RawMessage(`[ {"input":{"path":"a.go"},"name":"Read"} ]`)}, // copy of 2
		{Idx: 5, Role: "assistant", TextPreview: "done"},                                                                            // different tool calls
		{Idx: 6, Role: "system", TextPreview: "fix it"},                                                                             // different role
	}
	for _, turn := range turns {
		turn.SessionID = sid
		if err := mem.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	for _, tok := range []string{bob, carol} {
		if rec := doJSON(t, s, http.MethodPost, base+"/turns/dedupe", tok, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("non-owner dedupe: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions/nope/turns/dedupe", alice, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing dedupe: %d", rec.Code)
	}
	var res store.TurnDedupeResult
	rec := doJSON(t, s, http.MethodPost, base+"/turns/dedupe?dry_run=1", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &res)
	if res.Removed != 2 || res.Kept != 4 || !res.DryRun || len(res.RemovedIdx) != 2 || res.RemovedIdx[0] != 3 || res.RemovedIdx[1] != 4 {
		t.Fatalf("dry run = %+v", res)
	}
	if got, _ := mem.ListSessionTurns(ctx, sid); len(got) != 6 {
		t.Fatalf("dry run removed turns: %d", len(got))
	}
	rec = doJSON(t, s, http.MethodPost, base+"/turns/dedupe", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dedupe: %d %s", rec.Code, rec.Body.String())
	}
	res = store.TurnDedupeResult{}
	decodeBody(t, rec, &res)
	if res.Removed != 2 || res.Kept != 4 || res.DryRun {
		t.Fatalf("dedupe = %+v", res)
	}
	got, _ := mem.ListSessionTurns(ctx, sid)
	if len(got) != 4 {
		t.Fatalf("turns after dedupe: %+v", got)
	}
	for _, turn := range got {
		if turn.Idx == 3 || turn.Idx == 4 {
			t.Fatalf("copy %d survived", turn.Idx)
		}
	}
	// Idempotent.
	res = store.TurnDedupeResult{}
	decodeBody(t, doJSON(t, s, http.MethodPost, base+"/turns/dedupe", alice, nil), &res)
	if res.Removed != 0 || res.Kept != 4 {
		t.Fatalf("second dedupe = %+v", res)
	}
}

func TestMemStoreDedupeDatesFullyDatedChat(t *testing.T) {
	mem := store.NewMemStore()
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "dedupe-dates")
	if err != nil {
		t.Fatal(err)
	}
	row, err := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: project.ID, OwnerUserID: "alice", Harness: "claude", NativeID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for _, turn := range []store.SessionTurn{
		{Idx: 1, Role: "user", TextPreview: "hi", SourceAt: &at},
		{Idx: 2, Role: "user", TextPreview: "hi", At: time.Now().UTC()},
	} {
		turn.SessionID = row.ID
		if err := mem.AppendSessionTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	res, err := mem.DedupeSessionTurns(ctx, row.ID, false)
	if err != nil || res.Removed != 1 || res.Kept != 1 {
		t.Fatalf("dedupe = %+v %v", res, err)
	}
	after, err := mem.GetAgentSession(ctx, row.ID)
	if err != nil || !after.LastActiveAt.Equal(at) {
		t.Fatalf("last_active_at = %v %v", after.LastActiveAt, err)
	}
	if _, err := mem.DedupeSessionTurns(ctx, "missing", true); err == nil {
		t.Fatal("missing session deduped")
	}
}
