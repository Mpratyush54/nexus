package materializer

import (
	"context"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func auditItems() []*store.MemoryItem {
	return []*store.MemoryItem{
		{Key: "b", Content: "second", Scope: "fact", Status: "PROPOSED", Confidence: 0.5},
		{Key: "a", Content: "first-confirmed", Scope: "fact", Status: "CONFIRMED", Confidence: 0.9},
		nil,
		{Key: "c", Content: "  padded  ", Scope: "fact", Status: "CONFIRMED", Confidence: 0.4},
	}
}

func TestAuditShouldTriggerAndDebounce(t *testing.T) {
	if Debounce != 5*time.Second {
		t.Errorf("Debounce = %v, want 5s", Debounce)
	}
	if !ShouldTrigger(EventConfirmed) || !ShouldTrigger(EventSuperseded) {
		t.Error("MEMORY_CONFIRMED/SUPERSEDED must trigger")
	}
	for _, ev := range []string{"", "MEMORY_CREATED", "memory_confirmed", "SESSION_STARTED", "MEMORY_REJECTED"} {
		if ShouldTrigger(ev) {
			t.Errorf("ShouldTrigger(%q) = true, want false", ev)
		}
	}
}

func TestAuditDelimiters(t *testing.T) {
	if !strings.Contains(BeginMarker, "DO NOT EDIT") || !strings.Contains(EndMarker, "END CENTRAL MEMORY") {
		t.Error("primary delimiters malformed")
	}
	// ASCII alt begin accepted; both ends accepted (they are identical strings here).
	pre, _, _, found := ParseManaged("top\n" + BeginMarkerAlt + "\ngen\n" + EndMarkerAlt + "\nbottom")
	if !found || !strings.Contains(pre, "top") {
		t.Errorf("alt delimiters must parse: found=%v pre=%q", found, pre)
	}
	// Missing begin -> not found, whole content returned as pre.
	pre, _, _, found = ParseManaged("no markers here")
	if found || pre != "no markers here" {
		t.Errorf("missing section: found=%v pre=%q", found, pre)
	}
	// Begin without end -> found with trailing managed.
	_, man, post, found := ParseManaged("pre\n" + BeginMarker + "\norphan")
	if !found || !strings.Contains(man, "orphan") || post != "" {
		t.Errorf("unterminated section: found=%v man=%q post=%q", found, man, post)
	}
}

func TestAuditMergeManagedBytePreservation(t *testing.T) {
	existing := "user line 1\nuser line 2\n" + BeginMarker + "\nold gen\n" + EndMarker + "\nuser tail\n"
	merged := MergeManaged(existing, "new gen")
	pre, man, post, found := ParseManaged(merged)
	if !found {
		t.Fatal("merged output lost managed section")
	}
	if pre != "user line 1\nuser line 2\n" {
		t.Errorf("preamble mutated: %q", pre)
	}
	if post != "\nuser tail\n" && post != "user tail\n" && !strings.Contains(post, "user tail") {
		t.Errorf("postamble mutated: %q", post)
	}
	if !strings.Contains(man, "new gen") || strings.Contains(man, "old gen") {
		t.Errorf("managed body wrong: %q", man)
	}
	// No section -> appended after blank line, user bytes preserved.
	plain := "just user text"
	merged = MergeManaged(plain, "gen")
	if !strings.HasPrefix(merged, "just user text\n\n"+BeginMarker) {
		t.Errorf("append layout wrong:\n%s", merged)
	}
	// Empty file -> bare section.
	merged = MergeManaged("  \n ", "gen")
	if !strings.Contains(merged, BeginMarker) || !strings.Contains(merged, EndMarker) {
		t.Errorf("empty file should yield bare section:\n%s", merged)
	}
	// Alt markers round-trip in kind.
	alt := "u\n" + BeginMarkerAlt + "\nold\n" + EndMarkerAlt + "\ntail"
	if m := MergeManaged(alt, "g"); !strings.Contains(m, BeginMarkerAlt) {
		t.Errorf("alt marker kind must be preserved:\n%s", m)
	}
}

func TestAuditRenderBudgetCaps(t *testing.T) {
	items := auditItems()
	md := RenderMarkdown(items, 10000)
	if !strings.HasPrefix(md, "# Project Memory") {
		t.Error("markdown header missing")
	}
	// CONFIRMED-first: confirmed keys precede PROPOSED.
	if strings.Index(md, "first-confirmed") > strings.Index(md, "second") {
		t.Errorf("CONFIRMED-first violated:\n%s", md)
	}
	if strings.Contains(md, "padded  ") || !strings.Contains(md, "padded") {
		t.Error("content should be trimmed")
	}
	if got := RenderMarkdown(items, 0); got != "" {
		t.Errorf("budget<=0 markdown should be empty: %q", got)
	}
	if got := RenderText(items, -1); got != "" {
		t.Errorf("budget<=0 text should be empty: %q", got)
	}
	small := RenderMarkdown(items, 60)
	if len(small) > 60 {
		t.Errorf("markdown exceeded budget: %d > 60", len(small))
	}
	txt := RenderText(items, 10000)
	if strings.Contains(txt, "**") || strings.Contains(txt, "#") {
		t.Errorf("text format must be plain:\n%s", txt)
	}
	if small2 := RenderText(items, 10); len(small2) > 10 {
		t.Errorf("text exceeded budget: %d", len(small2))
	}
}

func TestAuditOutsideEditProposal(t *testing.T) {
	oldC := "pre-a\n" + BeginMarker + "\ngen\n" + EndMarker + "\npost-a\n"
	newC := "pre-a\npre-B-new\n" + BeginMarker + "\ngen-changed\n" + EndMarker + "\npost-a\n"
	if !OutsideEditChanged(oldC, newC) {
		t.Error("outside pre edit must register")
	}
	prop, ok := OutsideEditProposal(oldC, newC)
	if !ok || !strings.Contains(prop, "pre-B-new") {
		t.Errorf("proposal should carry added line: %q ok=%v", prop, ok)
	}
	// Managed-only churn is invisible.
	if OutsideEditChanged(oldC, "pre-a\n"+BeginMarker+"\ngen2\n"+EndMarker+"\npost-a\n") {
		t.Error("managed-only change must be ignored")
	}
	if _, ok := OutsideEditProposal(oldC, oldC); ok {
		t.Error("identical content must yield no proposal")
	}
	// Deletion-only outside edit still signals with surviving outside text.
	del := "pre-a\n" + BeginMarker + "\ngen\n" + EndMarker + "\n"
	if prop, ok := OutsideEditProposal(oldC, del); !ok || prop == "" {
		t.Errorf("deletion should still propose surviving text: %q ok=%v", prop, ok)
	}
}

type auditSource struct {
	items []*store.MemoryItem
	err   error
	calls int
}

func (s *auditSource) SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*store.MemoryItem, error) {
	s.calls++
	return s.items, s.err
}

func TestAuditCheckFileAndRender(t *testing.T) {
	oldC := "pre\n" + BeginMarker + "\ng\n" + EndMarker + "\npost\n"
	cur := "pre-extra\n" + BeginMarker + "\ng\n" + EndMarker + "\npost\n"
	var gotPath, gotProp string
	m := &Materializer{
		Read:              func(path string) (string, error) { return cur, nil },
		OnOutsideProposal: func(fp, p string) { gotPath, gotProp = fp, p },
	}
	tgt := Target{AgentName: "cursor", FilePath: ".cursorrules", Format: "text", Budget: 100}
	if err := m.CheckFile(context.Background(), tgt, oldC); err != nil {
		t.Fatalf("CheckFile: %v", err)
	}
	if gotPath != ".cursorrules" || !strings.Contains(gotProp, "pre-extra") {
		t.Errorf("proposal wiring wrong: path=%q prop=%q", gotPath, gotProp)
	}
	// Nil Read is a no-op (sandbox owns I/O).
	m2 := &Materializer{}
	if err := m2.CheckFile(context.Background(), tgt, oldC); err != nil {
		t.Errorf("nil Read should no-op: %v", err)
	}
	// Render sorts highest-confidence first and honors format.
	src := &auditSource{items: []*store.MemoryItem{
		{Key: "lo", Content: "low-conf content here", Status: "CONFIRMED", Confidence: 0.1},
		{Key: "hi", Content: "high-conf content here", Status: "CONFIRMED", Confidence: 0.99},
	}}
	m3 := &Materializer{Source: src, Targets: []Target{{AgentName: "x", FilePath: "f", Format: "markdown", Budget: 10000}}}
	out := m3.Render(m3.Targets[0], src.items)
	if strings.Index(out, "high-conf") > strings.Index(out, "low-conf") {
		t.Errorf("Render must be confidence-desc:\n%s", out)
	}
	if got := m3.Render(Target{Format: "TEXT", Budget: 10000}, src.items); strings.Contains(got, "**") {
		t.Errorf("case-insensitive text format broken:\n%s", got)
	}
	// Regenerate writes every target via MergeManaged.
	written := map[string]string{}
	m4 := &Materializer{
		ProjectID: "p", Targets: DefaultTargets(), Source: src,
		Read:  func(p string) (string, error) { return "", nil },
		Write: func(p, c string) error { written[p] = c; return nil },
	}
	if err := m4.Regenerate(context.Background()); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if len(written) != 3 {
		t.Errorf("Regenerate wrote %d targets, want 3", len(written))
	}
	for p, c := range written {
		if !strings.Contains(c, BeginMarker) {
			t.Errorf("target %s missing managed section", p)
		}
	}
	if len(DefaultTargets()) != 3 {
		t.Errorf("DefaultTargets = %d, want 3", len(DefaultTargets()))
	}
}
