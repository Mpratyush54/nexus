# Orchestration Log — 2026-09-17 multi-agent run

## Issue inventory (26 open, from api.github.com)
#1 DB migrations core+pgvector | #2 Storage layer+pool | #3 Daemon core+sandbox |
#4 Interceptor+watcher | #5 Transcript harvester | #6 Context builder+vector search |
#7 MCP server | #8 REST API+heartbeats | #9 Event store+bus | #10 Memory processor |
#11 Episodes engine | #12 Sessions/scoping | #13 WS hub+presence | #14 Web dashboard |
#15 Agent registry+budgets | #16 Materializer | #17 CoW branching | #18 Diff/merge engine |
#19 Security hardening | #20 AWS infra | #21 CLI client | #22 Steering/interrupt |
#23 Handoff protocol | #24 Daemon platform service | #25 Migration importer | #26 Cost/governance

## Wave plan (dependency-ordered, maximally parallel)
- **Wave 1 (now, 6 parallel):** #1 migrations, #2 storage pool, #3 daemon core,
  #4 interceptor+watcher, #5 harvester, #6 context builder.
  Rationale: distinct directories (`migrations/`, `internal/store/`, `internal/daemon/`,
  `internal/context/`) → minimal merge conflicts. #1 is foundation but others can
  code against the *planned* schema in `implementation-plan.md` §1.1 without blocking.
- **Wave 2 (next):** #7 MCP, #8 server, #9 events, #10 processor, #11 episodes, #12 sessions.
- **Wave 3:** #13 WS hub, #14 web, #15 agents, #16 materializer, #17 branches, #18 diff/merge.
- **Wave 4:** #19 security, #20 AWS, #21 CLI, #22 steering, #23 handoff, #24 platform, #25 migrate, #26 cost.

## Critical orchestrator decisions
- ADR-000-orchestration: parallelize by directory ownership to avoid write conflicts.
  Why: 6 agents editing disjoint paths can run concurrently; sequential would take 6x longer.
- ADR-000-docs: enforce `docs/decisions/ADR-<issue>-*.md` + `docs/issues/ISSUE-<n>.md` per subagent.
  Why: user explicitly required every critical decision logged with rationale.
- ADR-000-go-deps: subagents may add `pgx/v5`, `pgvector-go`, `fsnotify`, `jwt`, `websocket`
  per plan §1.9 only. Why: plan locks these; anything else needs a new ADR.
