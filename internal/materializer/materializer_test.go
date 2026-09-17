package materializer

import (
	"context"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func confirmedItems() []*store.MemoryItem {
	return []*store.MemoryItem{
		{Key: "testing/framework", Content: "The team uses pytest with fixtures and 80% coverage target.", Scope: "decision", Status: "CONFIRMED", Confidence: 0.95},
		{Key: "security/auth", Content: "All APIs must use JWT authentication.", Scope: "constraint", Status: "CONFIRMED", Confidence: 1.0},
		{Key: "scratch", Content: "Temporary note that should rank last.", Scope: "fact", Status: "PROPOSED", Confidence: 0.5},
	}
}

func TestShouldTrigger(t *testing.T) {
	if !ShouldTrigger("MEMORY_CONFIRMED") || !ShouldTrigger("MEMORY_SUPERSEDED") {
		t.Fatal("confirmed/superseded must trigger")
	}
	for _, e := range []string{"MEMORY_PROPOSED", "FILE_MODIFIED", "CONVERSATION_TURN", ""} {
		if ShouldTrigger(e) {
			t.Fatalf("%q must not trigger", e)
		}
	}
}

func TestRenderMarkdownBudget(t *testing.T) {
	out := RenderMarkdown(confirmedItems(), 10000)
	if !strings.Contains(out, "testing/framework") || !strings.Contains(out, "security/auth") {
		t.Fatalf("expected both keys in output:\n%s", out)
	}
	tiny := RenderMarkdown(confirmedItems(), 60)
	if len(tiny) > 60 {
		t.Fatalf("budget exceeded: %d > 60", len(tiny))
	}
}

func TestRenderTextBudget(t *testing.T) {
	out := RenderText(confirmedItems(), 10000)
	if strings.Contains(out, "**") || strings.Contains(out, "#") {
		t.Fatalf("text format must not contain markdown:\n%s", out)
	}
	if !strings.Contains(out, "security/auth") {
		t.Fatalf("expected key in output:\n%s", out)
	}
}

func TestMergeManagedPreservesUserContent(t *testing.T) {
	existing := "# My notes\nKeep this line.\n\n" + BeginMarker + "\nold generated\n" + EndMarker + "\n"
	merged := MergeManaged(existing, "new generated")
	if !strings.Contains(merged, "Keep this line.") {
		t.Fatalf("user content lost:\n%s", merged)
	}
	if !strings.Contains(merged, "new generated") {
		t.Fatalf("generated content missing:\n%s", merged)
	}
	if strings.Contains(merged, "old generated") {
		t.Fatalf("stale managed body not replaced:\n%s", merged)
	}
}

func TestMergeManagedCreatesSection(t *testing.T) {
	merged := MergeManaged("# Title\nbody\n", "gen")
	if !strings.Contains(merged, BeginMarker) || !strings.Contains(merged, EndMarker) {
		t.Fatalf("section not created:\n%s", merged)
	}
	if !strings.Contains(merged, "body") {
		t.Fatalf("existing content lost:\n%s", merged)
	}
}

func TestOutsideEditDetection(t *testing.T) {
	old := "user line\n" + BeginMarker + "\ngen\n" + EndMarker + "\n"
	same := "user line\n" + BeginMarker + "\ngen v2\n" + EndMarker + "\n"
	if OutsideEditChanged(old, same) {
		t.Fatal("managed-only change must not count as outside edit")
	}
	edited := "user line\nadded by human\n" + BeginMarker + "\ngen v2\n" + EndMarker + "\n"
	if !OutsideEditChanged(old, edited) {
		t.Fatal("outside edit not detected")
	}
	proposal, ok := OutsideEditProposal(old, edited)
	if !ok || !strings.Contains(proposal, "added by human") {
		t.Fatalf("bad proposal %q ok=%v", proposal, ok)
	}
	if _, ok := OutsideEditProposal(old, same); ok {
		t.Fatal("no proposal expected for managed-only change")
	}
}

// stubSource feeds canned memories; stubBus replays events.
type stubSource struct{ items []*store.MemoryItem }

func (s stubSource) SearchMemory(ctx context.Context, projectID string, query string, tags []string, limit int) ([]*store.MemoryItem, error) {
	return s.items, nil
}

type stubBus struct {
	ch chan *store.Event
}

func (b stubBus) Subscribe(ctx context.Context, projectID string) (<-chan *store.Event, func(), error) {
	return b.ch, func() {}, nil
}

func TestRegenerateWritesAllTargets(t *testing.T) {
	files := map[string]string{}
	m := &Materializer{
		ProjectID: "p1",
		Targets:   DefaultTargets(),
		Source:    stubSource{confirmedItems()},
		Read:      func(p string) (string, error) { return files[p], nil },
		Write:     func(p, c string) error { files[p] = c; return nil },
	}
	if err := m.Regenerate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, target := range DefaultTargets() {
		c, ok := files[target.FilePath]
		if !ok {
			t.Fatalf("missing write for %s", target.FilePath)
		}
		if !strings.Contains(c, BeginMarker) || len(c) > target.Budget+512 {
			t.Fatalf("bad content for %s (len=%d)", target.FilePath, len(c))
		}
	}
	if !strings.Contains(files[".cursorrules"], "security/auth") {
		t.Fatal("cursorrules missing memory content")
	}
}

func TestRunDebouncesBatch(t *testing.T) {
	ch := make(chan *store.Event, 16)
	writes := 0
	files := map[string]string{}
	m := &Materializer{
		ProjectID: "p1",
		Targets:   []Target{{AgentName: "cursor", FilePath: ".cursorrules", Format: "text", Budget: 6000}},
		Debounce:  50 * time.Millisecond,
		Source:    stubSource{confirmedItems()},
		Bus:       stubBus{ch},
		Read:      func(p string) (string, error) { return files[p], nil },
		Write:     func(p, c string) error { writes++; files[p] = c; return nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	// Batch of 5 triggers + 1 noise event: debounce must collapse to 1 write.
	for i := 0; i < 5; i++ {
		ch <- &store.Event{ProjectID: "p1", EventType: "MEMORY_CONFIRMED"}
	}
	ch <- &store.Event{ProjectID: "p1", EventType: "CONVERSATION_TURN"}
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if writes != 1 {
		t.Fatalf("expected 1 debounced write, got %d", writes)
	}
}
