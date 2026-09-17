package mcp

// budgets.go — per-agent context budgets (issue #41).
//
// Master owns server.go (static TokenBudget) and the store package owns the
// agent registry (AgentRegistry.BudgetFor: claude/opencode 10k, copilot 8k,
// cursor/windsurf 6k, fallback 4000). This file is purely additive: it
// defines the narrow BudgetResolver seam plus resolution order, and server.go
// gains two Config fields (AgentName, Budgets) consulted by tokenBudget.
//
// Resolution order: AgentName + Budgets set → registry seed budget for the
// named agent; otherwise the static TokenBudget (or DefaultTokenBudget).
// A budget never fails a search — nil resolver or empty name falls through.
// (Project-scoped overrides do not exist on master's registry yet; when they
// land, extend BudgetResolver without touching callers.)

import (
	"strings"

	"central-memory/internal/store"
)

// BudgetResolver resolves the context budget for a named agent.
// *store.AgentRegistry implements it.
type BudgetResolver interface {
	BudgetFor(agentName string) int
}

// Compile-time proof the production registry satisfies the narrow seam.
var _ BudgetResolver = (*store.AgentRegistry)(nil)

// resolveBudget applies the #41 resolution order.
func resolveBudget(agentName string, resolver BudgetResolver, staticBudget int) int {
	if resolver != nil && strings.TrimSpace(agentName) != "" {
		if b := resolver.BudgetFor(agentName); b > 0 {
			return b
		}
	}
	if staticBudget <= 0 {
		return DefaultTokenBudget
	}
	return staticBudget
}
