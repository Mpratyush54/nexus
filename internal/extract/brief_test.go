package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func sampleBriefInput() BriefInput {
	return BriefInput{
		Project: "nexus",
		Memories: []BriefMemory{
			{Key: "db", Content: "Postgres with pgvector stores memories. Extra detail here.", Category: "dependencies", Confidence: 0.9, SessionID: "s-db"},
			{Key: "arch", Content: "The server is a Go stdlib HTTP API behind a thin store interface.", Category: "architecture", Confidence: 0.95, SessionID: "s-arch"},
			{Key: "conv", Content: "Use LF line endings and gofmt on every change.", Category: "conventions", Confidence: 0.8},
			{Key: "pin", Content: "Never put personal memories into project-visible output.", Category: "auth", Confidence: 0.5, Pinned: true, SessionID: "s-pin"},
		},
		Summaries: []BriefMemory{{Key: "session/s-new", Content: "Added the project brief to MCP initialize.", SessionID: "s-new"}},
	}
}

func TestDeterministicBriefShapeAndSources(t *testing.T) {
	in := sampleBriefInput()
	a := DeterministicBrief(in)
	b := DeterministicBrief(in)
	if a.Content != b.Content {
		t.Fatal("deterministic brief must be stable")
	}
	if a.Provider != ProviderHeuristic {
		t.Fatalf("provider = %s", a.Provider)
	}
	for _, want := range []string{
		"# Project brief: nexus",
		"What it is: nexus is a Go software project.",
		"Stack: Go, Postgres, pgvector\n",
		"## Recent work\n- Added the project brief to MCP initialize. (session:s-new)",
		"- Dependencies: Postgres with pgvector stores memories. (session:s-db)",
		"- Architecture: The server is a Go stdlib HTTP API behind a thin store interface. (session:s-arch)",
		"- Conventions: Use LF line endings and gofmt on every change.", // no source: none known
		"- Auth: Never put personal memories into project-visible output. (session:s-pin)",
	} {
		if !strings.Contains(a.Content, want) {
			t.Fatalf("brief missing %q:\n%s", want, a.Content)
		}
	}
	if strings.Contains(a.Content, "every change. (session") {
		t.Fatalf("invented a source:\n%s", a.Content)
	}
	// Category order: architecture before auth before conventions.
	if strings.Index(a.Content, "- Architecture") > strings.Index(a.Content, "- Auth") ||
		strings.Index(a.Content, "- Auth") > strings.Index(a.Content, "- Conventions") {
		t.Fatalf("category order wrong:\n%s", a.Content)
	}
	got := strings.Join(a.Sources, ",")
	if got != "s-arch,s-pin,s-db,s-new" {
		t.Fatalf("sources = %s", got)
	}
}

func TestDeterministicBriefCapsLengthAndDecisions(t *testing.T) {
	in := BriefInput{Project: "big"}
	for i := 0; i < 40; i++ {
		in.Memories = append(in.Memories, BriefMemory{
			Key:        fmt.Sprintf("k%d", i),
			Content:    strings.Repeat(fmt.Sprintf("decision %d is long ", i), 20),
			Category:   "architecture",
			Confidence: float64(i) / 100,
			SessionID:  fmt.Sprintf("s%d", i),
		})
	}
	out := DeterministicBrief(in)
	if n := utf8.RuneCountInString(out.Content); n > MaxBriefChars {
		t.Fatalf("brief is %d chars", n)
	}
	if lines := strings.Count(out.Content, "\n- "); lines < 5 || lines > maxBriefDecisions {
		t.Fatalf("decision lines = %d:\n%s", lines, out.Content)
	}
	// Highest confidence first.
	if !strings.Contains(out.Content, "decision 39") {
		t.Fatalf("top memory missing:\n%s", out.Content)
	}
}

func TestClampBriefKeepsWholeLines(t *testing.T) {
	long := strings.Repeat("line of text that is fairly long\n", 100)
	out := ClampBrief(long)
	if utf8.RuneCountInString(out) > MaxBriefChars || strings.HasSuffix(out, "fairly") {
		t.Fatalf("clamp: %d chars", utf8.RuneCountInString(out))
	}
	if !strings.HasSuffix(out, "fairly long") {
		t.Fatalf("clamp cut mid-line: %q", out[len(out)-20:])
	}
}

func TestGenerateBriefWithoutKeyIsDeterministic(t *testing.T) {
	svc := NewService(Config{})
	out := svc.GenerateBrief(context.Background(), sampleBriefInput())
	if out.Provider != ProviderHeuristic || out.Content != DeterministicBrief(sampleBriefInput()).Content {
		t.Fatalf("no-key brief: %+v", out)
	}
	if empty := svc.GenerateBrief(context.Background(), BriefInput{Project: "x"}); empty.Content != "" {
		t.Fatalf("empty input must give empty brief: %q", empty.Content)
	}
}

func TestGenerateBriefLLMAndFallback(t *testing.T) {
	var prompt string
	reply := `{"brief":"# nexus\nGo + Postgres.\n- Keep LF endings (session:s-arch)"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			prompt = body.Messages[1].Content
		}
		if reply == "" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	defer srv.Close()
	cfg := Config{APIKey: "k"}
	svc := &Service{Cfg: cfg, Client: &Client{Cfg: cfg, BaseURLOverride: srv.URL}}

	out := svc.GenerateBrief(context.Background(), sampleBriefInput())
	if out.Provider != ProviderOpenRouter || !strings.Contains(out.Content, "Keep LF endings") {
		t.Fatalf("llm brief: %+v", out)
	}
	if strings.Join(out.Sources, ",") != "s-arch" {
		t.Fatalf("llm sources: %v", out.Sources)
	}
	if !strings.Contains(prompt, "session:s-db") || !strings.Contains(prompt, "Recent session summaries") {
		t.Fatalf("prompt lacks memories/summaries:\n%s", prompt)
	}

	reply = ""
	out = svc.GenerateBrief(context.Background(), sampleBriefInput())
	if out.Provider != ProviderHeuristic || out.LLMError == "" || out.Content == "" {
		t.Fatalf("fallback brief: %+v", out)
	}
}
