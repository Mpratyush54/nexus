package server

import (
	"net/http"
	"testing"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// uuidLikeSessionID shape check: 8-4-4-4-12 hex — matches real harness ids.
const harvestUUID = "01234567-89ab-cdef-0123-456789abcdef"

// TestHarvestSummaryPersistsForUUIDSession guards the regression where the
// summary create path set session_id on a level 'project' row and the store
// rejected it ("session_id set requires level 'session'"), silently dropping
// the summary for every UUID session.
func TestHarvestSummaryPersistsForUUIDSession(t *testing.T) {
	s := newTestServer()
	ms := s.Store.(*store.MemStore)
	ctx := t.Context()
	p, err := ms.ResolveProject(ctx, "", "", "uuid-summary-proj")
	if err != nil {
		t.Fatal(err)
	}
	job := &store.HarvestJob{ProjectID: p.ID, Source: "test"}
	summary := "Goal: persist harvested summaries for UUID sessions. Actions: removed invalid session_id attribution from the create path. Outcomes: episode summaries save again."
	item, err := s.upsertSessionSummary(ctx, job, harvestUUID, summary)
	if err != nil {
		t.Fatalf("summary create for UUID session: %v", err)
	}
	if item.SessionID != "" {
		t.Fatalf("summary must not claim session_id: %+v", item)
	}
	if item.Key != "session/"+harvestUUID {
		t.Fatalf("unexpected key: %q", item.Key)
	}
	found := false
	for _, tag := range item.Tags {
		if tag == "session:"+harvestUUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary missing session tag: %v", item.Tags)
	}
}

// TestHarvestDecisionPersistsForUUIDSession is the same guard for the
// decision/proposal create path.
func TestHarvestDecisionPersistsForUUIDSession(t *testing.T) {
	s := newTestServer()
	ms := s.Store.(*store.MemStore)
	ctx := t.Context()
	p, err := ms.ResolveProject(ctx, "", "", "uuid-decision-proj")
	if err != nil {
		t.Fatal(err)
	}
	job := &store.HarvestJob{ProjectID: p.ID, Source: "test"}
	prop := extract.Proposal{
		Key:     "harvest-uuid-decision",
		Content: "Harvested decisions attribute sessions through tags and the session/<id> key because session_id requires level 'session'.",
		Level:   "project",
		Scope:   "decision",
	}
	item, err := s.persistHarvestMemory(ctx, job, prop, harvestUUID)
	if err != nil {
		t.Fatalf("decision create for UUID session: %v", err)
	}
	if item.SessionID != "" {
		t.Fatalf("decision must not claim session_id: %+v", item)
	}
	found := false
	for _, tag := range item.Tags {
		if tag == "session:"+harvestUUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("decision missing session tag: %v", item.Tags)
	}
}

// TestAgentSessionSummaryFallsBackToHarvestMemory: once extraction runs, the
// processed summary lives as memory key session/<native>; the Summary route
// must serve it when the session row has no summary column value.
func TestAgentSessionSummaryFallsBackToHarvestMemory(t *testing.T) {
	s := newTestServer()
	s.registerAgentSessionToolRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "summary-fallback-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	token := loginAs(t, s, "owner")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", token, map[string]any{
		"project_id": proj.ID, "harness": "opencode", "native_id": "ses_summary_fallback",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)

	content := "Processed summary stored by the harvest worker for the summary fallback test session."
	if err := mem.CreateMemoryItem(ctx, &store.MemoryItem{
		ProjectID: proj.ID,
		Key:       "session/ses_summary_fallback",
		Content:   content,
		Level:     "project",
		Scope:     "episode_summary",
		Status:    store.StatusConfirmed,
		Source:    "harvest:test",
		Tags:      []string{"session-compress", "session:ses_summary_fallback"},
	}); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/summary", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary route: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Summary string `json:"summary"`
	}
	decodeBody(t, rec, &body)
	if body.Summary != content {
		t.Fatalf("summary fallback = %q, want stored memory", body.Summary)
	}
}

// TestAgentSessionMemoriesRouteIncludesHarvestTag: the memories route matches
// harvested rows by the session:<id> tag even though session_id stays NULL.
func TestAgentSessionMemoriesRouteIncludesHarvestTag(t *testing.T) {
	s := newTestServer()
	s.registerAgentSessionToolRoutes()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "memories-route-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	token := loginAs(t, s, "owner")

	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", token, map[string]any{
		"project_id": proj.ID, "harness": "opencode", "native_id": "ses_memories_route",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", rec.Code, rec.Body.String())
	}
	var created store.AgentSession
	decodeBody(t, rec, &created)

	if err := mem.CreateMemoryItem(ctx, &store.MemoryItem{
		ProjectID: proj.ID,
		Key:       "harvest-tagged-note",
		Content:   "Tagged harvest note that belongs to the session pane test conversation.",
		Level:     "project",
		Scope:     "fact",
		Status:    store.StatusConfirmed,
		Source:    "harvest:test",
		Tags:      []string{"session:ses_memories_route"},
	}); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+created.ID+"/memories", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("memories route: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	decodeBody(t, rec, &body)
	if len(body.Items) != 1 || body.Items[0]["key"] != "harvest-tagged-note" {
		t.Fatalf("memories route items = %+v", body.Items)
	}
}
