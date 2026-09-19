package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

func TestMemoryExtractHeuristicCreatesProposed(t *testing.T) {
	s := newTestServer()
	// Force heuristic (no OpenRouter key).
	s.Extractor = extract.NewService(extract.Config{})

	tok := loginAs(t, s, "alice")
	pid := resolveTestProject(t, s, tok, "extract-proj")

	rec := doJSON(t, s, http.MethodPost, "/memory/extract", tok, map[string]any{
		"project_id": pid,
		"turns": []map[string]string{
			{
				"speaker": "user",
				"content": "We decided to use Redis for pub/sub between the daemon and the portal.",
			},
			{
				"speaker": "user",
				"content": "Use when the user asks to summarize a failing pull request check.",
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("extract status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count    int    `json:"count"`
		Provider string `json:"provider"`
		Items    []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Content string `json:"content"`
			Source  string `json:"source"`
		} `json:"items"`
	}
	decodeBody(t, rec, &out)
	if out.Provider != extract.ProviderHeuristic {
		t.Fatalf("provider = %q", out.Provider)
	}
	if out.Count != 1 || len(out.Items) != 1 {
		t.Fatalf("expected 1 durable item, got %+v", out)
	}
	if out.Items[0].Status != "PROPOSED" {
		t.Fatalf("status = %q", out.Items[0].Status)
	}
	if !strings.Contains(strings.ToLower(out.Items[0].Content), "redis") {
		t.Fatalf("content = %q", out.Items[0].Content)
	}
}

func TestMemoryExtractOpenRouterPath(t *testing.T) {
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{
					"content": `{"memories":[{"key":"cache/redis","content":"We decided to use Redis for pub/sub between daemon and portal systems.","level":"project","scope":"decision","confidence":0.92,"explicit":true}]}`,
				}},
			},
		})
	}))
	defer or.Close()

	s := newTestServer()
	cfg := extract.Config{APIKey: "test-key", Model: "openrouter/free"}
	svc := extract.NewService(cfg)
	svc.Client = &extract.Client{Cfg: cfg, HTTP: or.Client(), BaseURLOverride: or.URL}
	s.Extractor = svc

	tok := loginAs(t, s, "alice")
	pid := resolveTestProject(t, s, tok, "extract-or")

	rec := doJSON(t, s, http.MethodPost, "/memory/extract", tok, map[string]any{
		"project_id": pid,
		"turns": []map[string]string{
			{"speaker": "user", "content": "Please remember we decided on Redis for pub/sub."},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Provider string `json:"provider"`
		Count    int    `json:"count"`
	}
	decodeBody(t, rec, &out)
	if out.Provider != extract.ProviderOpenRouter || out.Count != 1 {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestMemoryExtractEmptyTurns(t *testing.T) {
	s := newTestServer()
	tok := loginAs(t, s, "alice")
	pid := resolveTestProject(t, s, tok, "extract-empty")
	rec := doJSON(t, s, http.MethodPost, "/memory/extract", tok, map[string]any{
		"project_id": pid,
		"turns":      []any{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMemoryExtractRequiresAuth(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s, http.MethodPost, "/memory/extract", "", map[string]any{
		"project_id": "x",
		"turns":      []any{},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401", rec.Code)
	}
}

func TestMemoryExtractThrottleSkipsHeuristic(t *testing.T) {
	calls := 0
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `{"memories":[]}`}},
			},
		})
	}))
	defer or.Close()

	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	s := newTestServer()
	cfg := extract.Config{APIKey: "k"}
	svc := extract.NewService(cfg)
	svc.Now = func() time.Time { return fixed }
	svc.Client = &extract.Client{Cfg: cfg, HTTP: or.Client(), BaseURLOverride: or.URL}
	s.Extractor = svc

	tok := loginAs(t, s, "alice")
	pid := resolveTestProject(t, s, tok, "extract-throttle")
	body := map[string]any{
		"project_id": pid,
		"turns": []map[string]string{
			{"speaker": "user", "content": "We decided to use Postgres for the primary application store."},
		},
	}
	_ = doJSON(t, s, http.MethodPost, "/memory/extract", tok, body)
	rec := doJSON(t, s, http.MethodPost, "/memory/extract", tok, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("second extract status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("openrouter calls = %d want 1", calls)
	}
	var out struct {
		Provider string `json:"provider"`
		Count    int    `json:"count"`
	}
	decodeBody(t, rec, &out)
	if out.Count != 0 {
		t.Fatalf("throttled call must not emit heuristic scrap, got count=%d provider=%q", out.Count, out.Provider)
	}
}

func TestMemoryHarvestEnqueueShowsRaw(t *testing.T) {
	s := newTestServer()
	s.Harvest = store.NewMemHarvestQueue()
	tok := loginAs(t, s, "alice")
	pid := resolveTestProject(t, s, tok, "harvest-raw")
	rec := doJSON(t, s, http.MethodPost, "/memory/harvest", tok, map[string]any{
		"project_id": pid,
		"source":     "test",
		"turns": []map[string]string{
			{"speaker": "user", "content": "We decided the harvest queue should show raw turns in the portal immediately."},
		},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Created bool `json:"created"`
		Job     struct {
			Status     string `json:"status"`
			RawPreview string `json:"raw_preview"`
		} `json:"job"`
	}
	decodeBody(t, rec, &out)
	if !out.Created || out.Job.Status != "queued" {
		t.Fatalf("unexpected: %+v", out)
	}
	if !strings.Contains(out.Job.RawPreview, "harvest queue") {
		t.Fatalf("raw_preview = %q", out.Job.RawPreview)
	}
	list := doJSON(t, s, http.MethodGet, "/memory/harvest?project_id="+pid, tok, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
}
