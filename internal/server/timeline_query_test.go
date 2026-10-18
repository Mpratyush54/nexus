package server

import (
	"net/http"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestTimelineQueryAndProjectAreApplied(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	first, err := mem.ResolveProject(t.Context(), "", "", "first-project")
	if err != nil {
		t.Fatal(err)
	}
	second, err := mem.ResolveProject(t.Context(), "", "", "second-project")
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []*store.Project{first, second} {
		if _, err := mem.ClaimProject(t.Context(), project.ID, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	tok := loginAs(t, s, "owner")
	for _, spec := range []struct{ project, native, title string }{
		{first.ID, "native-auth", "Fix authentication"},
		{first.ID, "native-ui", "Polish interface"},
		{second.ID, "native-other", "Fix authentication elsewhere"},
	} {
		rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", tok, map[string]any{
			"project_id": spec.project, "harness": "codex", "native_id": spec.native, "title": spec.title,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := doJSON(t, s, http.MethodGet, "/v1/timeline?project_id="+first.ID+"&q=authentication", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Fix authentication") || strings.Contains(body, "Polish interface") || strings.Contains(body, "elsewhere") {
		t.Fatalf("project/query filter failed: %s", body)
	}
}
