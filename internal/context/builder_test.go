package context

import (
	"encoding/xml"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func delta(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// Decay anchors from plan §1.7: 0d ≈ base, 90d ≈ 86%, 180d ≈ 74%.
func TestDecayMath(t *testing.T) {
	if got := Decay(1.0, 0); !delta(got, 1.0, 1e-12) {
		t.Errorf("0d = %v, want 1.0", got)
	}
	if got := Decay(1.0, 90); !delta(got, 0.857375, 1e-9) {
		t.Errorf("90d = %v, want ~0.857375 (86%%)", got)
	}
	if got := Decay(1.0, 180); !delta(got, 0.735091890625, 1e-9) {
		t.Errorf("180d = %v, want ~0.73509189 (74%%)", got)
	}
	if got := Decay(0.9, 90); !delta(got, 0.9*0.857375, 1e-9) {
		t.Errorf("base scales: 0.9@90d = %v", got)
	}
	if got := Decay(0.8, -5); got != 0.8 {
		t.Errorf("negative days = %v, want base (clamped)", got)
	}
	if got := Decay(2.0, 0); got != 1 {
		t.Errorf("over-range base = %v, want clamp to 1", got)
	}
}

func TestEffectiveConfidence(t *testing.T) {
	if got := EffectiveConfidence(0.9, time.Time{}, testNow); got != 0.9 {
		t.Errorf("zero lastUsed = %v, want base", got)
	}
	if got := EffectiveConfidence(1.0, testNow.AddDate(0, 0, -90), testNow); !delta(got, 0.857375, 1e-9) {
		t.Errorf("90d stale = %v, want ~0.857", got)
	}
}

// Full precedence chain: SESSION > PERSONAL > PROJECT > ORGANIZATION.
func TestResolveOverridePrecedence(t *testing.T) {
	items := []Item{
		{Key: "k", Content: "org", Level: LevelOrganization, Confidence: 1},
		{Key: "k", Content: "project", Level: LevelProject, Confidence: 1},
		{Key: "k", Content: "personal", Level: LevelPersonal, Confidence: 1},
		{Key: "k", Content: "session", Level: LevelSession, Confidence: 0.1},
	}
	got := ResolveOverride(items)
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if got[0].Content != "session" {
		t.Errorf("winner = %q, want session (lowest confidence still wins)", got[0].Content)
	}
}

// Session-overrides-project acceptance case, distinct keys preserved.
func TestResolveOverrideSessionOverProject(t *testing.T) {
	items := []Item{
		{Key: "testing/framework", Content: "we use pytest", Level: LevelProject, Confidence: 0.95},
		{Key: "testing/framework", Content: "use vitest for this session", Level: LevelSession, Confidence: 1},
		{Key: "other", Content: "untouched", Level: LevelProject},
	}
	got := ResolveOverride(items)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	byKey := map[string]Item{}
	for _, it := range got {
		byKey[it.Key] = it
	}
	if byKey["testing/framework"].Content != "use vitest for this session" {
		t.Errorf("session did not shadow project: %+v", byKey["testing/framework"])
	}
	if byKey["other"].Content != "untouched" {
		t.Errorf("distinct key altered: %+v", byKey["other"])
	}
}

func TestResolveOverrideTieBreakConfidence(t *testing.T) {
	got := ResolveOverride([]Item{
		{Key: "k", Content: "low", Level: LevelProject, Confidence: 0.4},
		{Key: "k", Content: "high", Level: LevelProject, Confidence: 0.9},
	})
	if len(got) != 1 || got[0].Content != "high" {
		t.Errorf("same-level tie should prefer confidence: %+v", got)
	}
}

// BuildXML: valid XML within budget, task kept, org dropped first.
func TestBuildXMLBudgetTruncationOrder(t *testing.T) {
	long := strings.Repeat("x", 300)
	in := ContextInput{
		ProjectName: "demo", Branch: "main", Now: testNow,
		Task:         &ActiveTask{Title: "T", Status: "IN_PROGRESS", Description: "do the thing"},
		Session:      []Item{{Key: "s1", Content: "session fact " + long, Level: LevelSession, Confidence: 1}},
		Personal:     []Item{{Key: "p1", Content: "personal fact " + long, Level: LevelPersonal, Confidence: 1}},
		Project:      []Item{{Key: "pr1", Content: "project fact " + long, Level: LevelProject, Confidence: 1}},
		Organization: []Item{{Key: "o1", Content: "org fact " + long, Level: LevelOrganization, Confidence: 1}},
		SessionTitle: "S", PersonalUser: "alice",
	}
	out, stats := BuildXML(in, 900)
	assertWellFormed(t, out)
	if len(out) > 900 {
		t.Errorf("len %d exceeds budget 900", len(out))
	}
	for _, want := range []string{"<active_task", "<session", "session fact"} {
		if !strings.Contains(out, want) {
			t.Errorf("high-priority %q missing under pressure", want)
		}
	}
	if strings.Contains(out, "org fact") {
		t.Error("lowest-priority org section should be truncated first")
	}
	if len(stats.TruncatedSections) == 0 {
		t.Error("expected truncated sections to be reported")
	}
	if stats.ItemsDropped == 0 || stats.ItemsIncluded == 0 {
		t.Errorf("suspicious stats: %+v", stats)
	}
	if stats.CharsUsed != len(out) || stats.BudgetChars != 900 {
		t.Errorf("stats mismatch: %+v len=%d", stats, len(out))
	}
}

// Session shadows project inside BuildXML even when the caller passes raw,
// un-resolved items to both sections.
func TestBuildXMLSessionOverridesProject(t *testing.T) {
	in := ContextInput{
		ProjectName: "demo", Now: testNow,
		Project:      []Item{{Key: "testing/framework", Content: "we use pytest", Level: LevelProject, Scope: "decision", Confidence: 0.95}},
		Session:      []Item{{Key: "testing/framework", Content: "use vitest for this session", Level: LevelSession, Scope: "constraint", Confidence: 1}},
		SessionTitle: "Spike",
	}
	out, _ := BuildXML(in, 4000)
	assertWellFormed(t, out)
	if strings.Count(out, `key="testing/framework"`) != 1 {
		t.Errorf("key must appear exactly once:\n%s", out)
	}
	if !strings.Contains(out, "use vitest for this session") {
		t.Errorf("session content missing:\n%s", out)
	}
	if strings.Contains(out, "we use pytest") {
		t.Errorf("shadowed project content leaked:\n%s", out)
	}
	if !strings.Contains(out, "<session") {
		t.Errorf("winner must render in <session>:\n%s", out)
	}
}

// Full document: every section renders in plan §1.6 order with decayed
// confidence on display.
func TestBuildXMLFullDocument(t *testing.T) {
	in := ContextInput{
		ProjectName: "central-memory", Branch: "main", Now: testNow,
		PersonalUser: "alice", SessionTitle: "Auth refactor",
		Organization: []Item{{Key: "security/auth", Content: "All APIs must use JWT.", Level: LevelOrganization, Scope: "constraint", Confidence: 1}},
		Project:      []Item{{Key: "testing/framework", Content: "pytest + testcontainers.", Level: LevelProject, Scope: "decision", Confidence: 0.95, DecidedBy: "Alice", Date: "2026-09-10", LastUsedAt: testNow.AddDate(0, 0, -90)}},
		Personal:     []Item{{Key: "style/errors", Content: "Verbose errors.", Level: LevelPersonal, Scope: "preference", Confidence: 0.9}},
		Session:      []Item{{Key: "scope/lock", Content: "Do not touch payments/.", Level: LevelSession, Scope: "constraint", Confidence: 1}},
		Episodes:     []Episode{{Type: "bug_fix", Title: "WS timeout", Status: "RESOLVED", Date: "2026-09-15", Summary: "pool lifetime fix."}},
		Task:         &ActiveTask{Title: "WS auth", Status: "IN_PROGRESS", Assigned: "claude", Description: "Verify JWT on upgrade."},
	}
	out, stats := BuildXML(in, 4000)
	assertWellFormed(t, out)
	order := []string{"<organization>", "<project>", `<personal user="alice">`, `<session title="Auth refactor">`, "<recent_episodes>", "<active_task"}
	last := -1
	for _, tag := range order {
		i := strings.Index(out, tag)
		if i < 0 {
			t.Fatalf("missing section %s:\n%s", tag, out)
		}
		if i < last {
			t.Fatalf("section %s out of document order", tag)
		}
		last = i
	}
	// 90-day-old project item displays decayed confidence 0.95*0.857 ≈ 0.81.
	if !strings.Contains(out, `confidence="0.81"`) {
		t.Errorf("expected decayed confidence 0.81 in output:\n%s", out)
	}
	if stats.ItemsDropped != 0 || stats.ItemsIncluded != 6 { // 4 items + 1 episode + task
		t.Errorf("stats = %+v, want 7 included 0 dropped", stats)
	}
}

func TestBuildXMLDefaultBudget(t *testing.T) {
	out, stats := BuildXML(ContextInput{ProjectName: "p", Now: testNow}, 0)
	assertWellFormed(t, out)
	if stats.BudgetChars != DefaultBudgetChars {
		t.Errorf("budget = %d, want default %d", stats.BudgetChars, DefaultBudgetChars)
	}
}

func TestBuildXMLSpecialCharsEscaped(t *testing.T) {
	in := ContextInput{
		ProjectName: "a<b",
		Now:         testNow,
		Project:     []Item{{Key: `k"1`, Content: "a < b & c", Level: LevelProject, Confidence: 1}},
	}
	out, _ := BuildXML(in, 4000)
	assertWellFormed(t, out)
	if strings.Contains(out, "a < b") || strings.Contains(out, `k"1`) {
		t.Errorf("unescaped content:\n%s", out)
	}
}

func assertWellFormed(t *testing.T, out string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(out))
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML (%v):\n%s", err, out)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if depth != 0 {
		t.Fatalf("unbalanced XML:\n%s", out)
	}
	if !strings.HasPrefix(out, "<project_memory") || !strings.HasSuffix(out, "</project_memory>\n") {
		t.Fatalf("missing root element:\n%s", out)
	}
}
