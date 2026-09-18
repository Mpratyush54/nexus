package daemon

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Classification (plan §2.6)
// ---------------------------------------------------------------------------

func TestClassifyLevel(t *testing.T) {
	cases := []struct {
		text string
		want MemoryLevel
	}{
		{"I prefer verbose error messages with full stack context", LevelPersonal},
		{"I always write table-driven tests", LevelPersonal},
		{"Don't touch payments/ right now, we're mid-refactor", LevelSession},
		{"For now, skip the migration step during this task", LevelSession},
		{"All APIs must use JWT authentication across all projects", LevelOrganization},
		{"Company policy: every service ships an audit log", LevelOrganization},
		{"We use pytest with fixture-based setup for this codebase", LevelProject},
		{"The team decided on Redis over Memcached due to pub/sub needs", LevelProject},
		{"The cache TTL is 300 seconds", LevelSession}, // unsure ⇒ safe default
	}
	for _, c := range cases {
		if got := ClassifyLevel(c.text); got != c.want {
			t.Errorf("ClassifyLevel(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestClassifyScope(t *testing.T) {
	cases := []struct {
		text string
		want MemoryScope
	}{
		{"All APIs must use JWT. No API key auth.", ScopeConstraint},
		{"Do not modify payments/ during this session", ScopeConstraint},
		{"Team decided on Redis over Memcached due to pub/sub", ScopeDecision},
		{"Alice prefers detailed error messages", ScopePreference},
		{"We follow the repository pattern for all stores", ScopePattern},
		{"Root cause: pool lifetime too short; how we fixed it", ScopeEpisodeSummary},
		{"The server listens on port 8080", ScopeFact},
	}
	for _, c := range cases {
		if got := ClassifyScope(c.text); got != c.want {
			t.Errorf("ClassifyScope(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Deduplication (issue #10: similarity > 0.9 discarded)
// ---------------------------------------------------------------------------

func TestIsDuplicate(t *testing.T) {
	existing := []MemoryRecord{
		{Key: "testing/framework", Content: "The team uses pytest with fixture-based setup and testcontainers for Postgres"},
	}
	dup, score := IsDuplicate("The team uses pytest with fixture-based setup and testcontainers for Postgres!", existing)
	if !dup {
		t.Errorf("near-identical memory not flagged duplicate (score=%v)", score)
	}
	dup, _ = IsDuplicate("The team uses pytest with fixture-based setup and testcontainers for Postgres", existing)
	if !dup {
		t.Error("identical memory not flagged duplicate")
	}
	dup, _ = IsDuplicate("We deploy on Fridays using blue-green releases", existing)
	if dup {
		t.Error("unrelated memory flagged duplicate")
	}
}

func TestDeduplicateDropsBatchInternalDupes(t *testing.T) {
	proposals := []Proposal{
		{Key: "a/b", Content: "The team uses pytest with fixtures"},
		{Key: "a/b", Content: "The team uses pytest with fixtures"},
	}
	if got := Deduplicate(proposals, nil); len(got) != 1 {
		t.Errorf("expected 1 survivor, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Promotion (plan §2.7: SESSION → PROJECT after 3 sessions)
// ---------------------------------------------------------------------------

func TestPromotionThreshold(t *testing.T) {
	if ShouldPromoteSessionToProject(2) {
		t.Error("2 sessions must not promote")
	}
	if !ShouldPromoteSessionToProject(3) {
		t.Error("3 sessions must promote")
	}
	if got := PromotionFor(MemoryRecord{Level: LevelSession}, 3); got != LevelProject {
		t.Errorf("PromotionFor(session, 3) = %q, want project", got)
	}
	if got := PromotionFor(MemoryRecord{Level: LevelSession}, 2); got != "" {
		t.Errorf("PromotionFor(session, 2) = %q, want no promotion", got)
	}
	if got := PromotionFor(MemoryRecord{Level: LevelProject}, 5); got != "" {
		t.Errorf("PromotionFor(project, 5) = %q, want no promotion", got)
	}
}

// ---------------------------------------------------------------------------
// Confirmation timers (plan §2.8)
// ---------------------------------------------------------------------------

func TestConfirmationDelay(t *testing.T) {
	if got := ConfirmationDelay(Proposal{Confidence: 0.7}); got != 24*time.Hour {
		t.Errorf("default = %v, want 24h", got)
	}
	if got := ConfirmationDelay(Proposal{Confidence: 0.95}); got != 4*time.Hour {
		t.Errorf("high-confidence = %v, want 4h", got)
	}
	if got := ConfirmationDelay(Proposal{Confidence: 0.5, Explicit: true}); got != time.Hour {
		t.Errorf("explicit = %v, want 1h", got)
	}
	// Boundary: exactly 0.9 is NOT "high confidence" (> 0.9 required).
	if got := ConfirmationDelay(Proposal{Confidence: 0.9}); got != 24*time.Hour {
		t.Errorf("confidence=0.9 = %v, want 24h", got)
	}
}

// ---------------------------------------------------------------------------
// Episode auto-detection (plan §2.3: fail → reads → fix → pass → commit)
// ---------------------------------------------------------------------------

func toolCmd(command string, exit int, output string) ToolEvent {
	return ToolEvent{
		Type: ToolEventCommandExecuted,
		Payload: map[string]any{
			"command": command, "args": []string{"./..."},
			"exit_code": exit, "output": output,
		},
		CreatedAt: time.Now().UTC(),
	}
}

func fullEpisodeArc() []ToolEvent {
	return []ToolEvent{
		toolCmd("go test", 1, "FAIL: TestPool exhausted connections"),
		{Type: ToolEventFileRead, Payload: map[string]any{"path": "internal/store/db.go"}},
		{Type: ToolEventFileRead, Payload: map[string]any{"path": "internal/server/ws.go"}},
		{Type: ToolEventFileModified, Payload: map[string]any{"path": "internal/store/db.go", "diff": "+ lifetime 30m"}},
		toolCmd("go test", 0, "ok all passed"),
		{Type: ToolEventGitCommitted, Payload: map[string]any{"commit": "abc123", "message": "fix pool lifetime"}},
	}
}

func TestDetectEpisodePatternFullArc(t *testing.T) {
	ep := DetectEpisodePattern(fullEpisodeArc())
	if ep == nil {
		t.Fatal("expected episode, got nil")
	}
	if ep.Type != "bug_fix" {
		t.Errorf("type = %q, want bug_fix", ep.Type)
	}
	if ep.Status != "RESOLVED" {
		t.Errorf("status = %q, want RESOLVED (commit present)", ep.Status)
	}
	if len(ep.FilesInvolved) != 1 || ep.FilesInvolved[0] != "internal/store/db.go" {
		t.Errorf("files = %v, want [internal/store/db.go]", ep.FilesInvolved)
	}
	if len(ep.ErrorPatterns) == 0 {
		t.Error("expected error pattern captured from trigger")
	}
	if ep.Trigger == "" || ep.Verification == "" || ep.Resolution == "" {
		t.Error("expected trigger/verification/resolution to be filled")
	}
}

func TestDetectEpisodePatternOpenWithoutCommit(t *testing.T) {
	arc := fullEpisodeArc()[:5] // drop the commit
	ep := DetectEpisodePattern(arc)
	if ep == nil {
		t.Fatal("expected episode, got nil")
	}
	if ep.Status != "OPEN" {
		t.Errorf("status = %q, want OPEN (no commit yet)", ep.Status)
	}
}

func TestDetectEpisodePatternRejects(t *testing.T) {
	// Never fixed: no FILE_MODIFIED, no green re-run.
	noFix := []ToolEvent{
		toolCmd("go test", 1, "FAIL boom"),
		{Type: ToolEventFileRead, Payload: map[string]any{"path": "a.go"}},
	}
	if ep := DetectEpisodePattern(noFix); ep != nil {
		t.Errorf("expected nil (no fix), got %+v", ep)
	}
	// Fixed but never verified green.
	noVerify := []ToolEvent{
		toolCmd("go test", 1, "FAIL boom"),
		{Type: ToolEventFileModified, Payload: map[string]any{"path": "a.go"}},
		toolCmd("go test", 1, "FAIL still broken"),
	}
	if ep := DetectEpisodePattern(noVerify); ep != nil {
		t.Errorf("expected nil (no green re-run), got %+v", ep)
	}
	// Different command re-run green does not count as verification.
	wrongCmd := []ToolEvent{
		toolCmd("go test", 1, "FAIL boom"),
		{Type: ToolEventFileModified, Payload: map[string]any{"path": "a.go"}},
		{
			Type: ToolEventCommandExecuted,
			Payload: map[string]any{
				"command": "go vet", "args": []string{"./..."},
				"exit_code": 0, "output": "ok",
			},
		},
	}
	if ep := DetectEpisodePattern(wrongCmd); ep != nil {
		t.Errorf("expected nil (different command), got %+v", ep)
	}
	// All-green history: no trigger at all.
	if ep := DetectEpisodePattern([]ToolEvent{toolCmd("go test", 0, "ok")}); ep != nil {
		t.Errorf("expected nil (no failure), got %+v", ep)
	}
}

// ---------------------------------------------------------------------------
// Processor wiring: designated gate, batching, budgets, flush
// ---------------------------------------------------------------------------

func turnEvent(content string) Event {
	return Event{
		Type:      EventConversationTurn,
		Payload:   map[string]any{"speaker": "user", "content": content},
		CreatedAt: time.Now().UTC(),
	}
}

func TestProcessorNonDesignatedIsNoop(t *testing.T) {
	p := NewProcessor(NewInMemoryStore(nil), 0, 0, false, nil)
	got, err := p.ProcessEvents(context.Background(), "proj",
		[]Event{turnEvent("I prefer verbose error messages with full stack traces everywhere")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("non-designated daemon proposed %d memories, want 0", len(got))
	}
}

// Designation gating under concurrency (issue #139): designated and
// non-designated processors sharing event shapes must not race, and the
// gate must hold on every call — run with -race.
func TestProcessorDesignationConcurrent(t *testing.T) {
	on := NewProcessor(NewInMemoryStore(nil), 0, 0, true, nil)
	off := NewProcessor(NewInMemoryStore(nil), 0, 0, false, nil)
	ev := []Event{turnEvent("The team decided on connect timeouts with exponential backoff retries")}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := on.ProcessEvents(context.Background(), "proj", ev); err != nil {
				t.Errorf("designated: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			got, err := off.ProcessEvents(context.Background(), "proj", ev)
			if err != nil {
				t.Errorf("non-designated: %v", err)
				return
			}
			if len(got) != 0 {
				t.Errorf("non-designated proposed %d, want 0", len(got))
			}
		}()
	}
	wg.Wait()
}

func TestProcessorExtractClassifyConfirm(t *testing.T) {
	p := NewProcessor(NewInMemoryStore(nil), 0, 0, true, nil)
	got, err := p.ProcessEvents(context.Background(), "proj", []Event{
		turnEvent("I prefer verbose error messages with full stack context for debugging sessions"),
		turnEvent("The sky is blue"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal (short sentence skipped), got %d", len(got))
	}
	pr := got[0]
	if pr.Level != LevelPersonal {
		t.Errorf("level = %q, want personal", pr.Level)
	}
	if pr.Scope != ScopePreference {
		t.Errorf("scope = %q, want preference", pr.Scope)
	}
	if !pr.Explicit || pr.ConfirmAfter != time.Hour {
		t.Errorf("explicit statement should confirm in 1h, got explicit=%v after=%v", pr.Explicit, pr.ConfirmAfter)
	}
	if pr.ProposedAt.IsZero() || pr.Key == "" {
		t.Error("expected stamped ProposedAt and derived Key")
	}
}

func TestProcessorDedupsAgainstStore(t *testing.T) {
	seed := []MemoryRecord{{
		Key: "team/cache", Content: "The team decided on Redis over Memcached due to pub/sub needs",
		Level: LevelProject, Scope: ScopeDecision, Confidence: 1,
	}}
	p := NewProcessor(NewInMemoryStore(seed), 0, 0, true, nil)
	got, err := p.ProcessEvents(context.Background(), "proj", []Event{
		turnEvent("The team decided on Redis over Memcached due to pub/sub needs exactly"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("duplicate of seeded memory proposed: %+v", got)
	}
}

func TestProcessorCaps(t *testing.T) {
	p := NewProcessor(NewInMemoryStore(nil), 100, 10, true, nil) // tiny budget: first ~70-char proposal fits, two do not
	events := []Event{
		turnEvent("First long decision statement about caching with Redis for sessions"),
		turnEvent("Second long decision statement about queues with NATS for messaging"),
	}
	got, err := p.ProcessEvents(context.Background(), "proj", events)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("budget should cap to 1 proposal, got %d", len(got))
	}

	p2 := NewProcessor(NewInMemoryStore(nil), 0, 1, true, nil) // batch size 1
	var many []Event
	for i := 0; i < 5; i++ {
		many = append(many, turnEvent("Independent statement number about testing approach and fixtures"))
	}
	got2, err := p2.ProcessEvents(context.Background(), "proj", many)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 {
		t.Errorf("batch size should cap to 1 proposal, got %d", len(got2))
	}
}

func TestShouldFlush(t *testing.T) {
	now := time.Now().UTC()
	complete := Event{Type: EventSessionComplete}
	if !ShouldFlush(complete, time.Time{}, now) {
		t.Error("SESSION_TRANSCRIPT_COMPLETE must flush immediately")
	}
	turn := Event{Type: EventConversationTurn}
	if !ShouldFlush(turn, now.Add(-6*time.Minute), now) {
		t.Error("6min idle must flush")
	}
	if ShouldFlush(turn, now.Add(-4*time.Minute), now) {
		t.Error("4min idle must not flush yet")
	}
	if ShouldFlush(turn, time.Time{}, now) {
		t.Error("zero lastActive must not flush on plain turns")
	}
}

func TestHeuristicSkipsSQLiteHintAsContent(t *testing.T) {
	// sqlite/vscdb completion detail is a driver hint, not a memory.
	ev := Event{
		Type: EventSessionComplete,
		Payload: map[string]any{
			"turns":  []any{},
			"detail": "sqlite/vscdb source: rows not parsed (no driver); deep extraction must read the store out-of-band",
		},
	}
	var hp HeuristicProvider
	got, err := hp.Extract(context.Background(), "proj", []Event{ev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("sqlite hint extracted as memory: %+v", got)
	}
}

func TestBuildExtractionPromptMentionsHeuristics(t *testing.T) {
	prompt := BuildExtractionPrompt("central-memory",
		[]MemoryRecord{{Level: LevelProject, Scope: ScopeDecision, Content: "we use pytest"}},
		[]Event{turnEvent("hello world discussion about caching")})
	for _, want := range []string{"I prefer", "for now", "do not duplicate", "SESSION level if unsure"} {
		if !contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
