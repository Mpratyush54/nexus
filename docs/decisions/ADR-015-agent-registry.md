# ADR-015 — Agent Registry, Budgets & Push Targets

- **ADR ID:** ADR-015-agent-registry
- **Date:** 2026-09-17
- **Author:** issue-#15 agent
- **Issue:** #15 Agent registry (`migrations/004_*` + `internal/store/agents.go`)
- **Status:** Accepted

## Context

Phase 4 (plan §4) serves the same memory to live MCP agents and static
instruction files. The registry must record, per agent, whether it pulls
live context or is pushed materialized files, how many chars the Context
Builder may assemble for it, and — for push agents — which file the
materializer regenerates. Per-project opt-in/out plus config overrides live
in `project_agents`.

Constraints colliding here:

1. **Parallel ownership** — `001_*`, `002_*`, `003_*`, `db.go`,
   `builder.go`, and every other store file belong to other issues; this
   issue owns only `migrations/004_*`, `internal/store/agents.go`
   (+ tests), `docs/`. The agent store must compose with the existing
   `DBTX`/`nullText` seam untouched, and `builder.go` must NOT be edited.
2. **Deferred FKs** — 002 left `events.agent_id` and 003 left
   `session_participants.agent_id` as bare UUIDs explicitly awaiting 004
   (see ADR-012 consequences), so 004 is the scheduled place to enforce
   them.
3. **Acceptance is behavioural** (seed values, budget fallback, push
   mapping, project overrides), so the budget/push/override rules must be
   testable without a live database.

## Options Considered

1. **Transcribe plan §4.1 verbatim, nothing more.**
   Pros: zero deviation. Cons: leaves the two deferred `agent_id` FKs
   unenforced forever despite 002/003 promising them for 004; no guidance
   on what a missing `project_agents` row means.
2. **(Chosen) Plan-exact tables + seed, deferred FKs completed, pure
   budget/push/override helpers with a DB-backed store on the DBTX seam.**
   `agents`/`project_agents` and all seven seed rows match the plan
   exactly; 004 adds the two promised FKs; `SeedAgents`, `BudgetFor`,
   `EffectiveBudget`, `ParseBudgetOverride`, `PushTargets` are pure funcs
   while `AgentStore` (`GetByName`, `List`, `EnabledForProject`,
   `SetProjectAgent`, `BudgetForProject`, `PushTargetsForProject`) runs
   SQL through `DBTX`.
3. **Enforce per-project budgets in SQL (CHECK on config, RLS, views).**
   Pros: DB-guaranteed. Cons: config is schemaless JSON by plan design;
   a CHECK cannot express "override wins, else agent default, else 4000"
   without stored procedures; untestable without live Postgres — the same
   argument that rejected RLS in ADR-012.

## Decision

- `migrations/004_agents.up.sql`: `agents` per plan §4.1 exactly
  (`name UNIQUE`, `adapter_type` pull/push CHECK, `capabilities JSONB`,
  `context_budget DEFAULT 4000`, `output_file`, `output_format`,
  `created_at`); seven seed rows (claude/opencode/codex/antigravity 10k
  pull; copilot 8k push `.github/copilot-instructions.md` markdown;
  cursor/windsurf 6k push with the plan's `.cursorrules`/`.windsurfrules`
  text files); `project_agents` per plan (project → projects,
  agent → agents, `enabled DEFAULT true`, `config JSONB`,
  PK on both ids); `ALTER TABLE session_participants ... REFERENCES
  agents(id)` and `ALTER TABLE events ... REFERENCES agents(id)` closing
  the 002/003 deferred FKs (no cascade — history outlives agent rows).
- `migrations/004_agents.down.sql`: drop both FKs, then
  `project_agents`, then `agents` (seeds die with the table); columns and
  extensions untouched.
- `internal/store/agents.go`: `Agent`/`ProjectAgent` types;
  `AdapterPull`/`AdapterPush`, `NormalizeAgentName`, `IsValidAdapterType`;
  `SeedAgents` mirroring the SQL seeds; pure `BudgetFor` (seed hit, else
  `context.DefaultBudgetChars`), `ParseBudgetOverride`
  (`{"context_budget": N}`, positive N only), `EffectiveBudget`
  (override → base → builder default), `PushTargets` (push + file only);
  `AgentStore` on `DBTX` with case-insensitive `GetByName`, ordered
  `List`, `EnabledForProject` (enabled-only join; absent rows mean "no
  explicit setting", not "all enabled"), `SetProjectAgent` (upsert +
  read-back), `BudgetForProject` (LEFT JOIN so unknown agents fall back
  to default without error), `PushTargetsForProject`.
- `builder.go` untouched: the integration is one-directional
  (`store → context` import for the default constant; `context` still
  imports no internal package, so no cycle — see Why).

## Why (Rationale)

- **Plan fidelity is verifiable by diff:** table DDL, seed names,
  budgets, files, and formats match plan §4.1 literally; the only
  additions are the two FKs that 002/003 explicitly deferred to 004 —
  verified by re-reading `002_events.up.sql` ("parent tables land in
  003/004") and ADR-012 ("004 must add FOREIGN KEY ... or document why
  not"). No silent scope creep into other owners' tables.
- **Seam composes untouched:** `AgentStore` uses only `DBTX`,
  `nullText`, `ErrNotFound` from sibling files — verified: `git status`
  shows only `migrations/004_*`, `internal/store/agents*.go`, `docs/`.
  The `store → context` import is cycle-free (`context` imports stdlib
  only) and is asserted at build time by `go build ./...`.
- **Budget fallback is proven DB-free:** `TestAgentBudgetFallback`
  asserts every seed budget plus unknown/empty → `DefaultBudgetChars`
  (imported from the real `context` package, so drift breaks the test);
  `TestAgentProjectOverrides` asserts override-wins/base-fallback/
  default-fallback; `TestAgentPushMapping` asserts the exact three-file
  fan-out with pull agents excluded.
- **Evidence:** `go build ./...` 0, `go vet ./internal/store/` 0,
  `go test ./internal/store/ -run TestAgent` unit tests pass (output in
  ISSUE-15.md).

## Consequences

- Materializer (#4.x follow-up) fans out over
  `PushTargetsForProject` and renders within `BudgetForProject`; the
  managed-section delimiters (`BEGIN/END CENTRAL MEMORY`) are its
  concern, not this issue's.
- `capabilities` JSONB is stored but uninterpreted — capability-gated
  tool exposure is a follow-up once the MCP server needs it.
- Memory Processor (#10) may write per-project `config` overrides
  (e.g. lower budgets for noisy projects); the parse contract is
  `ParseBudgetOverride`.
- RLS / SQL-view enforcement explicitly deferred (option 3).

## Alternatives Rejected

- Verbatim-only tables (option 1): strands the 002/003 deferred FKs and
  leaves "absent project_agents row" semantics undefined.
- SQL-enforced budgets (option 3): schemaless config defeats CHECKs;
  untestable without live DB for no extra Phase-4 guarantee.
