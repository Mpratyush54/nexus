package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestTimelineSearchIncludesAuthorizedArchivedConversationText(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, err := mem.ResolveProject(ctx, "", "", "archive-search")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, project.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	id := "6f7d0a54-1111-4c2e-9c48-8a2b1f4e6d10"
	if err := mem.UpsertSessionSnapshot(ctx, &store.SessionSnapshot{
		SessionID: id, ProjectID: project.ID, OwnerUserID: "owner", Harness: "cursor", ConversationID: "old-chat", TurnCount: 2,
		TranscriptPayload: gzipJSONL(t, `{"speaker":"user","content":"Where are the islands?"}`, `{"speaker":"assistant","content":"They are in the SILVER lagoon."}`),
	}); err != nil {
		t.Fatal(err)
	}
	owner := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodGet, "/v1/timeline?project_id="+project.ID+"&q=silver", owner, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SILVER lagoon") || !strings.Contains(rec.Body.String(), `"match_role":"assistant"`) {
		t.Fatalf("archived search: %d %s", rec.Code, rec.Body.String())
	}
	outsider := loginAs(t, s, "outsider")
	rec = doJSON(t, s, http.MethodGet, "/v1/timeline?q=silver", outsider, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "SILVER lagoon") {
		t.Fatalf("archived search leaked private content: %d %s", rec.Code, rec.Body.String())
	}
}
