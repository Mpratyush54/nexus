package store

// Audit: AgentRegistry budgets vs 004 seed, task/watched/episode pure
// validators, MemStore UUID-format portability, and the Postgres-only CRUD
// gap (TaskStore/UserStore/WatchedFileStore have no MemStore equivalent, so
// their CRUD paths cannot run without a live database).

import (
	"context"
	"os"
	"testing"
)

func TestAuditAgentBudgetsMatchSeed(t *testing.T) {
	r := NewAgentRegistry()
	// Transcribed from migrations/004_agents.up.sql.
	want := map[string]struct {
		budget int
		typ    string
		file   string
		format string
	}{
		"claude":      {10000, AdapterPull, "", ""},
		"opencode":    {10000, AdapterPull, "", ""},
		"codex":       {10000, AdapterPull, "", ""},
		"antigravity": {10000, AdapterPull, "", ""},
		"copilot":     {8000, AdapterPush, ".github/copilot-instructions.md", "markdown"},
		"cursor":      {6000, AdapterPush, ".cursorrules", "text"},
		"windsurf":    {6000, AdapterPush, ".windsurfrules", "text"},
	}
	for name, w := range want {
		a, err := r.GetAgent(name)
		if err != nil {
			t.Fatalf("seed agent %q missing: %v", name, err)
		}
		if a.ContextBudget != w.budget {
			t.Errorf("%s budget = %d, want %d (004 seed)", name, a.ContextBudget, w.budget)
		}
		if a.AdapterType != w.typ {
			t.Errorf("%s adapter = %q, want %q", name, a.AdapterType, w.typ)
		}
		if a.OutputFile != w.file || a.OutputFormat != w.format {
			t.Errorf("%s output = %q/%q, want %q/%q", name, a.OutputFile, a.OutputFormat, w.file, w.format)
		}
		if got := r.BudgetFor(name); got != w.budget {
			t.Errorf("BudgetFor(%q) = %d, want %d", name, got, w.budget)
		}
	}
	if got := len(r.ListAgents()); got != 7 {
		t.Errorf("registry holds %d agents, want 7 seeded", got)
	}
}

func TestAuditAgentBudgetFallback(t *testing.T) {
	r := NewAgentRegistry()
	for _, name := range []string{"unknown-agent", "", "   "} {
		if got := r.BudgetFor(name); got != DefaultBudgetFallback {
			t.Errorf("BudgetFor(%q) = %d, want fallback %d", name, got, DefaultBudgetFallback)
		}
	}
	if DefaultBudgetFallback != 4000 {
		t.Errorf("DefaultBudgetFallback = %d, want 4000 (agents.context_budget DEFAULT)", DefaultBudgetFallback)
	}
}

func TestAuditAgentLookupCaseInsensitive(t *testing.T) {
	r := NewAgentRegistry()
	for _, name := range []string{"CLAUDE", " Claude ", "cOpIlOt"} {
		a, err := r.GetAgent(name)
		if err != nil {
			t.Errorf("GetAgent(%q) failed: %v", name, err)
			continue
		}
		if got := r.BudgetFor(name); got != a.ContextBudget {
			t.Errorf("BudgetFor(%q) = %d, want %d", name, got, a.ContextBudget)
		}
	}
	if _, err := r.GetAgent("nope"); err != ErrNotFound {
		t.Errorf("unknown agent: got %v, want ErrNotFound", err)
	}
}

func TestAuditAgentUpsertFallbackAndNilSafe(t *testing.T) {
	r := NewAgentRegistry()
	r.UpsertAgent(nil)      // must not panic
	r.UpsertAgent(&Agent{}) // empty name: ignored
	r.UpsertAgent(&Agent{Name: "custom", AdapterType: AdapterPull})
	if got := r.BudgetFor("custom"); got != DefaultBudgetFallback {
		t.Errorf("zero-budget upsert: got %d, want fallback %d", got, DefaultBudgetFallback)
	}
	r.UpsertAgent(&Agent{Name: "custom", AdapterType: AdapterPull, ContextBudget: 1234})
	if got := r.BudgetFor("CUSTOM"); got != 1234 {
		t.Errorf("upsert overwrite: got %d, want 1234", got)
	}
}

func TestAuditAgentProjectEnablement(t *testing.T) {
	r := NewAgentRegistry()
	if !r.IsEnabled("proj-new", "claude") {
		t.Error("default enablement should be true (no explicit row)")
	}
	if err := r.SetProjectAgentEnabled("p1", "claude", false); err != nil {
		t.Fatal(err)
	}
	if r.IsEnabled("p1", "claude") {
		t.Error("claude should be disabled for p1")
	}
	if r.IsEnabled("p1", "copilot") {
		// copilot untouched for p1: still default-enabled.
	} else {
		t.Error("untouched agent should stay enabled")
	}
	if err := r.SetProjectAgentEnabled("p1", "ghost", true); err != ErrNotFound {
		t.Errorf("unknown agent enable: got %v, want ErrNotFound", err)
	}
}

func TestAuditAgentPushPullClassification(t *testing.T) {
	r := NewAgentRegistry()
	for _, name := range []string{"claude", "opencode", "codex", "antigravity"} {
		a, _ := r.GetAgent(name)
		if !a.IsPull() || a.IsPush() {
			t.Errorf("%s should classify pull", name)
		}
		if got := r.OutputFileFor(name); got != "" {
			t.Errorf("%s OutputFile = %q, want empty for pull agents", name, got)
		}
	}
	for _, name := range []string{"copilot", "cursor", "windsurf"} {
		a, _ := r.GetAgent(name)
		if !a.IsPush() || a.IsPull() {
			t.Errorf("%s should classify push", name)
		}
		if got := r.OutputFileFor(name); got == "" {
			t.Errorf("%s needs an output file", name)
		}
	}
	if got := r.OutputFileFor("ghost"); got != "" {
		t.Errorf("unknown agent OutputFile = %q, want empty", got)
	}
	var nilAgent *Agent
	if nilAgent.IsPush() || nilAgent.IsPull() {
		t.Error("nil agent must classify neither push nor pull")
	}
}

func TestAuditTaskStatusMachineEdges(t *testing.T) {
	if got, ok := NormalizeTaskStatus(""); !ok || got != TaskOpen {
		t.Errorf("empty status normalizes to %q,%v; want OPEN,true", got, ok)
	}
	if got, ok := NormalizeTaskStatus("done"); !ok || got != TaskDone {
		t.Errorf("lowercase normalizes to %q,%v", got, ok)
	}
	if _, ok := NormalizeTaskStatus("bogus"); ok {
		t.Error("unknown status must not normalize")
	}
	legal := [][2]string{{TaskOpen, TaskInProgress}, {TaskOpen, TaskBlocked}, {TaskOpen, TaskDone},
		{TaskInProgress, TaskDone}, {TaskBlocked, TaskOpen}, {TaskDone, TaskOpen}, {TaskDone, TaskDone}}
	for _, tc := range legal {
		if !CanTransitionTaskStatus(tc[0], tc[1]) {
			t.Errorf("legal transition %s->%s denied", tc[0], tc[1])
		}
	}
	illegal := [][2]string{{TaskDone, TaskInProgress}, {TaskDone, TaskBlocked}, {TaskBlocked, TaskDone},
		{"BOGUS", TaskOpen}, {TaskOpen, "BOGUS"}}
	for _, tc := range illegal {
		if CanTransitionTaskStatus(tc[0], tc[1]) {
			t.Errorf("illegal transition %s->%s allowed", tc[0], tc[1])
		}
	}
}

func TestAuditWatchedFileTypeAndStale(t *testing.T) {
	if got, ok := NormalizeWatchedFileType("CLAUDE_MD"); !ok || got != WatchedClaudeMD {
		t.Errorf("got %q,%v", got, ok)
	}
	if _, ok := NormalizeWatchedFileType("vimrc"); ok {
		t.Error("unknown file type must not normalize")
	}
	stored := []WatchedFile{
		{Path: "a.md", LastHash: "h1"},
		{Path: "b.md", LastHash: "same"},
	}
	current := map[string]string{"a.md": "h2", "b.md": "same", "c.md": "new"}
	stale := StaleWatchedFiles(stored, current)
	if len(stale) != 2 || stale[0].Path != "a.md" || stale[1].Path != "c.md" {
		t.Errorf("StaleWatchedFiles = %+v, want [a.md c.md] sorted", stale)
	}
}

func TestAuditEpisodeRoleAndBranchAccess(t *testing.T) {
	for _, role := range []string{"trigger", "investigation", "attempt", "fix", "verification", "context"} {
		if !ValidEpisodeRole(role) {
			t.Errorf("role %q should be valid", role)
		}
	}
	if ValidEpisodeRole("bogus") {
		t.Error("bogus role must be invalid")
	}
	priv := &MemoryBranch{ID: "b1", Visibility: BranchVisibilityPrivate, OwnerID: "u1"}
	if err := CheckBranchAccess(priv, "u2"); err != ErrForbidden {
		t.Errorf("private branch stranger: got %v, want ErrForbidden", err)
	}
	if err := CheckBranchAccess(priv, "u1"); err != nil {
		t.Errorf("owner should pass: %v", err)
	}
	if err := CheckBranchAccess(priv, ""); err != nil {
		t.Errorf("system (empty requester) should bypass: %v", err)
	}
	shared := &MemoryBranch{ID: "b2", Visibility: BranchVisibilityShared}
	if err := CheckBranchAccess(shared, "anyone"); err != nil {
		t.Errorf("shared branch should be open: %v", err)
	}
	if err := CheckBranchAccess(nil, "u1"); err != ErrNotFound {
		t.Errorf("nil branch: got %v, want ErrNotFound", err)
	}
}

// MemStore performs no UUID validation anywhere: arbitrary string IDs flow
// through every API. That is convenient for tests but masks a portability
// gap — Postgres casts IDs with $1::uuid, so code exercised on MemStore
// with non-UUID IDs fails against Postgres. These passing tests pin the
// lenient behavior so the divergence is explicit.
func TestAuditMemStoreAcceptsNonUUIDIDs(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	junk := "not-a-uuid"
	if err := s.CreateMemoryItem(ctx, &MemoryItem{ProjectID: junk, Key: "k", Content: "content with enough length here"}); err != nil {
		t.Fatalf("MemStore rejected non-UUID project id: %v", err)
	}
	if err := s.CreateEpisode(ctx, &Episode{ProjectID: junk, Title: "t", EpisodeType: "bug_fix"}); err != nil {
		t.Fatalf("MemStore rejected non-UUID episode project id: %v", err)
	}
	if err := s.AppendEvent(ctx, &Event{ProjectID: junk, EventType: "X"}); err != nil {
		t.Fatalf("MemStore rejected non-UUID event project id: %v", err)
	}
	if res, err := s.ListEvents(ctx, junk, 0, 0); err != nil || len(res) != 1 {
		t.Fatalf("non-UUID event round-trip broken: %+v %v", res, err)
	}
	t.Log("PORTABILITY GAP: all of the above fail on Postgres ($N::uuid casts reject non-UUID strings)")
}

// TaskStore/UserStore/WatchedFileStore CRUD runs only over DBTX (live
// Postgres): MemStore implements none of those surfaces, so no stdlib test
// can exercise task/user/watched-file CRUD without a database.
func TestAuditPostgresCRUDRequiresLiveDB(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_DSN") == "" {
		t.Skip("no TEST_POSTGRES_DSN: TaskStore/UserStore/WatchedFileStore CRUD need live Postgres; MemStore has no equivalent (coverage gap by construction)")
	}
	t.Skip("audit scope is MemStore-only; Postgres CRUD paths are untested here by design")
}
