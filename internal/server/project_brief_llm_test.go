package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// TestProjectBriefRefreshUsesLLM: with OpenRouter configured, the refresh
// route uses the LLM (a fenced markdown answer is accepted), patches the
// source of a brief first written by the heuristic, and reports the
// provider. A garbage answer falls back with a recorded reason and one WARN.
func TestProjectBriefRefreshUsesLLM(t *testing.T) {
	s, mem, pid := briefFixture(t)
	var logBuf bytes.Buffer
	s.Log = log.New(&logBuf, "", 0)
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "api/routes", Content: "Express routes are registered in backend/src/routes/index.js.", Category: "api", Tags: []string{"session:s7"}, FilesAffected: []string{"backend/src/routes/index.js"}})
	addBriefMemory(t, mem, &store.MemoryItem{ProjectID: pid, Key: "infra/vercel", Content: "The Node.js backend deploys to Vercel.", Category: "infrastructure", Tags: []string{"session:s8"}})

	// First brief without a key: heuristic, reason no_api_key.
	owner := loginAs(t, s, "owner")
	rec := doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/brief/refresh", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("heuristic refresh: %d %s", rec.Code, rec.Body.String())
	}
	var got projectBriefResponse
	decodeBody(t, rec, &got)
	if got.Provider != extract.ProviderHeuristic || got.FallbackReason != extract.BriefFallbackNoKey {
		t.Fatalf("heuristic response: %+v", got)
	}
	first := loadProjectBrief(t.Context(), mem, pid)
	if first.Source != "brief:heuristic" {
		t.Fatalf("first source = %s", first.Source)
	}

	// Now OpenRouter is configured.
	reply := "Here is the brief:\n```markdown\n# Project brief: brief-proj\nWhat it is: a pathology lab web app.\nStack: Node, Express, Vercel\n\n## Key decisions and conventions\n- Routes live in backend/src/routes (session:s7)\n```"
	var prompts []string
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			prompts = append(prompts, body.Messages[1].Content)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	defer or.Close()
	cfg := extract.Config{APIKey: "test-key"}
	svc := extract.NewService(cfg)
	svc.Client = &extract.Client{Cfg: cfg, HTTP: or.Client(), BaseURLOverride: or.URL}
	s.Extractor = svc

	rec = doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/brief/refresh", owner, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("llm refresh: %d %s", rec.Code, rec.Body.String())
	}
	got = projectBriefResponse{}
	decodeBody(t, rec, &got)
	if len(prompts) != 1 || !strings.Contains(prompts[0], "Detected stack (from files and mentions): ") || !strings.Contains(prompts[0], "Express") {
		t.Fatalf("prompts: %q", prompts)
	}
	if got.Provider != extract.ProviderOpenRouter || got.FallbackReason != "" ||
		!strings.HasPrefix(got.Content, "# Project brief: brief-proj\nWhat it is: a pathology lab web app.") ||
		strings.Contains(got.Content, "```") || strings.Contains(got.Content, "Here is") {
		t.Fatalf("llm response: %+v", got)
	}
	brief := loadProjectBrief(t.Context(), mem, pid)
	if brief.ID != first.ID || brief.Source != "brief:openrouter" {
		t.Fatalf("llm rebuild must patch the same row's source: id %s→%s source %s", first.ID, brief.ID, brief.Source)
	}

	// Garbage answer: deterministic brief, reason recorded, WARN logged once.
	reply = "Sorry, I can't help with that."
	logBuf.Reset()
	for i := 0; i < 2; i++ {
		rec = doJSON(t, s, http.MethodPost, "/v1/projects/"+pid+"/brief/refresh", owner, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("garbage refresh: %d %s", rec.Code, rec.Body.String())
		}
	}
	got = projectBriefResponse{}
	decodeBody(t, rec, &got)
	if got.Provider != extract.ProviderHeuristic || got.FallbackReason != extract.BriefFallbackUnparseable ||
		!strings.Contains(got.Content, "What it is: ") || strings.Contains(got.Content, "unparseable") {
		t.Fatalf("garbage response: %+v", got)
	}
	if brief := loadProjectBrief(t.Context(), mem, pid); brief.Source != "brief:heuristic" {
		t.Fatalf("fallback source = %s", brief.Source)
	}
	if n := strings.Count(logBuf.String(), "WARN brief: project="+pid); n != 1 {
		t.Fatalf("WARN lines = %d:\n%s", n, logBuf.String())
	}
	if strings.Contains(logBuf.String(), "test-key") {
		t.Fatal("log leaked the API key")
	}
}
