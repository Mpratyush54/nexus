package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is the ProcessorStore fake: in-memory CONFIRMED set + recorded
// writes. No network, no database.
type fakeStore struct {
	mu        sync.Mutex
	confirmed []ConfirmedMemory
	proposed  []ProposedMemory
	episodes  []EpisodeDraft
	epRefs    [][]EpisodeEventRef
	listErr   error
	saveErr   error
}

func (f *fakeStore) ListConfirmed(_ context.Context) ([]ConfirmedMemory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]ConfirmedMemory(nil), f.confirmed...), nil
}

func (f *fakeStore) SaveProposed(_ context.Context, m ProposedMemory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.proposed = append(f.proposed, m)
	return nil
}

func (f *fakeStore) SaveEpisode(_ context.Context, e EpisodeDraft, refs []EpisodeEventRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.episodes = append(f.episodes, e)
	f.epRefs = append(f.epRefs, refs)
	return nil
}

func turnEvent(session, speaker, content string, at time.Time) ProcessorEvent {
	return ProcessorEvent{
		Type:      EventConversationTurn,
		SessionID: session,
		At:        at,
		Payload:   map[string]any{"session_id": session, "speaker": speaker, "content": content},
	}
}

func TestProcessClassificationDefaults(t *testing.T) {
	ctx := context.Background()
	// Missing level/scope, bogus labels, out-of-range confidence.
	stub := &StubLLMClient{Response: `[
	  {"key": "a/x", "content": "The team decided to use Redis for caching pubsub.", "level": "", "scope": ""},
	  {"key": "b/y", "content": "Alice said she always wants verbose test output enabled.", "level": "nonsense", "scope": "nonsense", "confidence": 0.5},
	  {"key": "c/z", "content": "Do not touch the payments directory during this task please.", "confidence": 9.5}
	]`}
	store := &fakeStore{}
	p := NewProcessor("proj", true, stub, store)
	p.Ingest(turnEvent("s1", "user", "we decided on redis", time.Now()))
	res, err := p.FlushSession(ctx, "s1")
	if err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	if len(res.Proposed) != 3 {
		t.Fatalf("want 3 proposed, got %d", len(res.Proposed))
	}
	// Empty labels → SESSION + fact.
	if got := res.Proposed[0].Level; got != LevelSession {
		t.Errorf("empty level: want session, got %q", got)
	}
	if got := res.Proposed[0].Scope; got != "fact" {
		t.Errorf("empty scope: want fact, got %q", got)
	}
	// Bogus labels → SESSION + fact.
	if got := res.Proposed[1].Level; got != LevelSession {
		t.Errorf("bogus level: want session, got %q", got)
	}
	if got := res.Proposed[1].Scope; got != "fact" {
		t.Errorf("bogus scope: want fact, got %q", got)
	}
	// Confidence clamps to [0,1].
	if got := res.Proposed[2].Confidence; got != 1 {
		t.Errorf("confidence clamp: want 1, got %v", got)
	}
	// Prompt carries the §2.6 contract markers.
	if len(stub.Prompts) != 1 {
		t.Fatalf("want 1 LLM call, got %d", len(stub.Prompts))
	}
	for _, want := range []string{"Default to SESSION level", "do not duplicate", "explicit_user_statement"} {
		if !strings.Contains(stub.Prompts[0], want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// Valid labels survive untouched.
	stub2 := &StubLLMClient{Response: `[{"key": "k", "content": "All APIs in this org must use JWT authentication now.", "level": "organization", "scope": "constraint", "confidence": 0.8}]`}
	p2 := NewProcessor("proj", true, stub2, &fakeStore{})
	p2.Ingest(turnEvent("s", "user", "x", time.Now()))
	res2, err := p2.FlushSession(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Proposed[0].Level != LevelOrganization || res2.Proposed[0].Scope != "constraint" {
		t.Errorf("valid labels rewritten: %+v", res2.Proposed[0].ExtractedMemory)
	}
}

func TestProcessDedupThreshold(t *testing.T) {
	ctx := context.Background()
	// Near-identical to CONFIRMED (token-cosine 1.0 > 0.9) must skip;
	// the novel fact must persist.
	stub := &StubLLMClient{Response: `[
	  {"key": "testing/framework", "content": "The team uses pytest with fixture-based setup.", "level": "project", "scope": "decision", "confidence": 0.8},
	  {"key": "cache/choice", "content": "Completely unrelated note about office lunch menus today.", "level": "session", "scope": "fact", "confidence": 0.6}
	]`}
	store := &fakeStore{confirmed: []ConfirmedMemory{
		{Key: "testing/framework", Content: "The team uses pytest with fixture-based setup."},
	}}
	p := NewProcessor("proj", true, stub, store)
	p.Ingest(turnEvent("s1", "assistant", "pytest it is", time.Now()))
	res, err := p.FlushSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedDuplicates != 1 {
		t.Errorf("want 1 dedup skip, got %d", res.SkippedDuplicates)
	}
	if len(res.Proposed) != 1 || res.Proposed[0].Key != "cache/choice" {
		t.Errorf("want only cache/choice proposed, got %+v", res.Proposed)
	}

	// Vector path: orthogonal embeddings keep, identical embeddings skip.
	if IsNearDuplicate("anything", []float32{1, 0}, []ConfirmedMemory{{Content: "other", Embedding: []float32{0, 1}}}) {
		t.Error("orthogonal vectors must not dedup")
	}
	if !IsNearDuplicate("anything", []float32{1, 0}, []ConfirmedMemory{{Content: "other", Embedding: []float32{1, 0}}}) {
		t.Error("identical vectors must dedup")
	}
	// Below-threshold pair ([1,0] vs [1,1] ≈ 0.707) must not dedup.
	if IsNearDuplicate("anything", []float32{1, 0}, []ConfirmedMemory{{Content: "other", Embedding: []float32{1, 1}}}) {
		t.Error("0.707-similar vectors must not dedup (threshold 0.9)")
	}
}

func TestProcessTimerRules(t *testing.T) {
	cases := []struct {
		name string
		mem  ExtractedMemory
		want time.Duration
	}{
		{"default 24h", ExtractedMemory{Confidence: 0.5}, ConfirmAfterDefault},
		{"zero confidence 24h", ExtractedMemory{}, ConfirmAfterDefault},
		{"high confidence 4h", ExtractedMemory{Confidence: 0.95}, ConfirmAfterHighConfidence},
		{"boundary 0.9 stays 24h", ExtractedMemory{Confidence: 0.9}, ConfirmAfterDefault},
		{"explicit user 1h", ExtractedMemory{Confidence: 0.4, Explicit: true}, ConfirmAfterExplicitUser},
		{"explicit beats high-conf", ExtractedMemory{Confidence: 0.99, Explicit: true}, ConfirmAfterExplicitUser},
	}
	for _, c := range cases {
		if got := ConfirmAfterFor(c.mem); got != c.want {
			t.Errorf("%s: want %v, got %v", c.name, c.want, got)
		}
	}

	// End-to-end: the timer rides along on the saved proposal.
	ctx := context.Background()
	stub := &StubLLMClient{Response: `[{"key": "style/errors", "content": "Alice prefers verbose error messages with full stack.", "level": "personal", "scope": "preference", "confidence": 0.97, "explicit_user_statement": true}]`}
	store := &fakeStore{}
	p := NewProcessor("proj", true, stub, store)
	p.Ingest(turnEvent("s", "user", "I prefer verbose errors", time.Now()))
	res, err := p.FlushSession(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Proposed) != 1 {
		t.Fatalf("want 1 proposed, got %d", len(res.Proposed))
	}
	if res.Proposed[0].ConfirmAfter != ConfirmAfterExplicitUser {
		t.Errorf("want 1h explicit timer, got %v", res.Proposed[0].ConfirmAfter)
	}
}

func TestProcessBatching(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	clock := &manualClock{at: now}
	stub := &StubLLMClient{Response: `[]`}
	store := &fakeStore{}
	p := NewProcessor("proj", true, stub, store,
		WithProcessorClock(clock.fn()),
		WithProcessorIdleAfter(5*time.Minute))

	p.Ingest(turnEvent("s1", "user", "hello", now))
	p.Ingest(turnEvent("s1", "assistant", "hi there", now))

	// Fresh activity: nothing due.
	if n, err := p.CheckIdle(ctx); err != nil || n != 0 {
		t.Fatalf("fresh: want 0 flushed, got %d, err %v", n, err)
	}
	if got := len(p.PendingSessions()); got != 1 {
		t.Fatalf("want 1 pending session, got %d", got)
	}
	if stub.Calls != 0 {
		t.Fatalf("LLM must not be called before idle/complete, got %d calls", stub.Calls)
	}

	// 5-min idle triggers the batch.
	clock.advance(5 * time.Minute)
	if n, err := p.CheckIdle(ctx); err != nil || n != 1 {
		t.Fatalf("idle: want 1 flushed, got %d, err %v", n, err)
	}
	if stub.Calls != 1 {
		t.Errorf("want 1 LLM call after idle flush, got %d", stub.Calls)
	}

	// SESSION_TRANSCRIPT_COMPLETE flushes immediately, no waiting.
	p.Ingest(turnEvent("s2", "user", "second session", clock.now()))
	p.Ingest(ProcessorEvent{Type: EventSessionTranscriptComplete,
		Payload: map[string]any{"session_id": "s2"}})
	if n, err := p.CheckIdle(ctx); err != nil || n != 1 {
		t.Fatalf("complete: want 1 flushed, got %d, err %v", n, err)
	}
	if stub.Calls != 2 {
		t.Errorf("want 2 LLM calls total, got %d", stub.Calls)
	}
	if got := len(p.PendingSessions()); got != 0 {
		t.Errorf("want empty buffer after flush, got %v", p.PendingSessions())
	}
}

func TestProcessEpisodeAutoDetect(t *testing.T) {
	ctx := context.Background()
	// failure → reads → patch → verify → commit (§2.3).
	events := []ProcessorEvent{
		{Type: EventCommandExecuted, SessionID: "s", Payload: map[string]any{
			"cmdline": "go test ./...", "exit_code": 1, "stderr": "ConnectionTimeout in ws.go:142"}},
		{Type: EventFileRead, SessionID: "s", Payload: map[string]any{"path": "internal/store/db.go"}},
		{Type: EventFileRead, SessionID: "s", Payload: map[string]any{"path": "internal/server/ws.go"}},
		{Type: EventFileModified, SessionID: "s", Payload: map[string]any{"path": "internal/store/db.go"}},
		{Type: EventCommandExecuted, SessionID: "s", Payload: map[string]any{
			"cmdline": "go test ./...", "exit_code": 0}},
		{Type: EventGitCommitted, SessionID: "s", Payload: map[string]any{
			"hash": "abc123", "message": "fix pool lifetime"}},
	}
	draft, refs := DetectEpisode(events)
	if draft == nil {
		t.Fatal("want episode draft, got nil")
	}
	if draft.EpisodeType != "bug_fix" {
		t.Errorf("want bug_fix, got %q", draft.EpisodeType)
	}
	if len(draft.FilesInvolved) == 0 || len(draft.ErrorPatterns) == 0 {
		t.Errorf("want files + error patterns, got %+v", draft)
	}
	if draft.Verification == "" || draft.Trigger == "" {
		t.Errorf("want trigger + verification text, got %+v", draft)
	}
	roles := map[string]bool{}
	for _, r := range refs {
		roles[r.Role] = true
	}
	for _, want := range []string{"trigger", "investigation", "fix", "verification"} {
		if !roles[want] {
			t.Errorf("missing episode role %q in %v", want, refs)
		}
	}

	// No failure → no episode. Bare failure → no episode (noise, not an arc).
	if d, _ := DetectEpisode([]ProcessorEvent{
		{Type: EventFileRead, Payload: map[string]any{"path": "x"}},
	}); d != nil {
		t.Error("no trigger must yield no episode")
	}
	if d, _ := DetectEpisode([]ProcessorEvent{
		{Type: EventCommandExecuted, Payload: map[string]any{"cmdline": "go test ./...", "exit_code": 2}},
	}); d != nil {
		t.Error("bare failure with no follow-up must yield no episode")
	}

	// End-to-end: the draft is delegated to the store, not persisted inline.
	stub := &StubLLMClient{Response: `[]`}
	store := &fakeStore{}
	p := NewProcessor("proj", true, stub, store)
	for _, ev := range events {
		p.Ingest(ev)
	}
	res, err := p.FlushSession(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if res.Episode == nil {
		t.Fatal("want episode saved through flush, got nil")
	}
	if len(store.episodes) != 1 || store.episodes[0].EpisodeType != "bug_fix" {
		t.Errorf("store saw %d episodes, want 1 bug_fix", len(store.episodes))
	}
}

func TestProcessNotDesignated(t *testing.T) {
	ctx := context.Background()
	stub := &StubLLMClient{Response: `[{"key": "k", "content": "This fact is long enough to be valid content.", "level": "project"}]`}
	store := &fakeStore{}
	p := NewProcessor("proj", false, stub, store)
	if p.IsDesignated() {
		t.Error("want IsDesignated false")
	}
	p.Ingest(turnEvent("s", "user", "hello", time.Now()))
	if got := len(p.PendingSessions()); got != 0 {
		t.Errorf("non-designated must buffer nothing, got %d sessions", got)
	}
	if err := p.Start(ctx); err != nil {
		t.Errorf("non-designated Start must return nil immediately, got %v", err)
	}
	if res, err := p.FlushSession(ctx, "s"); err != nil || len(res.Proposed) != 0 {
		t.Errorf("non-designated flush: want zero result, got %+v, err %v", res, err)
	}
	if stub.Calls != 0 || len(store.proposed) != 0 {
		t.Error("non-designated must never touch LLM or store")
	}
}

func TestProcessFailureRetention(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("llm down")
	stub := &StubLLMClient{Err: boom}
	store := &fakeStore{}
	p := NewProcessor("proj", true, stub, store)
	p.Ingest(turnEvent("s", "user", "hello", time.Now()))
	if _, err := p.FlushSession(ctx, "s"); err == nil {
		t.Fatal("want LLM error propagated")
	}
	if got := len(p.PendingSessions()); got != 1 {
		t.Error("failed flush must retain the buffer for retry")
	}
}

// manualClock is an injectable Clock for idle tests.
type manualClock struct {
	mu sync.Mutex
	at time.Time
}

func (m *manualClock) now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at
}

func (m *manualClock) fn() Clock {
	return func() time.Time { return m.now() }
}

func (m *manualClock) advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at = m.at.Add(d)
}
