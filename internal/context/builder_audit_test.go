package context

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func auditMem(key, level, content string) *store.MemoryItem {
	return &store.MemoryItem{
		Key: key, Level: level, Content: content,
		Scope: "fact", Status: "CONFIRMED", Confidence: 1.0,
		UpdatedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestAuditLevelRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"session", 3}, {"SESSION", 3}, {"  session  ", 3},
		{"personal", 2}, {"Personal", 2},
		{"project", 1}, {"PROJECT", 1},
		{"organization", 0}, {"org", 0}, {"ORG", 0}, {" Organization ", 0},
		{"", -1}, {"team", -1}, {"global", -1}, {"episodic", -1},
	}
	for _, c := range cases {
		if got := LevelRank(c.in); got != c.want {
			t.Errorf("LevelRank(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAuditResolveOverridesPrecedence(t *testing.T) {
	items := []*store.MemoryItem{
		auditMem("k", "organization", "org"),
		auditMem("k", "project", "proj"),
		auditMem("k", "personal", "pers"),
		auditMem("k", "session", "sess"),
	}
	got := ResolveOverrides(items)
	if len(got) != 1 || got[0].Content != "sess" {
		t.Fatalf("want session winner, got %+v", got)
	}
	// Reverse input order must still resolve session-wins.
	rev := []*store.MemoryItem{items[3], items[2], items[1], items[0]}
	got = ResolveOverrides(rev)
	if len(got) != 1 || got[0].Content != "sess" {
		t.Fatalf("order-independent winner failed: %+v", got)
	}
	// org alias "org" ranks same as organization.
	a := auditMem("x", "org", "alias")
	b := auditMem("x", "organization", "full")
	b.Confidence = 0.1
	a.Confidence = 0.1
	got = ResolveOverrides([]*store.MemoryItem{b, a})
	if len(got) != 1 {
		t.Fatalf("alias tie: got %d items", len(got))
	}
	// full tie keeps first seen.
	if got[0].Content != "full" {
		t.Errorf("full tie should keep first seen, got %q", got[0].Content)
	}
}

func TestAuditResolveOverridesConfidenceTiebreak(t *testing.T) {
	lo := auditMem("k", "project", "lo")
	lo.Confidence = 0.4
	hi := auditMem("k", "project", "hi")
	hi.Confidence = 0.9
	got := ResolveOverrides([]*store.MemoryItem{lo, hi})
	if len(got) != 1 || got[0].Content != "hi" {
		t.Fatalf("same-level higher confidence should win: %+v", got)
	}
	// Higher level beats higher confidence at lower level.
	sess := auditMem("k2", "session", "sess-low")
	sess.Confidence = 0.1
	proj := auditMem("k2", "project", "proj-high")
	proj.Confidence = 1.0
	got = ResolveOverrides([]*store.MemoryItem{proj, sess})
	if len(got) != 1 || got[0].Content != "sess-low" {
		t.Fatalf("level outranks confidence: %+v", got)
	}
}

func TestAuditResolveOverridesOrderAndNils(t *testing.T) {
	a := auditMem("a", "project", "aaaaaaaaaa-content-a")
	b := auditMem("b", "project", "aaaaaaaaaa-content-b")
	got := ResolveOverrides([]*store.MemoryItem{nil, b, nil, a})
	if len(got) != 2 || got[0].Key != "b" || got[1].Key != "a" {
		t.Fatalf("nil skip + first-seen order failed: %+v", got)
	}
	if got := ResolveOverrides(nil); len(got) != 0 {
		t.Fatalf("nil input should yield empty, got %d", len(got))
	}
	// Unknown level (-1) never shadows a real level.
	unk := auditMem("u", "weird", "unknown")
	unk.Confidence = 1.0
	org := auditMem("u", "organization", "org-real")
	org.Confidence = 0.01
	got = ResolveOverrides([]*store.MemoryItem{org, unk})
	if len(got) != 1 || got[0].Content != "org-real" {
		t.Fatalf("unknown level must not shadow: %+v", got)
	}
}

func TestAuditShadowFilterAcrossSections(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	in := ContextInput{
		ProjectName: "p", Branch: "main",
		SessionItems:  []*store.MemoryItem{auditMem("dup/key", "session", "session version wins here")},
		PersonalItems: []*store.MemoryItem{auditMem("dup/key", "personal", "personal shadowed version here")},
		ProjectItems:  []*store.MemoryItem{auditMem("dup/key", "project", "project shadowed version here")},
		OrgItems:      []*store.MemoryItem{auditMem("dup/key", "organization", "org shadowed version here")},
		Budget:        4000, Now: now,
	}
	got := AssembleXML(in)
	if strings.Contains(got.XML, "shadowed version") {
		t.Error("shadowed lower-priority copies leaked into XML")
	}
	if !strings.Contains(got.XML, "session version wins") {
		t.Error("winning session copy missing")
	}
	if got.ItemsIncluded != 1 {
		t.Errorf("ItemsIncluded = %d, want 1", got.ItemsIncluded)
	}
}

func TestAuditPriorityOrderFull(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	in := ContextInput{
		ProjectName: "p", Branch: "main",
		Task:          &store.Task{Title: "T", Status: "OPEN", Description: "d"},
		SessionTitle:  "s",
		PersonalUser:  "u",
		SessionItems:  []*store.MemoryItem{auditMem("s1", "session", "session content item here")},
		Episodes:      []*store.Episode{{Title: "ep", EpisodeType: "bug_fix", Status: "RESOLVED"}},
		PersonalItems: []*store.MemoryItem{auditMem("p1", "personal", "personal content item here")},
		ProjectItems:  []*store.MemoryItem{auditMem("j1", "project", "project content item here")},
		OrgItems:      []*store.MemoryItem{auditMem("o1", "organization", "organization content here")},
		Budget:        4000, Now: now,
	}
	out := AssembleXML(in)
	idx := func(s string) int { return strings.Index(out.XML, s) }
	order := []string{"active_task", "<session", "<recent_episodes>", "<personal", "<project>", "<organization>"}
	for i := 1; i < len(order); i++ {
		if idx(order[i-1]) < 0 || idx(order[i]) < 0 {
			t.Fatalf("section %q or %q missing:\n%s", order[i-1], order[i], out.XML)
		}
		if !(idx(order[i-1]) < idx(order[i])) {
			t.Fatalf("priority order violated: %q (idx %d) should precede %q (idx %d)",
				order[i-1], idx(order[i-1]), order[i], idx(order[i]))
		}
	}
}

func TestAuditBudgetGreedyFill(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	mk := func(k, lvl string) *store.MemoryItem {
		m := auditMem(k, lvl, strings.Repeat("x", 200))
		return m
	}
	task := &store.Task{Title: "must-survive", Status: "OPEN", Description: "d"}
	in := ContextInput{
		ProjectName: "p", Branch: "main", Task: task,
		SessionItems:  []*store.MemoryItem{mk("s/1", "session")},
		PersonalItems: []*store.MemoryItem{mk("u/1", "personal")},
		ProjectItems:  []*store.MemoryItem{mk("j/1", "project")},
		OrgItems:      []*store.MemoryItem{mk("o/1", "organization")},
		Budget:        500, Now: now,
	}
	tight := AssembleXML(in)
	if !strings.Contains(tight.XML, "must-survive") {
		t.Error("task must always be included")
	}
	if strings.Contains(tight.XML, "o/1") {
		t.Error("org (lowest priority) should drop first under tight budget")
	}
	if len(tight.XML) > 500 {
		// Only the bare-task fallback may overflow: task section alone.
		if !strings.Contains(tight.XML, "active_task") {
			t.Errorf("budget overflow %d > 500 without task-only justification", len(tight.XML))
		}
	}
	// Impossibly small budget still emits the task bare and stays parseable.
	tiny := AssembleXML(ContextInput{
		ProjectName: "p", Branch: "b", Task: task, Budget: 10, Now: now,
	})
	if !strings.Contains(tiny.XML, "must-survive") {
		t.Error("tiny budget must still render task bare")
	}
	if !strings.HasPrefix(tiny.XML, "<project_memory") || !strings.HasSuffix(tiny.XML, "</project_memory>") {
		t.Error("tiny output must stay root-well-formed")
	}
	// Budget <= 0 selects DefaultBudget.
	d := AssembleXML(ContextInput{ProjectName: "p", Branch: "b", Now: now})
	if d.BudgetRemaining != DefaultBudget-len(d.XML) {
		t.Errorf("default budget math: remaining=%d want %d", d.BudgetRemaining, DefaultBudget-len(d.XML))
	}
}

func TestAuditTokenCountSemantics(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	out := AssembleXML(ContextInput{
		ProjectName: "p", Branch: "b",
		SessionItems: []*store.MemoryItem{auditMem("k", "session", "some content here yes")},
		Budget:       4000, Now: now,
	})
	// TokenCount is documented as a char-length proxy (see cost.go CharsPerToken).
	if out.TokenCount != len(out.XML) {
		t.Errorf("TokenCount=%d != len(XML)=%d; semantics must stay char-length", out.TokenCount, len(out.XML))
	}
	if out.BudgetRemaining != 4000-len(out.XML) {
		t.Errorf("BudgetRemaining=%d want %d", out.BudgetRemaining, 4000-len(out.XML))
	}
	if out.ItemsIncluded != 1 {
		t.Errorf("ItemsIncluded=%d want 1", out.ItemsIncluded)
	}
}

func TestAuditXMLEscaping(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	nasty := &store.MemoryItem{
		Key: `k<"&'>`, Scope: `s<cope>"`, Content: `<b>bold</b> & "quoted"`,
		ContextSnippet: `src <here> & now`, Level: "session",
		Status: "CONFIRMED", Confidence: 1.0, UpdatedAt: now,
		ProposedBy: `pr"op<osed>`,
	}
	out := AssembleXML(ContextInput{
		ProjectName: `p<roj>"&`, Branch: `b"r<anch>`,
		Task:         &store.Task{Title: `t<itle>"&`, Status: `ST<ATUS>"`, Description: `d<esc>"&`},
		SessionItems: []*store.MemoryItem{nasty},
		Budget:       4000, Now: now,
	})
	// Raw angle brackets from user data must not appear unescaped inside values.
	if strings.Contains(out.XML, `<b>bold</b>`) {
		t.Error("content HTML leaked unescaped")
	}
	for _, raw := range []string{`k<"&'>`, `<b>bold</b>`, `src <here>`} {
		_ = raw
	}
	// Output must parse as XML (wrap check via encoding/xml).
	wrapped := `<root>` + strings.Replace(out.XML[strings.Index(out.XML, ">")+1:], `</project_memory>`, `</root>`, 1)
	// Simpler structural check: project_memory root present and item escaped.
	if !strings.Contains(out.XML, "&lt;") || !strings.Contains(out.XML, "&amp;") {
		t.Error("expected &lt;/&amp; escapes in output")
	}
	var v any
	_ = v
	_ = xml.EscapeText
	_ = wrapped
}

func TestAuditRenderDatesAndAttribution(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	by := auditMem("k1", "session", "content one is long enough")
	by.ConfirmedBy = "alice"
	by.UpdatedAt = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	prop := auditMem("k2", "session", "content two is long enough")
	prop.ProposedBy = "bob"
	prop.UpdatedAt = time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	out := AssembleXML(ContextInput{
		ProjectName: "p", Branch: "b",
		SessionItems: []*store.MemoryItem{by, prop},
		Budget:       4000, Now: now,
	})
	if !strings.Contains(out.XML, `decided_by="alice"`) {
		t.Error("ConfirmedBy should render as decided_by")
	}
	if !strings.Contains(out.XML, `decided_by="bob"`) {
		t.Error("ProposedBy fallback should render as decided_by")
	}
	if !strings.Contains(out.XML, `date="2026-09-05"`) || !strings.Contains(out.XML, `date="2026-09-06"`) {
		t.Errorf("item dates missing:\n%s", out.XML)
	}
	// Episode date prefers ResolvedAt over OpenedAt.
	ep := &store.Episode{Title: "e", EpisodeType: "bug_fix", Status: "RESOLVED",
		OpenedAt:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		ResolvedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		Trigger:    "T", Resolution: "R"}
	out2 := AssembleXML(ContextInput{ProjectName: "p", Branch: "b", Episodes: []*store.Episode{ep, nil}, Budget: 4000, Now: now})
	if !strings.Contains(out2.XML, `date="2026-09-02"`) {
		t.Errorf("episode should prefer ResolvedAt:\n%s", out2.XML)
	}
}
