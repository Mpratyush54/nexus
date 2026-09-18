// permissions.go — MCP agent mode / tool / rate-limit gates (issue #166).
package mcp

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Agent modes mirror store.AgentMode* (kept local so mcp stays store-free).
const (
	ModeReadOnly    = "read_only"
	ModeProposeOnly = "propose_only"
	ModeFull        = "full"
	ModeBlocked     = "blocked"
)

// AgentAccess is the runtime permission snapshot for one agent on a project.
type AgentAccess struct {
	AgentID   string
	Mode      string
	RateLimit int // calls/minute; 0 = unlimited
	Tools     map[string]bool
}

// PermissionError is returned when a tool call is denied.
type PermissionError struct {
	Reason string
}

func (e *PermissionError) Error() string { return e.Reason }

func isWriteTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "memory_write", "memory_reflect", "episode_report", "file_write":
		return true
	default:
		return false
	}
}

// AllowTool reports whether access permits the named tool.
func (a *AgentAccess) AllowTool(tool string) error {
	if a == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" {
		mode = ModeFull
	}
	if mode == ModeBlocked {
		return &PermissionError{Reason: fmt.Sprintf("agent %q is blocked", a.AgentID)}
	}
	tool = strings.TrimSpace(tool)
	if tool != "" && a.Tools != nil {
		if allowed, ok := a.Tools[tool]; ok && !allowed {
			return &PermissionError{Reason: fmt.Sprintf("tool %q is not allowed for agent %q", tool, a.AgentID)}
		}
	}
	if mode == ModeReadOnly && isWriteTool(tool) {
		return &PermissionError{Reason: fmt.Sprintf("agent %q is read_only; %q denied", a.AgentID, tool)}
	}
	return nil
}

// ForceProposed reports whether writes must stay PROPOSED (propose_only mode).
func (a *AgentAccess) ForceProposed() bool {
	if a == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(a.Mode)) == ModeProposeOnly
}

// RateLimiter enforces calls/minute per agent key.
type RateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// NewRateLimiter returns an empty limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{hits: make(map[string][]time.Time)}
}

// Allow returns false when key exceeded limit calls in the last minute.
func (r *RateLimiter) Allow(key string, limit int) bool {
	if r == nil || limit <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	r.mu.Lock()
	defer r.mu.Unlock()
	recent := r.hits[key][:0]
	for _, t := range r.hits[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= limit {
		r.hits[key] = recent
		return false
	}
	r.hits[key] = append(recent, now)
	return true
}
