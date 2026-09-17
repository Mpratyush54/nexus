// Unit tests for the agent registry (issue #15, plan §4.1).
//
// DB-free by design except through a scripted DBTX fake: seed values,
// budget fallback, push-target mapping, and project config overrides run
// with no database at all. Store-method SQL paths (GetByName, List,
// EnabledForProject, SetProjectAgent, BudgetForProject,
// PushTargetsForProject) run against agentFakeDB, which records statements
// and replays canned rows.
package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	ctxbuilder "central-memory/internal/context"
	"central-memory/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Fake DBTX
// ---------------------------------------------------------------------------

// agentFakeRow is a pgx.Row replaying one canned Scan.
type agentFakeRow struct {
	values []any
	err    error
}

func (r agentFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("agentFakeRow: arity mismatch")
	}
	for i := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = r.values[i].(string)
		case *bool:
			*ptr = r.values[i].(bool)
		case *int:
			*ptr = r.values[i].(int)
		case *time.Time:
			*ptr = r.values[i].(time.Time)
		default:
			return errors.New("agentFakeRow: unsupported dest")
		}
	}
	return nil
}

// agentFakeRows is a store.Rows replaying canned rows.
type agentFakeRows struct {
	rows [][]any
	pos  int
}

func (f *agentFakeRows) Next() bool { f.pos++; return f.pos <= len(f.rows) }
func (f *agentFakeRows) Err() error { return nil }
func (f *agentFakeRows) Close()     {}
func (f *agentFakeRows) Scan(dest ...any) error {
	return agentFakeRow{values: f.rows[f.pos-1]}.Scan(dest...)
}

// agentFakeDB scripts QueryRow as a queue and Query as one result set,
// recording every statement for SQL assertions.
type agentFakeDB struct {
	rowQueue []agentFakeRow
	rows     *agentFakeRows
	queryErr error
	queries  []string
}

func (f *agentFakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *agentFakeDB) Query(_ context.Context, sql string, _ ...any) (store.Rows, error) {
	f.queries = append(f.queries, sql)
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func (f *agentFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	if len(f.rowQueue) == 0 {
		return agentFakeRow{err: pgx.ErrNoRows}
	}
	r := f.rowQueue[0]
	f.rowQueue = f.rowQueue[1:]
	return r
}

var agentTestTime = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

func agentRow(id, name, adapter, caps string, budget int, file, format string) agentFakeRow {
	return agentFakeRow{values: []any{
		id, name, adapter, caps, budget, file, format, agentTestTime,
	}}
}

func projectAgentRow(project, agentID, name, adapter string, budget int, file, format string, enabled bool, config string) agentFakeRow {
	return agentFakeRow{values: []any{
		project, agentID, name, adapter, budget, file, format, enabled, config,
	}}
}

// ---------------------------------------------------------------------------
// Seed values (plan §4.1 INSERT, issue #15 seed contract)
// ---------------------------------------------------------------------------

func TestAgentSeedValues(t *testing.T) {
	if len(store.SeedAgents) != 7 {
		t.Fatalf("SeedAgents has %d entries, want 7", len(store.SeedAgents))
	}
	want := map[string]struct {
		adapter string
		budget  int
		file    string
		format  string
	}{
		"claude":      {store.AdapterPull, 10000, "", ""},
		"opencode":    {store.AdapterPull, 10000, "", ""},
		"codex":       {store.AdapterPull, 10000, "", ""},
		"antigravity": {store.AdapterPull, 10000, "", ""},
		"copilot":     {store.AdapterPush, 8000, ".github/copilot-instructions.md", "markdown"},
		"cursor":      {store.AdapterPush, 6000, ".cursorrules", "text"},
		"windsurf":    {store.AdapterPush, 6000, ".windsurfrules", "text"},
	}
	seen := make(map[string]bool, len(store.SeedAgents))
	for _, a := range store.SeedAgents {
		w, ok := want[a.Name]
		if !ok {
			t.Errorf("unexpected seed agent %q", a.Name)
			continue
		}
		seen[a.Name] = true
		if a.AdapterType != w.adapter {
			t.Errorf("%s adapter = %q, want %q", a.Name, a.AdapterType, w.adapter)
		}
		if a.ContextBudget != w.budget {
			t.Errorf("%s budget = %d, want %d", a.Name, a.ContextBudget, w.budget)
		}
		if a.OutputFile != w.file {
			t.Errorf("%s output_file = %q, want %q", a.Name, a.OutputFile, w.file)
		}
		if a.OutputFormat != w.format {
			t.Errorf("%s output_format = %q, want %q", a.Name, a.OutputFormat, w.format)
		}
		if !store.IsValidAdapterType(a.AdapterType) {
			t.Errorf("%s carries invalid adapter type %q", a.Name, a.AdapterType)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("seed agent %q missing", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Budget fallback (Context Builder integration, builder.go untouched)
// ---------------------------------------------------------------------------

func TestAgentBudgetFallback(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"claude", 10000},
		{"opencode", 10000},
		{"codex", 10000},
		{"antigravity", 10000},
		{"copilot", 8000},
		{"cursor", 6000},
		{"windsurf", 6000},
		// Normalization: case + whitespace still resolve.
		{"Claude", 10000},
		{"  COPILOT  ", 8000},
		// Unknown agents fall back to the Context Builder default.
		{"gemini", ctxbuilder.DefaultBudgetChars},
		{"", ctxbuilder.DefaultBudgetChars},
		{"   ", ctxbuilder.DefaultBudgetChars},
	}
	for _, c := range cases {
		if got := store.BudgetFor(c.name); got != c.want {
			t.Errorf("BudgetFor(%q) = %d, want %d", c.name, got, c.want)
		}
	}
	if ctxbuilder.DefaultBudgetChars != 4000 {
		t.Errorf("DefaultBudgetChars = %d, want 4000 (plan §1.6)", ctxbuilder.DefaultBudgetChars)
	}
}

// ---------------------------------------------------------------------------
// Push-target mapping (materializer fan-out)
// ---------------------------------------------------------------------------

func TestAgentPushMapping(t *testing.T) {
	got := store.PushTargets(store.SeedAgents)
	want := map[string]string{
		"copilot":  ".github/copilot-instructions.md",
		"cursor":   ".cursorrules",
		"windsurf": ".windsurfrules",
	}
	if len(got) != len(want) {
		t.Fatalf("PushTargets(seeds) has %d entries, want %d: %v", len(got), len(want), got)
	}
	for name, file := range want {
		if got[name] != file {
			t.Errorf("PushTargets[%q] = %q, want %q", name, got[name], file)
		}
	}
	for _, pull := range []string{"claude", "opencode", "codex", "antigravity"} {
		if _, ok := got[pull]; ok {
			t.Errorf("pull agent %q must not appear in push targets", pull)
		}
	}

	// Push agent without an output file is excluded; empty input is empty.
	partial := store.PushTargets([]store.Agent{
		{Name: "copilot", AdapterType: store.AdapterPush, OutputFile: ".github/copilot-instructions.md"},
		{Name: "future-push", AdapterType: store.AdapterPush},
		{Name: "claude", AdapterType: store.AdapterPull},
	})
	if len(partial) != 1 || partial["copilot"] == "" {
		t.Errorf("partial push mapping = %v, want only copilot", partial)
	}
	if empty := store.PushTargets(nil); empty == nil || len(empty) != 0 {
		t.Errorf("PushTargets(nil) = %v, want empty non-nil map", empty)
	}
}

// ---------------------------------------------------------------------------
// Project overrides (config JSON wins, then agent default, then fallback)
// ---------------------------------------------------------------------------

func TestAgentProjectOverrides(t *testing.T) {
	// ParseBudgetOverride: only a positive context_budget parses.
	parseCases := []struct {
		config string
		budget int
		ok     bool
	}{
		{`{"context_budget": 5000}`, 5000, true},
		{`{"context_budget":5000}`, 5000, true},
		{`{"other": 1, "context_budget": 2500}`, 2500, true},
		{`{"context_budget": 0}`, 0, false},
		{`{"context_budget": -5}`, 0, false},
		{`{"context_budget": "many"}`, 0, false},
		{`{"unrelated": true}`, 0, false},
		{`not json`, 0, false},
		{``, 0, false},
		{`{}`, 0, false},
		{`null`, 0, false},
	}
	for _, c := range parseCases {
		budget, ok := store.ParseBudgetOverride(c.config)
		if budget != c.budget || ok != c.ok {
			t.Errorf("ParseBudgetOverride(%q) = (%d, %v), want (%d, %v)",
				c.config, budget, ok, c.budget, c.ok)
		}
	}

	// EffectiveBudget funnel: override > base > builder default.
	if got := store.EffectiveBudget(10000, `{"context_budget": 5000}`); got != 5000 {
		t.Errorf("EffectiveBudget override = %d, want 5000", got)
	}
	if got := store.EffectiveBudget(8000, ""); got != 8000 {
		t.Errorf("EffectiveBudget base = %d, want 8000", got)
	}
	if got := store.EffectiveBudget(8000, `{"other": 1}`); got != 8000 {
		t.Errorf("EffectiveBudget unrelated config = %d, want 8000", got)
	}
	if got := store.EffectiveBudget(0, ""); got != ctxbuilder.DefaultBudgetChars {
		t.Errorf("EffectiveBudget zero base = %d, want default %d", got, ctxbuilder.DefaultBudgetChars)
	}

	// ProjectAgent.Budget threads the row's own fields through the funnel.
	row := store.ProjectAgent{AgentName: "cursor", AdapterType: store.AdapterPush, ContextBudget: 6000}
	if row.Budget() != 6000 {
		t.Errorf("ProjectAgent.Budget without config = %d, want 6000", row.Budget())
	}
	row.Config = `{"context_budget": 4500}`
	if row.Budget() != 4500 {
		t.Errorf("ProjectAgent.Budget with override = %d, want 4500", row.Budget())
	}
}

// ---------------------------------------------------------------------------
// Names, adapter types, push/pull predicates
// ---------------------------------------------------------------------------

func TestAgentNormalizeAndAdapterTypes(t *testing.T) {
	if got := store.NormalizeAgentName("  Claude "); got != "claude" {
		t.Errorf("NormalizeAgentName = %q, want %q", got, "claude")
	}
	for _, valid := range []string{"pull", "push", " PULL ", "Push"} {
		if !store.IsValidAdapterType(valid) {
			t.Errorf("IsValidAdapterType(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "poll", "live"} {
		if store.IsValidAdapterType(invalid) {
			t.Errorf("IsValidAdapterType(%q) = true, want false", invalid)
		}
	}
	pull := store.Agent{Name: "claude", AdapterType: store.AdapterPull}
	if !pull.IsPull() || pull.IsPush() {
		t.Error("pull agent predicates wrong")
	}
	push := store.Agent{Name: "copilot", AdapterType: store.AdapterPush}
	if !push.IsPush() || push.IsPull() {
		t.Error("push agent predicates wrong")
	}
}

// ---------------------------------------------------------------------------
// Store SQL paths via scripted fake
// ---------------------------------------------------------------------------

func TestAgentGetByName(t *testing.T) {
	ctx := context.Background()
	fake := &agentFakeDB{rowQueue: []agentFakeRow{
		agentRow("id-1", "claude", "pull", "", 10000, "", ""),
	}}
	a, err := store.NewAgentStore(fake).GetByName(ctx, "Claude")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if a.Name != "claude" || a.ContextBudget != 10000 || !a.IsPull() {
		t.Errorf("GetByName returned %+v", a)
	}
	if len(fake.queries) != 1 || !strings.Contains(fake.queries[0], "FROM agents WHERE name") {
		t.Errorf("GetByName SQL = %v, want name lookup on agents", fake.queries)
	}

	if _, err := store.NewAgentStore(&agentFakeDB{}).GetByName(ctx, "gemini"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown agent err = %v, want ErrNotFound", err)
	}
	if _, err := store.NewAgentStore(&agentFakeDB{}).GetByName(ctx, "  "); err == nil {
		t.Error("empty name must error")
	}
}

func TestAgentList(t *testing.T) {
	ctx := context.Background()
	fake := &agentFakeDB{rows: &agentFakeRows{rows: [][]any{
		{"id-1", "claude", "pull", "", 10000, "", "", agentTestTime},
		{"id-2", "copilot", "push", "", 8000, ".github/copilot-instructions.md", "markdown", agentTestTime},
	}}}
	got, err := store.NewAgentStore(fake).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "claude" || got[1].OutputFile != ".github/copilot-instructions.md" {
		t.Errorf("List returned %+v", got)
	}
	if !strings.Contains(fake.queries[0], "ORDER BY name") {
		t.Errorf("List SQL = %q, want ORDER BY name", fake.queries[0])
	}
}

func TestAgentEnabledForProject(t *testing.T) {
	ctx := context.Background()
	fake := &agentFakeDB{rows: &agentFakeRows{rows: [][]any{
		{"proj-1", "id-2", "copilot", "push", 8000, ".github/copilot-instructions.md", "markdown", true, ""},
		{"proj-1", "id-3", "cursor", "push", 6000, ".cursorrules", "text", true, `{"context_budget": 4500}`},
	}}}
	got, err := store.NewAgentStore(fake).EnabledForProject(ctx, "proj-1")
	if err != nil {
		t.Fatalf("EnabledForProject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("EnabledForProject returned %d rows, want 2", len(got))
	}
	if got[1].Budget() != 4500 {
		t.Errorf("cursor project budget = %d, want 4500 (config override)", got[1].Budget())
	}
	if !strings.Contains(fake.queries[0], "JOIN agents") || !strings.Contains(fake.queries[0], "pa.enabled") {
		t.Errorf("EnabledForProject SQL = %q, want enabled join", fake.queries[0])
	}
	if _, err := store.NewAgentStore(&agentFakeDB{}).EnabledForProject(ctx, ""); err == nil {
		t.Error("empty project id must error")
	}
}

func TestAgentSetProjectAgent(t *testing.T) {
	ctx := context.Background()
	fake := &agentFakeDB{rowQueue: []agentFakeRow{
		{values: []any{"id-2"}},
		projectAgentRow("proj-1", "id-2", "copilot", "push", 8000, ".github/copilot-instructions.md", "markdown", true, `{"context_budget": 7000}`),
	}}
	got, err := store.NewAgentStore(fake).SetProjectAgent(ctx, "proj-1", "id-2", true, `{"context_budget": 7000}`)
	if err != nil {
		t.Fatalf("SetProjectAgent: %v", err)
	}
	if got.AgentName != "copilot" || !got.Enabled || got.Budget() != 7000 {
		t.Errorf("SetProjectAgent returned %+v", got)
	}
	if len(fake.queries) != 2 || !strings.Contains(fake.queries[0], "ON CONFLICT (project_id, agent_id)") {
		t.Errorf("SetProjectAgent SQL = %v, want upsert + read-back", fake.queries)
	}
	if _, err := store.NewAgentStore(&agentFakeDB{}).SetProjectAgent(ctx, "", "id-2", true, ""); err == nil {
		t.Error("empty project id must error")
	}
	if _, err := store.NewAgentStore(&agentFakeDB{}).SetProjectAgent(ctx, "proj-1", "", true, ""); err == nil {
		t.Error("empty agent id must error")
	}
}

func TestAgentBudgetForProject(t *testing.T) {
	ctx := context.Background()

	// Project override wins over the agent default.
	withOverride := &agentFakeDB{rowQueue: []agentFakeRow{
		{values: []any{6000, `{"context_budget": 4500}`}},
	}}
	if got, err := store.NewAgentStore(withOverride).BudgetForProject(ctx, "proj-1", "cursor"); err != nil || got != 4500 {
		t.Errorf("BudgetForProject override = (%d, %v), want (4500, nil)", got, err)
	}

	// No config: the agent's own budget.
	plain := &agentFakeDB{rowQueue: []agentFakeRow{
		{values: []any{10000, ""}},
	}}
	if got, err := store.NewAgentStore(plain).BudgetForProject(ctx, "proj-1", "claude"); err != nil || got != 10000 {
		t.Errorf("BudgetForProject plain = (%d, %v), want (10000, nil)", got, err)
	}

	// Unknown agent: default fallback, no error.
	if got, err := store.NewAgentStore(&agentFakeDB{}).BudgetForProject(ctx, "proj-1", "gemini"); err != nil || got != ctxbuilder.DefaultBudgetChars {
		t.Errorf("BudgetForProject unknown = (%d, %v), want (%d, nil)", got, err, ctxbuilder.DefaultBudgetChars)
	}

	if _, err := store.NewAgentStore(&agentFakeDB{}).BudgetForProject(ctx, "", "claude"); err == nil {
		t.Error("empty project id must error")
	}
	if _, err := store.NewAgentStore(&agentFakeDB{}).BudgetForProject(ctx, "proj-1", ""); err == nil {
		t.Error("empty agent name must error")
	}
}

func TestAgentPushTargetsForProject(t *testing.T) {
	ctx := context.Background()
	fake := &agentFakeDB{rows: &agentFakeRows{rows: [][]any{
		{"proj-1", "id-1", "claude", "pull", 10000, "", "", true, ""},
		{"proj-1", "id-2", "copilot", "push", 8000, ".github/copilot-instructions.md", "markdown", true, ""},
		{"proj-1", "id-3", "cursor", "push", 6000, ".cursorrules", "text", true, ""},
	}}}
	got, err := store.NewAgentStore(fake).PushTargetsForProject(ctx, "proj-1")
	if err != nil {
		t.Fatalf("PushTargetsForProject: %v", err)
	}
	if len(got) != 2 || got["copilot"] != ".github/copilot-instructions.md" || got["cursor"] != ".cursorrules" {
		t.Errorf("PushTargetsForProject = %v", got)
	}
	if _, ok := got["claude"]; ok {
		t.Error("pull agent must not appear in project push targets")
	}
}
