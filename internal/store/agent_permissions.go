// agent_permissions.go — per-project MCP agent access control (issue #166).
//
// Modes: read_only | propose_only | full | blocked. rate_limit is
// calls/minute (0 = unlimited). Rows with tool_name='*' carry agent-level
// mode/rate; other tool_name rows are per-tool allow flags. Both MemStore
// and PostgresStore implement AgentPermissionStore so server handlers stay
// store-agnostic via a seam (same pattern as memory_edit).

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Agent access modes (implementation-plan-v2.md §6).
const (
	AgentModeReadOnly    = "read_only"
	AgentModeProposeOnly = "propose_only"
	AgentModeFull        = "full"
	AgentModeBlocked     = "blocked"

	// AgentToolWildcard is the sentinel tool_name for agent-level mode/rate.
	AgentToolWildcard = "*"

	// DefaultAgentRateLimit is calls/minute when unset.
	DefaultAgentRateLimit = 60
)

// KnownMCPTools is the Phase 1.4 MCP tool catalog (plan §6).
var KnownMCPTools = []string{
	"memory_search", "memory_write", "memory_reflect",
	"episode_search", "episode_report", "workspace_info",
	"file_read", "file_write",
}

// AgentPermission is the aggregated per-agent config returned by the API.
type AgentPermission struct {
	ProjectID string          `json:"project_id"`
	AgentID   string          `json:"agent_id"`
	Mode      string          `json:"mode"`
	RateLimit int             `json:"rate_limit"`
	Tools     map[string]bool `json:"tools"`
	UpdatedAt time.Time       `json:"updated_at,omitempty"`
}

// AgentPermissionStore is the Phase 6 agent-permissions surface.
type AgentPermissionStore interface {
	ListAgentPermissions(ctx context.Context, projectID string) ([]*AgentPermission, error)
	GetAgentPermission(ctx context.Context, projectID, agentID string) (*AgentPermission, error)
	SetAgentPermission(ctx context.Context, perm *AgentPermission) error
	DeleteAgentPermission(ctx context.Context, projectID, agentID string) error
}

// NormalizeAgentMode lowercases and validates mode; empty → full.
func NormalizeAgentMode(mode string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return AgentModeFull, nil
	}
	switch m {
	case AgentModeReadOnly, AgentModeProposeOnly, AgentModeFull, AgentModeBlocked:
		return m, nil
	case "read-only", "readonly":
		return AgentModeReadOnly, nil
	case "propose-only", "propose":
		return AgentModeProposeOnly, nil
	default:
		return "", fmt.Errorf("store: invalid agent mode %q (want read_only|propose_only|full|blocked)", mode)
	}
}

// IsWriteMCPTool reports whether tool mutates memory/files/episodes.
func IsWriteMCPTool(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "memory_write", "memory_reflect", "episode_report", "file_write":
		return true
	default:
		return false
	}
}

// CheckAgentToolAllowed evaluates mode + per-tool flags for one call.
// Missing config defaults to full access (back-compat for unconfigured agents).
func CheckAgentToolAllowed(perm *AgentPermission, tool string) error {
	if perm == nil {
		return nil
	}
	mode := perm.Mode
	if mode == "" {
		mode = AgentModeFull
	}
	if mode == AgentModeBlocked {
		return fmt.Errorf("store: agent %q is blocked for this project", perm.AgentID)
	}
	tool = strings.TrimSpace(tool)
	if tool != "" && perm.Tools != nil {
		if allowed, ok := perm.Tools[tool]; ok && !allowed {
			return fmt.Errorf("store: tool %q is not allowed for agent %q", tool, perm.AgentID)
		}
	}
	if mode == AgentModeReadOnly && IsWriteMCPTool(tool) {
		return fmt.Errorf("store: agent %q is read_only; tool %q denied", perm.AgentID, tool)
	}
	return nil
}

func cloneAgentPermission(p *AgentPermission) *AgentPermission {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Tools != nil {
		cp.Tools = make(map[string]bool, len(p.Tools))
		for k, v := range p.Tools {
			cp.Tools[k] = v
		}
	} else {
		cp.Tools = map[string]bool{}
	}
	return &cp
}

func normalizeAgentPermission(p *AgentPermission) error {
	if p == nil {
		return fmt.Errorf("store: agent permission is required")
	}
	p.ProjectID = strings.TrimSpace(p.ProjectID)
	p.AgentID = strings.TrimSpace(p.AgentID)
	if p.ProjectID == "" {
		return fmt.Errorf("store: project_id is required")
	}
	if p.AgentID == "" {
		return fmt.Errorf("store: agent_id is required")
	}
	mode, err := NormalizeAgentMode(p.Mode)
	if err != nil {
		return err
	}
	p.Mode = mode
	if p.RateLimit < 0 {
		return fmt.Errorf("store: rate_limit must be >= 0")
	}
	if p.Tools == nil {
		p.Tools = map[string]bool{}
	}
	normalized := make(map[string]bool, len(p.Tools))
	for k, v := range p.Tools {
		k = strings.TrimSpace(k)
		if k == "" || k == AgentToolWildcard {
			continue
		}
		normalized[k] = v
	}
	p.Tools = normalized
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	return nil
}

// ---- MemStore ----

func (s *MemStore) ensureAgentPerms() {
	// Lazily allocated so NewMemStore stays unchanged for older tests.
	if s.agentPerms == nil {
		s.agentPerms = make(map[string]map[string]*AgentPermission)
	}
}

// ListAgentPermissions returns all configured agents for a project.
func (s *MemStore) ListAgentPermissions(_ context.Context, projectID string) ([]*AgentPermission, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("store: project_id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.agentPerms[projectID]
	out := make([]*AgentPermission, 0, len(m))
	for _, p := range m {
		out = append(out, cloneAgentPermission(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

// GetAgentPermission returns one agent config or ErrNotFound.
func (s *MemStore) GetAgentPermission(_ context.Context, projectID, agentID string) (*AgentPermission, error) {
	projectID = strings.TrimSpace(projectID)
	agentID = strings.TrimSpace(agentID)
	if projectID == "" || agentID == "" {
		return nil, fmt.Errorf("store: project_id and agent_id are required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.agentPerms[projectID][agentID]; ok {
		return cloneAgentPermission(p), nil
	}
	return nil, ErrNotFound
}

// SetAgentPermission upserts one agent config.
func (s *MemStore) SetAgentPermission(_ context.Context, perm *AgentPermission) error {
	if err := normalizeAgentPermission(perm); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[perm.ProjectID]; !ok {
		return fmt.Errorf("store: project %s: %w", perm.ProjectID, ErrNotFound)
	}
	s.ensureAgentPerms()
	if s.agentPerms[perm.ProjectID] == nil {
		s.agentPerms[perm.ProjectID] = make(map[string]*AgentPermission)
	}
	cp := cloneAgentPermission(perm)
	cp.UpdatedAt = time.Now().UTC()
	s.agentPerms[perm.ProjectID][perm.AgentID] = cp
	perm.UpdatedAt = cp.UpdatedAt
	return nil
}

// DeleteAgentPermission removes one agent config. Missing is a no-op.
func (s *MemStore) DeleteAgentPermission(_ context.Context, projectID, agentID string) error {
	projectID = strings.TrimSpace(projectID)
	agentID = strings.TrimSpace(agentID)
	if projectID == "" || agentID == "" {
		return fmt.Errorf("store: project_id and agent_id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agentPerms[projectID], agentID)
	return nil
}

// ---- PostgresStore ----

func (s *PostgresStore) ListAgentPermissions(ctx context.Context, projectID string) ([]*AgentPermission, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("store: project_id is required")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT project_id::text, agent_id, tool_name, allowed, mode, rate_limit, updated_at
		   FROM agent_permissions
		  WHERE project_id = $1::uuid
		  ORDER BY agent_id, tool_name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentPermissionRows(rows)
}

func (s *PostgresStore) GetAgentPermission(ctx context.Context, projectID, agentID string) (*AgentPermission, error) {
	projectID = strings.TrimSpace(projectID)
	agentID = strings.TrimSpace(agentID)
	if projectID == "" || agentID == "" {
		return nil, fmt.Errorf("store: project_id and agent_id are required")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT project_id::text, agent_id, tool_name, allowed, mode, rate_limit, updated_at
		   FROM agent_permissions
		  WHERE project_id = $1::uuid AND agent_id = $2
		  ORDER BY tool_name`, projectID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanAgentPermissionRows(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

func (s *PostgresStore) SetAgentPermission(ctx context.Context, perm *AgentPermission) error {
	if err := normalizeAgentPermission(perm); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`DELETE FROM agent_permissions WHERE project_id = $1::uuid AND agent_id = $2`,
		perm.ProjectID, perm.AgentID); err != nil {
		return err
	}
	now := time.Now().UTC()
	perm.UpdatedAt = now
	if _, err := tx.Exec(ctx,
		`INSERT INTO agent_permissions (project_id, agent_id, tool_name, allowed, mode, rate_limit, updated_at)
		 VALUES ($1::uuid, $2, $3, true, $4, $5, $6)`,
		perm.ProjectID, perm.AgentID, AgentToolWildcard, perm.Mode, perm.RateLimit, now); err != nil {
		return err
	}
	for tool, allowed := range perm.Tools {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_permissions (project_id, agent_id, tool_name, allowed, mode, rate_limit, updated_at)
			 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			perm.ProjectID, perm.AgentID, tool, allowed, perm.Mode, perm.RateLimit, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) DeleteAgentPermission(ctx context.Context, projectID, agentID string) error {
	projectID = strings.TrimSpace(projectID)
	agentID = strings.TrimSpace(agentID)
	if projectID == "" || agentID == "" {
		return fmt.Errorf("store: project_id and agent_id are required")
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM agent_permissions WHERE project_id = $1::uuid AND agent_id = $2`,
		projectID, agentID)
	return err
}

type agentPermRowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanAgentPermissionRows(rows agentPermRowScanner) ([]*AgentPermission, error) {
	byAgent := map[string]*AgentPermission{}
	order := []string{}
	for rows.Next() {
		var projectID, agentID, toolName, mode string
		var allowed bool
		var rateLimit int
		var updatedAt time.Time
		if err := rows.Scan(&projectID, &agentID, &toolName, &allowed, &mode, &rateLimit, &updatedAt); err != nil {
			return nil, err
		}
		p, ok := byAgent[agentID]
		if !ok {
			p = &AgentPermission{
				ProjectID: projectID,
				AgentID:   agentID,
				Mode:      mode,
				RateLimit: rateLimit,
				Tools:     map[string]bool{},
				UpdatedAt: updatedAt,
			}
			byAgent[agentID] = p
			order = append(order, agentID)
		}
		if toolName == AgentToolWildcard {
			p.Mode = mode
			p.RateLimit = rateLimit
			if updatedAt.After(p.UpdatedAt) {
				p.UpdatedAt = updatedAt
			}
			continue
		}
		p.Tools[toolName] = allowed
		// Keep mode/rate from wildcard if present; otherwise last non-wildcard.
		if p.Mode == "" {
			p.Mode = mode
		}
		if p.RateLimit == 0 && rateLimit > 0 {
			p.RateLimit = rateLimit
		}
		if updatedAt.After(p.UpdatedAt) {
			p.UpdatedAt = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*AgentPermission, 0, len(order))
	for _, id := range order {
		out = append(out, byAgent[id])
	}
	return out, nil
}

// MarshalAgentTools is a test/helper for JSON round-trips.
func MarshalAgentTools(tools map[string]bool) ([]byte, error) {
	if tools == nil {
		tools = map[string]bool{}
	}
	return json.Marshal(tools)
}
