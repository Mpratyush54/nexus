package mcp

// Tests for per-agent context budgets (issue #41): registry seed budget
// wins when AgentName + Budgets select one, static TokenBudget otherwise.
// A budget never fails a search — every fallback is covered.

import (
	"testing"

	"central-memory/internal/store"
)

func testRegistry() *store.AgentRegistry {
	r := store.NewAgentRegistry()
	for _, a := range store.DefaultAgents() {
		a := a
		r.UpsertAgent(&a)
	}
	return r
}

func TestTokenBudgetPerAgentRegistryWins(t *testing.T) {
	r := testRegistry()
	s := NewServer(nil, Config{AgentName: "claude", Budgets: r, TokenBudget: 123})
	if got := s.tokenBudget(); got != 10000 {
		t.Fatalf("claude budget = %d, want registry seed 10000", got)
	}
	s = NewServer(nil, Config{AgentName: "cursor", Budgets: r})
	if got := s.tokenBudget(); got != 6000 {
		t.Fatalf("cursor budget = %d, want registry seed 6000", got)
	}
}

func TestTokenBudgetFallsBackToStatic(t *testing.T) {
	r := testRegistry()
	// No agent: static applies.
	s := NewServer(nil, Config{Budgets: r, TokenBudget: 123})
	if got := s.tokenBudget(); got != 123 {
		t.Fatalf("empty agent budget = %d, want static 123", got)
	}
	// Nil resolver: static applies.
	s = NewServer(nil, Config{AgentName: "claude", TokenBudget: 123})
	if got := s.tokenBudget(); got != 123 {
		t.Fatalf("nil resolver budget = %d, want static 123", got)
	}
	// Nothing set: builder default.
	s = NewServer(nil, Config{})
	if got := s.tokenBudget(); got != DefaultTokenBudget {
		t.Fatalf("default budget = %d, want %d", got, DefaultTokenBudget)
	}
}
