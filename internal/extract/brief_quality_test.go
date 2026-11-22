package extract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// pathlabBriefInput is the production Pathlab brief input that fell back to
// the old heuristic brief, expressed as memories.
func pathlabBriefInput() BriefInput {
	mem := func(key, content, cat, sid string, conf float64, files ...string) BriefMemory {
		return BriefMemory{Key: key, Content: content, Category: cat, SessionID: sid, Confidence: conf, Confirmed: true, FilesAffected: files}
	}
	return BriefInput{
		Project: "Pathlab",
		Memories: []BriefMemory{
			mem("infra/cors-vercel", "Fixed CORS configuration on Vercel (backend/src/config/cors.js) to normalize origin matching for the Express API.", "infrastructure", "3c87", 0.9, "backend/src/config/cors.js"),
			mem("api/report-controller-outsource-lab", "Update reportController.js to include OUTSOURCE LAB alongside LAB in the pending-cases filter.", "dependencies", "ses_f548", 0.88, "backend/src/controllers/reportController.js"),
			mem("api/report-controller-outsource-lab-2", "Update reportController.js to include OUTSOURCE LAB alongside LAB in the pending-cases filter.", "dependencies", "ses_f548", 0.87, "backend/src/controllers/reportController.js"),
			mem("arch/match-score", "Match scores prioritize interpretation coverage (≥75%) before results coverage (≥65%).", "architecture", "76f8", 0.95, "frontend/src/components/MatchScore.jsx"),
			mem("arch/report-outsource-visibility", "Restored outsource case visibility in Today's Reports by modifying reportController.js.", "architecture", "3c87", 0.93, "backend/src/controllers/reportController.js"),
			mem("arch/report-usg-xray-ui", "Revamp USG and X-ray case lists and reports with new-look UI.", "architecture", "f6fa", 0.92, "frontend/src/pages/UsgReports.jsx"),
			mem("api/compliance-lifecycle", "Integrated ComplianceService trait into lifecycle.rs for cross-component lifecycle management.", "api", "3c87", 0.91),
			mem("conventions/express-routes", "Express routes live in backend/src/routes and every handler is async with a central error middleware.", "conventions", "a1b2", 0.86, "backend/src/routes/index.js"),
			mem("infra/node-version", "The backend runs on Node.js 20 and deploys to Vercel serverless functions.", "infrastructure", "a1b3", 0.85, "backend/package.json"),
			mem("api/report-pagination", "Report list endpoints paginate with page and limit query parameters, default limit 20.", "api", "a1b4", 0.84),
			mem("auth/employee-roles", "Employees have roles admin, pathologist and technician; only admins create employees.", "auth", "a1b5", 0.83),
			mem("api/employee-create", "POST /api/employees validates email uniqueness before insert and returns 409 on duplicates.", "api", "a1b6", 0.82),
			mem("conventions/case-ids", "Case IDs are prefixed by modality (USG-, XR-, LAB-) and are never reused.", "conventions", "a1b7", 0.81),
			mem("arch/report-pdf", "Report PDFs are generated server-side so the printed layout matches the lab letterhead.", "architecture", "a1b8", 0.8),
			mem("conventions/testsprite", "TestSprite dashboards must be green before a release is tagged.", "conventions", "a1b9", 0.79),
			mem("session/d759", "The session focused on resolving TC013 employee-create failures.", "general", "d759", 0.99),
		},
		Summaries: []BriefMemory{
			{Key: "session/d759", Scope: "episode_summary", Content: "The session focused on resolving TC013 employee-create failures and ensuring all TestSprite dashboards turn green. Many details followed.", SessionID: "d759"},
			{Key: "session/c001", Scope: "episode_summary", Content: "Outsource lab cases now appear in Today's Reports again.", SessionID: "c001"},
			{Key: "session/c000", Scope: "episode_summary", Content: "Older session about CORS.", SessionID: "c000"},
		},
	}
}

func TestDeterministicBriefPathlabFixture(t *testing.T) {
	out := DeterministicBrief(pathlabBriefInput())
	c := out.Content
	if n := utf8.RuneCountInString(c); n > MaxBriefChars {
		t.Fatalf("brief is %d chars:\n%s", n, c)
	}
	var what, stack, section string
	seen := map[string]bool{}
	decisions, recent := 0, 0
	for _, l := range strings.Split(c, "\n") {
		switch {
		case strings.HasPrefix(l, "What it is: "):
			what = l
		case strings.HasPrefix(l, "Stack: "):
			stack = strings.TrimPrefix(l, "Stack: ")
		case strings.HasPrefix(l, "## "):
			section = l
		case strings.HasPrefix(l, "- ") && strings.Contains(section, "decisions"):
			decisions++
		case strings.HasPrefix(l, "- ") && strings.Contains(section, "Recent"):
			recent++
		}
		if strings.TrimSpace(l) != "" && seen[l] {
			t.Fatalf("duplicate line %q:\n%s", l, c)
		}
		seen[l] = true
	}
	if !strings.HasPrefix(what, "What it is: Pathlab is a ") || !strings.Contains(what, "web app") {
		t.Fatalf("what-it-is line = %q\n%s", what, c)
	}
	for _, tech := range strings.Split(stack, ", ") {
		if !IsBriefTech(tech) {
			t.Fatalf("stack item %q is not a technology: %q", tech, stack)
		}
	}
	for _, want := range []string{"Node", "Express", "Vercel", "React"} {
		if !strings.Contains(", "+stack+", ", ", "+want+", ") {
			t.Fatalf("stack %q lacks %s", stack, want)
		}
	}
	if decisions < 5 || decisions > maxBriefDecisions {
		t.Fatalf("decisions = %d:\n%s", decisions, c)
	}
	if recent != 2 {
		t.Fatalf("recent lines = %d:\n%s", recent, c)
	}
	if strings.Count(c, "OUTSOURCE LAB alongside LAB") > 1 {
		t.Fatalf("duplicate decision text:\n%s", c)
	}
	if strings.Contains(c, ": The session focused") || strings.Contains(c, "Many details followed") {
		t.Fatalf("session summary leaked into decisions or recent work is too long:\n%s", c)
	}
	t.Logf("Pathlab brief (%d chars):\n%s", utf8.RuneCountInString(c), c)
}

func TestDeterministicBriefUsesOverviewMemory(t *testing.T) {
	in := pathlabBriefInput()
	in.Memories = append(in.Memories, BriefMemory{Key: "project/overview", Content: "Pathlab is a pathology lab web app for case intake, reporting and outsourcing. It has more.", Category: "architecture", Confidence: 0.5, Confirmed: true})
	out := DeterministicBrief(in)
	if !strings.Contains(out.Content, "What it is: Pathlab is a pathology lab web app for case intake, reporting and outsourcing.\n") {
		t.Fatalf("overview not used:\n%s", out.Content)
	}
	if strings.Count(out.Content, "pathology lab web app") != 1 {
		t.Fatalf("overview repeated as a decision:\n%s", out.Content)
	}
}

func TestDetectStackOnlyTechnologies(t *testing.T) {
	got := DetectStack([]BriefMemory{
		{Content: "We react quickly; go to the docs. express delivery is a feature name."},
		{Content: "The API uses Express on Node.js with Prisma and Postgres.", FilesAffected: []string{"api/src/server.ts"}},
		{Content: "Deployed with Docker on AWS ECS; Tailwind for styles.", FilesAffected: []string{"web/app/page.tsx"}},
	})
	want := "TypeScript, Node, Express, React, Tailwind, Postgres, Prisma, Docker, AWS" // all seen once: dictionary order
	if strings.Join(got, ", ") != want {
		t.Fatalf("stack = %q, want %q", strings.Join(got, ", "), want)
	}
}

func TestParseBriefReplyVariants(t *testing.T) {
	md := "# Project brief: Pathlab\nWhat it is: a pathology lab web app.\nStack: Node, Express\n- Decision one (session:s1)"
	escaped := strings.ReplaceAll(md, "\n", `\n`)
	cases := map[string]string{
		"json":             `{"brief":"` + escaped + `"}`,
		"fenced json":      "```json\n{\"brief\":\"" + escaped + "\"}\n```",
		"raw newline json": "{\"brief\":\"" + md + "\"}",
		"plain markdown":   md,
		"fenced markdown":  "Here is the brief:\n```markdown\n" + md + "\n```",
		"think then md":    "<think>let me plan</think>\n" + md,
	}
	for name, reply := range cases {
		got, err := ParseBriefReply(reply)
		if err != nil || got != md {
			t.Fatalf("%s: got %q err %v", name, got, err)
		}
	}
	for name, reply := range map[string]string{
		"refusal":     "I cannot help with that.",
		"other json":  `{"memories":[]}`,
		"empty brief": `{"brief":""}`,
		"garbage":     "```\n???\n```",
	} {
		if got, err := ParseBriefReply(reply); err == nil || !errors.Is(err, errBriefUnparseable) {
			t.Fatalf("%s: want unparseable, got %q %v", name, got, err)
		}
	}
}

// fakeBriefOpenRouter answers call i with replies[i] (the last one repeats);
// "" means HTTP 503.
func fakeBriefOpenRouter(t *testing.T, replies ...string) (*Service, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["response_format"]; ok {
			t.Errorf("brief call must not force JSON mode")
		}
		reply := replies[len(replies)-1]
		if calls < len(replies) {
			reply = replies[calls]
		}
		calls++
		if reply == "" {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	old := briefRetryDelay
	briefRetryDelay = 0
	t.Cleanup(func() { briefRetryDelay = old })
	cfg := Config{APIKey: "k"}
	return &Service{Cfg: cfg, Client: &Client{Cfg: cfg, BaseURLOverride: srv.URL}}, &calls
}

func TestGenerateBriefReplyVariantsAndFallbackReason(t *testing.T) {
	md := "# Project brief: nexus\nWhat it is: a memory server.\nStack: Go, Postgres\n- Keep LF endings (session:s-arch)"
	escaped := strings.ReplaceAll(md, "\n", `\n`)
	for name, reply := range map[string]string{
		"json":           `{"brief":"` + escaped + `"}`,
		"fenced json":    "```json\n{\"brief\":\"" + escaped + "\"}\n```",
		"plain markdown": md,
	} {
		svc, _ := fakeBriefOpenRouter(t, reply)
		out := svc.GenerateBrief(context.Background(), sampleBriefInput())
		if out.Provider != ProviderOpenRouter || out.Content != md || out.FallbackReason != "" {
			t.Fatalf("%s: %+v", name, out)
		}
	}

	// Garbage: deterministic brief, reason recorded, no error text in content.
	svc, calls := fakeBriefOpenRouter(t, "Sorry, I can't.")
	out := svc.GenerateBrief(context.Background(), sampleBriefInput())
	if out.Provider != ProviderHeuristic || out.FallbackReason != BriefFallbackUnparseable || *calls != 1 {
		t.Fatalf("garbage: calls=%d %+v", *calls, out)
	}
	if out.Content != DeterministicBrief(sampleBriefInput()).Content || strings.Contains(out.Content, "unparseable") {
		t.Fatalf("garbage content: %q", out.Content)
	}

	// Transient 503 then a good reply: retried once, LLM brief.
	svc, calls = fakeBriefOpenRouter(t, "", md)
	out = svc.GenerateBrief(context.Background(), sampleBriefInput())
	if out.Provider != ProviderOpenRouter || *calls != 2 {
		t.Fatalf("retry: calls=%d %+v", *calls, out)
	}

	// 503 every time: one retry only, then fallback with a reason.
	svc, calls = fakeBriefOpenRouter(t, "")
	out = svc.GenerateBrief(context.Background(), sampleBriefInput())
	if out.Provider != ProviderHeuristic || out.FallbackReason != BriefFallbackUpstream || *calls != 2 || out.LLMError == "" {
		t.Fatalf("503 fallback: calls=%d %+v", *calls, out)
	}

	// No key: reason no_api_key.
	if out := NewService(Config{}).GenerateBrief(context.Background(), sampleBriefInput()); out.FallbackReason != BriefFallbackNoKey {
		t.Fatalf("no key reason: %+v", out)
	}
}
