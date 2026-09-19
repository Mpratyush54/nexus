package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHeuristicKeepsDecisionDropsSkill(t *testing.T) {
	turns := []Turn{
		{Speaker: "user", Content: "We decided to use Redis for pub/sub between the daemon and the portal."},
		{Speaker: "user", Content: "Use when the user asks to summarize a failing pull request check."},
		{Speaker: "assistant", Content: "Here is a long explanation without any project decision at all in it."},
	}
	got := Heuristic("proj", turns, nil)
	if len(got) != 1 {
		t.Fatalf("got %d proposals: %+v", len(got), got)
	}
	if !strings.Contains(strings.ToLower(got[0].Content), "decided") {
		t.Errorf("unexpected: %+v", got[0])
	}
}

func TestExtractOpenRouterSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"content": `{"memories":[{"key":"cache/redis","content":"We decided to use Redis for pub/sub between daemon and portal.","level":"project","scope":"decision","confidence":0.9,"explicit":true}]}`,
				}},
			},
		})
	}))
	defer srv.Close()

	svc := NewService(Config{APIKey: "test-key", Model: "openrouter/free"})
	svc.Client = &Client{Cfg: svc.Cfg, HTTP: srv.Client(), BaseURLOverride: srv.URL}
	res, err := svc.Extract(context.Background(), "proj", []Turn{
		{Speaker: "user", Content: "We decided to use Redis for caching layers in the API."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderOpenRouter {
		t.Fatalf("provider = %q", res.Provider)
	}
	if len(res.Proposals) != 1 {
		t.Fatalf("proposals = %+v", res.Proposals)
	}
}

func TestExtractOpenRouterEmptyFallsBackToDurableHeuristic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `{"memories":[]}`}},
			},
		})
	}))
	defer srv.Close()

	svc := NewService(Config{APIKey: "test-key"})
	svc.Client = &Client{Cfg: svc.Cfg, HTTP: srv.Client(), BaseURLOverride: srv.URL}
	res, err := svc.Extract(context.Background(), "proj", []Turn{
		{Speaker: "user", Content: "We decided to use Redis for pub/sub between daemon and portal."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderHeuristic {
		t.Fatalf("provider = %q want heuristic for empty LLM + durable turn", res.Provider)
	}
	if len(res.Proposals) != 1 {
		t.Fatalf("want durable heuristic proposal, got %+v", res.Proposals)
	}
}

func TestExtractFallsBackHeuristicOnLLMFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	svc := NewService(Config{APIKey: "test-key"})
	svc.Client = &Client{Cfg: svc.Cfg, HTTP: srv.Client(), BaseURLOverride: srv.URL}
	res, err := svc.Extract(context.Background(), "proj", []Turn{
		{Speaker: "user", Content: "We decided to use Postgres for the primary store going forward."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != ProviderHeuristic {
		t.Fatalf("provider = %q want heuristic", res.Provider)
	}
	if len(res.Proposals) != 1 {
		t.Fatalf("expected heuristic proposal, got %+v", res.Proposals)
	}
}

func TestExtractThrottleSkipsSecondLLMCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `{"memories":[]}`}},
			},
		})
	}))
	defer srv.Close()

	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	svc := NewService(Config{APIKey: "k"})
	svc.Now = func() time.Time { return fixed }
	svc.Client = &Client{Cfg: svc.Cfg, HTTP: srv.Client(), BaseURLOverride: srv.URL}

	turn := []Turn{{Speaker: "user", Content: "We decided to use Redis for pub/sub in production."}}
	_, _ = svc.Extract(context.Background(), "p1", turn, nil)
	_, _ = svc.Extract(context.Background(), "p1", turn, nil)
	if calls != 1 {
		t.Fatalf("LLM calls = %d want 1 (throttled)", calls)
	}
}

func TestParseProposalsRejectsJunk(t *testing.T) {
	raw := []byte(`{"memories":[{"key":"x","content":"Use when the user asks to summarize a failing check please.","level":"project","scope":"fact","confidence":0.9}]}`)
	got := parseProposals(raw)
	if len(got) != 0 {
		t.Fatalf("junk accepted: %+v", got)
	}
}
