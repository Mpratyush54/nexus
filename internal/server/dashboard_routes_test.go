package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestProjectDashboardShape(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "dash-proj")

	_ = s.Store.AppendEvent(t.Context(), &store.Event{
		ProjectID: proj,
		UserID:    "alice",
		EventType: "MEMORY_PROPOSED",
		Payload:   map[string]any{"key": "a/b"},
		CreatedAt: time.Now().UTC(),
	})
	_ = s.Store.CreateMemoryItem(t.Context(), &store.MemoryItem{
		ProjectID:  proj,
		Key:        "a/b",
		Content:    "hello world content that is long enough for validation",
		Level:      "project",
		Scope:      "fact",
		Status:     "PROPOSED",
		Confidence: 0.8,
	})

	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/dashboard", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	metrics, _ := body["metrics"].(map[string]any)
	if metrics == nil {
		t.Fatalf("missing metrics: %+v", body)
	}
	heat, _ := body["heatmap"].([]any)
	if len(heat) < 300 {
		t.Fatalf("heatmap days = %d, want ~365", len(heat))
	}
}

func TestGitHubOAuthStatusWhenUnconfigured(t *testing.T) {
	t.Setenv("GITHUB_CLIENT_ID", "")
	t.Setenv("GITHUB_CLIENT_SECRET", "")
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "oauth-status")
	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/github/oauth/status", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	if body["oauth_configured"] != false {
		t.Fatalf("expected oauth_configured false: %+v", body)
	}
}

// slowDashboardStore wraps MemStore: its event counts fail and its memory
// counts hang until the request budget ends, and it counts calls.
type slowDashboardStore struct {
	*store.MemStore
	eventCalls, memoryCalls atomic.Int32
	hangMemories            bool
}

func (s *slowDashboardStore) DashboardEvents(ctx context.Context, projectID string, since, now time.Time) (store.DashboardEventStats, error) {
	s.eventCalls.Add(1)
	return store.DashboardEventStats{}, errors.New("read events: unexpected EOF")
}

func (s *slowDashboardStore) DashboardMemories(ctx context.Context, projectID string) (store.DashboardMemoryCounts, error) {
	s.memoryCalls.Add(1)
	if s.hangMemories {
		<-ctx.Done()
		return store.DashboardMemoryCounts{}, ctx.Err()
	}
	return s.MemStore.DashboardMemories(ctx, projectID)
}

// One slow or failing section must not fail the dashboard: the rest answers
// 200 within the budget and the missing sections are named.
func TestProjectDashboardReturnsPartialDataWhenASectionFails(t *testing.T) {
	old := dashboardBudget
	dashboardBudget = 300 * time.Millisecond
	t.Cleanup(func() { dashboardBudget = old })
	st := &slowDashboardStore{MemStore: store.NewMemStore(), hangMemories: true}
	s := NewServer(st)
	s.Log.SetOutput(&bytes.Buffer{})
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "dash-partial")

	start := time.Now()
	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/dashboard", alice, nil)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("dashboard took %s; a hung section must end at the budget", took)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Partial     bool           `json:"partial"`
		Unavailable []string       `json:"unavailable"`
		Metrics     map[string]any `json:"metrics"`
		Activity    map[string]any `json:"agent_activity"`
	}
	decodeBody(t, rec, &body)
	sort.Strings(body.Unavailable)
	if !body.Partial || strings.Join(body.Unavailable, ",") != "events,memories" {
		t.Fatalf("partial=%v unavailable=%v, want events and memories", body.Partial, body.Unavailable)
	}
	if body.Activity == nil {
		t.Fatalf("agent activity missing although it loaded: %s", rec.Body.String())
	}
	if _, shown := body.Metrics["memories"]; shown {
		t.Fatalf("an unreadable count must be left out, not shown as 0: %+v", body.Metrics)
	}
	if body.Metrics["online"] == nil || body.Metrics["members"] == nil {
		t.Fatalf("live tiles missing: %+v", body.Metrics)
	}
}

// Answers are cached per project and caller; a memory write drops them.
func TestProjectDashboardIsCachedAndMemoryWritesRefreshIt(t *testing.T) {
	st := &slowDashboardStore{MemStore: store.NewMemStore()}
	s := NewServer(st)
	s.Log.SetOutput(&bytes.Buffer{})
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "dash-cache")
	get := func() map[string]any {
		t.Helper()
		rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/dashboard", alice, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("dashboard = %d %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		decodeBody(t, rec, &body)
		return body["metrics"].(map[string]any)
	}
	get()
	get()
	if n := st.memoryCalls.Load(); n != 1 {
		t.Fatalf("memory counts read %d times for two requests, want 1 (cached)", n)
	}
	rec := doJSON(t, s, http.MethodPost, "/memory", alice, map[string]any{
		"project_id": proj, "key": "cache/refresh", "content": "a memory long enough to pass validation",
	})
	if rec.Code >= 300 {
		t.Fatalf("create memory = %d %s", rec.Code, rec.Body.String())
	}
	if got := get()["memories"]; got != float64(1) {
		t.Fatalf("memories after a write = %v, want 1 (cache dropped)", got)
	}
	if n := st.memoryCalls.Load(); n != 2 {
		t.Fatalf("memory counts read %d times, want 2", n)
	}
}

// The tile counted at most 500 memories (it loaded rows to count them).
func TestProjectDashboardCountsEveryMemory(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "dash-many")
	for i := 0; i < 620; i++ {
		status := "CONFIRMED"
		if i%4 == 0 {
			status = "PROPOSED"
		}
		if err := s.Store.CreateMemoryItem(t.Context(), &store.MemoryItem{ProjectID: proj, Key: fmt.Sprintf("many/%d", i),
			Content: "a memory long enough to pass validation", Level: "project", Scope: "fact", Status: status, Confidence: 0.9}); err != nil {
			t.Fatal(err)
		}
	}
	rec := doJSON(t, s, http.MethodGet, "/projects/"+proj+"/dashboard", alice, nil)
	var body struct {
		Metrics struct{ Memories, Proposed, Confirmed int } `json:"metrics"`
	}
	decodeBody(t, rec, &body)
	if body.Metrics.Memories != 620 || body.Metrics.Proposed != 155 || body.Metrics.Confirmed != 465 {
		t.Fatalf("metrics = %+v, want 620 / 155 proposed / 465 confirmed", body.Metrics)
	}
}
