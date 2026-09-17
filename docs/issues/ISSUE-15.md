# ISSUE-15 — Agent Registry (Schema + Store + Budgets)

- **Issue:** #15 — Agent registry (`migrations/004_agents.up.sql` +
  `down.sql`, store, Context Builder budget hook)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `migrations/004_*`,
  `internal/store/agents.go` (+ test), `docs/`. Did NOT touch
  `implementation-plan.md` §4.1 source text, `adapters/registry.go`,
  `internal/context/builder.go`, `db.go`, `projects.go`, `workspaces.go`,
  `memory.go`, `sessions.go`, or any other package.
- **Plan ref:** `implementation-plan.md` §4.1 (agent table + seed rows +
  `project_agents` join); read first along with `adapters/registry.go`
  (14 adapter definitions — the registry of record for harvest paths) and
  `internal/context/builder.go` (`DefaultBudgetChars = 4000`, the
  fallback this issue integrates with but does not edit).

## What was built

| File | Contents |
|---|---|
| `migrations/004_agents.up.sql` | `agents` (plan §4.1 exactly: `name UNIQUE`, pull/push CHECK, `capabilities JSONB`, `context_budget DEFAULT 4000`, `output_file`, `output_format`); 7 seed rows (4× 10k pull, copilot 8k push markdown, cursor/windsurf 6k push text); `project_agents` join (`enabled DEFAULT true`, `config JSONB`, PK on both ids); two deferred FKs (`session_participants.agent_id`, `events.agent_id` → `agents(id)`, closing 002/003) |
| `migrations/004_agents.down.sql` | Reverse-order rollback: drop both FKs → `project_agents` → `agents`; columns/extensions kept |
| `internal/store/agents.go` | `Agent`/`ProjectAgent` types; adapter consts + `NormalizeAgentName`/`IsValidAdapterType`; `SeedAgents`; pure `BudgetFor` (Context Builder hook), `ParseBudgetOverride`, `EffectiveBudget`, `PushTargets`; `AgentStore`: `GetByName`, `List`, `EnabledForProject`, `SetProjectAgent`, `BudgetForProject`, `PushTargetsForProject` |
| `internal/store/agents_test.go` | 11 DB-free tests: seed contract, budget fallback, push mapping, project overrides, names/types, six store SQL paths via scripted `DBTX` fake |
| `docs/decisions/ADR-015-agent-registry.md` | Why-mandatory ADR (plan fidelity, deferred-FK closure, seam composition, DB-free budget proof) |
| `docs/issues/ISSUE-15.md` | This file |

## Decisions (see ADR-015 for rationale)

1. Tables + seeds transcribed plan-exact; cursor/windsurf output files
   (`.cursorrules`/`.windsurfrules`, `text`) taken from the plan INSERT —
   the issue text specifies only their budgets.
2. 004 closes both deferred `agent_id` FKs (002 events + 003 session
   participants, no cascade) — the follow-up ADR-012 explicitly
   scheduled for 004.
3. Absent `project_agents` row = "no explicit per-project setting"
   (global seed defaults apply); opt-out is an explicit
   `enabled = false` row, so `EnabledForProject` returns enabled-only
   joins and may be empty.
4. `BudgetFor` (pure, seed-or-`DefaultBudgetChars`) is the Context
   Builder hook; `builder.go` untouched — integration is the one-way
   `store → context` import, cycle-free by construction.

## Verification

- Structural SQL review of up/down (balanced parens/quotes, FK targets
  resolve against 001+002+003+004, down covers every object up creates):
  **passed** (output below).
- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test ./internal/store/ -run TestAgent -v` → unit tests **PASS**
  (output below).

Acceptance mapping: seed values (`TestAgentSeedValues`: all 7 names,
budgets, adapter types, copilot/cursor/windsurf files + formats);
budget fallback (`TestAgentBudgetFallback`: every seed budget,
case/whitespace normalization, unknown/empty → `DefaultBudgetChars`;
`TestAgentBudgetForProject`: override, plain, unknown-fallback);
push mapping (`TestAgentPushMapping`: exact 3-file fan-out, pull
excluded; `TestAgentPushTargetsForProject`: project-scoped fan-out);
project overrides (`TestAgentProjectOverrides`: parse table incl.
malformed/zero/negative, funnel order; `TestAgentSetProjectAgent`
upsert + read-back; `TestAgentEnabledForProject` join + row budget).

## Follow-ups (not this issue)

- Materializer: fan out over `PushTargetsForProject`, render within
  `BudgetForProject`, managed `BEGIN/END CENTRAL MEMORY` delimiters.
- `capabilities` JSONB is stored, uninterpreted — capability-gated MCP
  tool exposure when the server needs it.
- #10 processor: may write per-project `config` budget overrides;
  parse contract is `ParseBudgetOverride`.
- Live apply of 004 up/down against Aurora/pgvector in CI (no live
  Postgres in this env).
