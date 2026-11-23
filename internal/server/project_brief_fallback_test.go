package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// TestRateLimitedHarvestStillBuildsBrief: a project whose harvest jobs all
// hit OpenRouter 429 used to never get a brief (the hook ran on success
// only). Now the failed job builds the deterministic brief without another
// LLM call, and a later working job upgrades it.
func TestRateLimitedHarvestStillBuildsBrief(t *testing.T) {
	s, mem, pid := briefFixture(t)
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/server", Content: "The server is a Go stdlib HTTP API over a store interface.", Category: "architecture", Tags: []string{"session:s1"}})
	q := store.NewMemHarvestQueue()
	s.Harvest = q
	var limited atomic.Bool
	limited.Store(true)
	var calls, briefPrompts atomic.Int32
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if limited.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
			return
		}
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		content := `{"session_summary":"","decisions":[]}`
		if len(body.Messages) > 0 && strings.Contains(body.Messages[0].Content, "onboarding brief") {
			briefPrompts.Add(1)
			content = `{"brief":"# brief-proj\nGo server written by the LLM brief path for this project."}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	defer or.Close()
	cfg := extract.Config{APIKey: "test-key"}
	svc := extract.NewService(cfg)
	svc.Client = &extract.Client{Cfg: cfg, HTTP: or.Client(), BaseURLOverride: or.URL}
	s.Extractor = svc
	now := time.Now().UTC()
	s.briefNow = func() time.Time { return now }

	ctx := t.Context()
	n := 0
	runJob := func() {
		t.Helper()
		n++
		if _, _, err := q.EnqueueHarvestJob(ctx, pid, "test", []store.HarvestTurn{
			{Speaker: "user", Content: "Please fix the deploy script, attempt " + strings.Repeat("again ", n), SessionID: harvestUUID},
			{Speaker: "assistant", Content: "Fixed the deploy script.", SessionID: harvestUUID},
		}); err != nil {
			t.Fatal(err)
		}
		job, err := q.ClaimNextHarvestJob(ctx)
		if err != nil || job == nil {
			t.Fatalf("claim: %v %v", job, err)
		}
		s.processHarvestJob(ctx, job)
	}

	runJob()
	brief := loadProjectBrief(ctx, mem, pid)
	if brief == nil {
		t.Fatal("a rate-limited job must still build the brief")
	}
	if brief.Source != "brief:"+extract.ProviderHeuristic || briefTagValue(brief, briefFallbackTag) != extract.BriefFallbackRateLimited {
		t.Fatalf("brief source=%s tags=%v", brief.Source, brief.Tags)
	}
	if !strings.Contains(brief.Content, "Go stdlib HTTP API") {
		t.Fatalf("brief content: %s", brief.Content)
	}
	if briefPrompts.Load() != 0 {
		t.Fatal("no brief prompt while rate-limited")
	}

	// The LLM recovers; after briefUpgradeAge the next job upgrades it.
	limited.Store(false)
	now = now.Add(briefUpgradeAge + briefDebounce)
	runJob()
	up := loadProjectBrief(ctx, mem, pid)
	if up == nil || up.ID != brief.ID || up.Source != "brief:"+extract.ProviderOpenRouter || briefPrompts.Load() != 1 {
		t.Fatalf("upgrade: prompts=%d %+v", briefPrompts.Load(), up)
	}
	if briefTagValue(up, briefFallbackTag) != "" {
		t.Fatalf("upgraded brief keeps fallback tag: %v", up.Tags)
	}
}

func TestHarvestBriefFallbackReason(t *testing.T) {
	cases := map[string]string{
		`extract: openrouter 429: {"error":{"message":"Rate limit exceeded"}}`: extract.BriefFallbackRateLimited,
		"context deadline exceeded":                              extract.BriefFallbackTimeout,
		"extract: decode response: unexpected end of JSON input": extract.BriefFallbackUpstream,
	}
	for in, want := range cases {
		if got := harvestBriefFallback(in); got != want {
			t.Errorf("%q = %s want %s", in, got, want)
		}
	}
}

// TestProjectBriefGetSchedulesMissingBrief: a project that never had a
// harvest job gets its brief after the first GET.
func TestProjectBriefGetSchedulesMissingBrief(t *testing.T) {
	s, mem, pid := briefFixture(t)
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "arch/server", Content: "The server is a Go stdlib HTTP API over a store interface.", Category: "architecture"})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "note/private", Content: "SECRET-PRIVATE project note visible to its author only.", Visibility: store.VisibilityPrivate, ProposedBy: "editor", Confidence: 1})
	editor := loginAs(t, s, "editor")

	rec := doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/brief", editor, nil)
	var got projectBriefResponse
	decodeBody(t, rec, &got)
	if rec.Code != http.StatusOK || got.Content != "" || got.UpdatedAt != nil || !got.Building {
		t.Fatalf("first get: %d %+v", rec.Code, got)
	}
	s.briefWG.Wait()

	rec = doJSON(t, s, http.MethodGet, "/v1/projects/"+pid+"/brief", editor, nil)
	got = projectBriefResponse{}
	decodeBody(t, rec, &got)
	if got.UpdatedAt == nil || !strings.Contains(got.Content, "Go stdlib HTTP API") || strings.Contains(got.Content, "SECRET") || got.Building {
		t.Fatalf("second get: %+v", got)
	}

	// An empty project is checked once per debounce window, not per GET.
	empty, err := mem.ResolveProject(t.Context(), "", "", "brief-empty-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !s.scheduleProjectBrief(empty.ID) {
		t.Fatal("first schedule must start")
	}
	s.briefWG.Wait()
	if s.scheduleProjectBrief(empty.ID) {
		t.Fatal("second schedule inside the debounce window must not start")
	}
}
