# Decision Log Index

Every critical decision must be logged in `docs/decisions/` with WHY.
Format: `YYYY-MM-DD-<slug>.md` with sections: Context, Decision, Alternatives, Why, Consequences.

## Active waves (2026-09-17)

| Wave | Issues | Owner subagents | Status |
|------|--------|-----------------|--------|
| 1 Foundation | #1 schema, #2 store, #9 events | foundation-agent | launched |
| 2 Daemon | #3 daemon core, #4 interceptor+watcher, #5 harvester, #10 processor | daemon-agents x3 | launched |
| 3 Retrieval | #6 context, #7 mcp, #8 server, #11 episodes, #12 sessions, #13 ws | retrieval-agents x3 | launched |
| 4 Product | #14 dashboard, #15 agents, #16 materializer, #21 CLI, #25 migration | product-agents | queued |
| 5 Advanced | #17 branches, #18 diff/merge, #19 security, #20 AWS, #22 steering, #23 handoff, #24 platform, #26 cost | advanced-agents | queued |

Rule: each subagent MUST append a file under `docs/decisions/` before finishing.
