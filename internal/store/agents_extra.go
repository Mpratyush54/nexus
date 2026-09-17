package store

// agents_extra.go — Agent registry: CRUD, per-project enablement, budgets.
//
// Phase 4.1 (Mpratyush54/nexus#15). New file only: MemStore/PostgresStore
// core types in store.go, models.go, episodes.go are untouched.
//
// Two access paths, same seed data (mirrors migrations/004_agents.up.sql):
//   - AgentRegistry: stdlib-only in-memory registry for tests, local dev,
//     and callers with no database (context builder budget lookups).
//   - PostgresStore methods (ListAgents/GetAgentByName/UpsertAgent,
//     SetProjectAgent/ListProjectAgents/BudgetFor): production path over
//     the agents + project_agents tables. Requires a live pool, like all
//     other PostgresStore methods in db.go.
//
// Agent names align with adapters/registry.go: claude, opencode, codex,
// antigravity (pull) + copilot, cursor, windsurf (push).

import (
	"context"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// Adapter types.
const (
	AdapterPull = "pull"
	AdapterPush = "push"
)

// DefaultBudgetFallback is used when an agent name is unknown.
const DefaultBudgetFallback = 4000

// Agent is one row of the agents table.
type Agent struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	AdapterType   string         `json:"adapter_type"` // pull | push
	Capabilities  map[string]any `json:"capabilities,omitempty"`
	ContextBudget int            `json:"context_budget"`
	OutputFile    string         `json:"output_file,omitempty"`
	OutputFormat  string         `json:"output_format,omitempty"` // markdown | text
}

// ProjectAgent is one row of project_agents.
type ProjectAgent struct {
	ProjectID string         `json:"project_id"`
	AgentID   string         `json:"agent_id"`
	AgentName string         `json:"agent_name,omitempty"`
	Enabled   bool           `json:"enabled"`
	Config    map[string]any `json:"config,omitempty"`
}

// DefaultAgents returns the 7 seeded agents from 004_agents.up.sql.
// Budgets: pull agents 10k chars, copilot 8k, cursor/windsurf 6k.
func DefaultAgents() []Agent {
	return []Agent{
		{Name: "claude", AdapterType: AdapterPull, ContextBudget: 10000, Capabilities: map[string]any{"mcp": true}},
		{Name: "opencode", AdapterType: AdapterPull, ContextBudget: 10000, Capabilities: map[string]any{"mcp": true}},
		{Name: "codex", AdapterType: AdapterPull, ContextBudget: 10000, Capabilities: map[string]any{"mcp": true}},
		{Name: "antigravity", AdapterType: AdapterPull, ContextBudget: 10000, Capabilities: map[string]any{"mcp": true}},
		{Name: "copilot", AdapterType: AdapterPush, ContextBudget: 8000, OutputFile: ".github/copilot-instructions.md", OutputFormat: "markdown", Capabilities: map[string]any{"instruction_file": true}},
		{Name: "cursor", AdapterType: AdapterPush, ContextBudget: 6000, OutputFile: ".cursorrules", OutputFormat: "text", Capabilities: map[string]any{"instruction_file": true}},
		{Name: "windsurf", AdapterType: AdapterPush, ContextBudget: 6000, OutputFile: ".windsurfrules", OutputFormat: "text", Capabilities: map[string]any{"instruction_file": true}},
	}
}

// IsPush reports whether the agent is served via generated files.
func (a *Agent) IsPush() bool { return a != nil && a.AdapterType == AdapterPush }

// IsPull reports whether the agent is served live over MCP.
func (a *Agent) IsPull() bool { return a != nil && a.AdapterType == AdapterPull }

// ---- In-memory registry (stdlib only) ----

// AgentRegistry is a thread-safe standalone agent catalog + per-project
// enablement map. It deliberately carries its own state instead of
// extending MemStore so store.go stays untouched.
type AgentRegistry struct {
	mu            sync.RWMutex
	agents        map[string]*Agent                   // key: lower-cased name
	projectAgents map[string]map[string]*ProjectAgent // projectID -> agentName -> row
}

// NewAgentRegistry seeds the 7 default agents with all projects enabled.
func NewAgentRegistry() *AgentRegistry {
	r := &AgentRegistry{
		agents:        make(map[string]*Agent),
		projectAgents: make(map[string]map[string]*ProjectAgent),
	}
	for _, a := range DefaultAgents() {
		cp := a
		r.agents[strings.ToLower(cp.Name)] = &cp
	}
	return r
}

// GetAgent returns the agent by name (case-insensitive) or ErrNotFound.
func (r *AgentRegistry) GetAgent(name string) (*Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}

// ListAgents returns all agents in seed order.
func (r *AgentRegistry) ListAgents() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	order := []string{"claude", "opencode", "codex", "antigravity", "copilot", "cursor", "windsurf"}
	out := make([]*Agent, 0, len(r.agents))
	for _, n := range order {
		if a, ok := r.agents[n]; ok {
			cp := *a
			out = append(out, &cp)
		}
	}
	for n, a := range r.agents {
		found := false
		for _, o := range order {
			if o == n {
				found = true
				break
			}
		}
		if !found {
			cp := *a
			out = append(out, &cp)
		}
	}
	return out
}

// UpsertAgent inserts or replaces an agent definition.
func (r *AgentRegistry) UpsertAgent(a *Agent) {
	if a == nil || strings.TrimSpace(a.Name) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	cp.Name = strings.ToLower(strings.TrimSpace(cp.Name))
	if cp.ContextBudget <= 0 {
		cp.ContextBudget = DefaultBudgetFallback
	}
	r.agents[cp.Name] = &cp
}

// SetProjectAgentEnabled enables/disables one agent for a project.
func (r *AgentRegistry) SetProjectAgentEnabled(projectID, agentName string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(agentName))
	a, ok := r.agents[key]
	if !ok {
		return ErrNotFound
	}
	m, ok := r.projectAgents[projectID]
	if !ok {
		m = make(map[string]*ProjectAgent)
		r.projectAgents[projectID] = m
	}
	m[key] = &ProjectAgent{ProjectID: projectID, AgentID: a.ID, AgentName: a.Name, Enabled: enabled}
	return nil
}

// IsEnabled reports whether an agent is enabled for a project.
// Default is true (no explicit row means enabled).
func (r *AgentRegistry) IsEnabled(projectID, agentName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.projectAgents[projectID]; ok {
		if row, ok := m[strings.ToLower(strings.TrimSpace(agentName))]; ok {
			return row.Enabled
		}
	}
	return true
}

// BudgetFor returns the context budget (chars) for an agent name.
// Unknown names fall back to DefaultBudgetFallback so the Context Builder
// always has a cap to enforce (issue #15 acceptance: unique budgets).
func (r *AgentRegistry) BudgetFor(agentName string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.agents[strings.ToLower(strings.TrimSpace(agentName))]; ok && a.ContextBudget > 0 {
		return a.ContextBudget
	}
	return DefaultBudgetFallback
}

// OutputFileFor returns the push target file for an agent, or "" for pull.
func (r *AgentRegistry) OutputFileFor(agentName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.agents[strings.ToLower(strings.TrimSpace(agentName))]; ok {
		return a.OutputFile
	}
	return ""
}

// ---- Postgres-backed CRUD (production path) ----

const agentColumns = `id, name, adapter_type, capabilities, context_budget, output_file, output_format`

func scanAgent(row pgx.Row) (*Agent, error) {
	var a Agent
	var caps []byte
	var outputFile, outputFormat *string
	if err := row.Scan(&a.ID, &a.Name, &a.AdapterType, &caps, &a.ContextBudget, &outputFile, &outputFormat); err != nil {
		return nil, err
	}
	a.Capabilities = unmarshalPayload(caps)
	if outputFile != nil {
		a.OutputFile = *outputFile
	}
	if outputFormat != nil {
		a.OutputFormat = *outputFormat
	}
	return &a, nil
}

// ListAgents returns all agent rows ordered by name.
func (s *PostgresStore) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentColumns+` FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgentByName fetches one agent by name.
func (s *PostgresStore) GetAgentByName(ctx context.Context, name string) (*Agent, error) {
	a, err := scanAgent(s.pool.QueryRow(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE name = $1`, strings.ToLower(strings.TrimSpace(name))))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return a, err
}

// UpsertAgent inserts or updates an agent row by name.
func (s *PostgresStore) UpsertAgent(ctx context.Context, a *Agent) error {
	if a == nil || strings.TrimSpace(a.Name) == "" {
		return ErrConflict
	}
	budget := a.ContextBudget
	if budget <= 0 {
		budget = DefaultBudgetFallback
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO agents (name, adapter_type, capabilities, context_budget, output_file, output_format)
		 VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''))
		 ON CONFLICT (name) DO UPDATE SET
		     adapter_type = EXCLUDED.adapter_type,
		     capabilities = EXCLUDED.capabilities,
		     context_budget = EXCLUDED.context_budget,
		     output_file = EXCLUDED.output_file,
		     output_format = EXCLUDED.output_format
		 RETURNING id`,
		strings.ToLower(strings.TrimSpace(a.Name)), a.AdapterType,
		marshalPayload(a.Capabilities), budget,
		a.OutputFile, a.OutputFormat).Scan(&a.ID)
}

// SetProjectAgent enables/disables an agent for a project (upsert).
func (s *PostgresStore) SetProjectAgent(ctx context.Context, projectID, agentName string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO project_agents (project_id, agent_id, enabled)
		 SELECT $1::uuid, id, $3 FROM agents WHERE name = $2
		 ON CONFLICT (project_id, agent_id) DO UPDATE SET enabled = EXCLUDED.enabled`,
		nullText(projectID), strings.ToLower(strings.TrimSpace(agentName)), enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListProjectAgents returns enablement rows for one project.
func (s *PostgresStore) ListProjectAgents(ctx context.Context, projectID string) ([]*ProjectAgent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT pa.project_id, pa.agent_id, a.name, pa.enabled, pa.config
		  FROM project_agents pa JOIN agents a ON a.id = pa.agent_id
		  WHERE pa.project_id = $1::uuid ORDER BY a.name`, nullText(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProjectAgent
	for rows.Next() {
		var pa ProjectAgent
		var cfg []byte
		if err := rows.Scan(&pa.ProjectID, &pa.AgentID, &pa.AgentName, &pa.Enabled, &cfg); err != nil {
			return nil, err
		}
		pa.Config = unmarshalPayload(cfg)
		out = append(out, &pa)
	}
	return out, rows.Err()
}

// BudgetFor returns the context budget for an agent (DB path).
// Unknown agents fall back to DefaultBudgetFallback.
func (s *PostgresStore) BudgetFor(ctx context.Context, agentName string) (int, error) {
	var budget int
	err := s.pool.QueryRow(ctx, `SELECT context_budget FROM agents WHERE name = $1`,
		strings.ToLower(strings.TrimSpace(agentName))).Scan(&budget)
	if err == pgx.ErrNoRows {
		return DefaultBudgetFallback, nil
	}
	if err != nil {
		return 0, err
	}
	return budget, nil
}
