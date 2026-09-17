# Nexus Decision Log

Every critical decision taken by any agent (human or subagent) MUST be logged here.
No code merges without a corresponding ADR entry explaining **why**.

## Structure

- `docs/decisions/ADR-<issue>-<slug>.md` — Architecture Decision Records. One per critical decision.
  Template: `docs/decisions/_TEMPLATE.md`
- `docs/issues/ISSUE-<n>.md` — Per-issue progress log: status, assignee (subagent id), decisions, verification.
- `docs/ORCHESTRATION.md` — This run's wave plan: which subagents ran in parallel, dependency rationale.

## ADR Template Fields

`Context | Options considered | Decision | Why (rationale) | Consequences | Alternatives rejected`

## Rule

Subagents: append your ADR(s) BEFORE finishing. Reference them in your final summary.
Orchestrator verifies `go build ./...` + `go vet` where applicable.
