# Agents Registry & Push Materializer — Design Decisions

Date: 2026-09-17
Scope: `migrations/004_agents.*`, `internal/store/agents_extra.go`,
`internal/materializer/` for
[Mpratyush54/nexus#15](https://github.com/Mpratyush54/nexus/issues/15) and
[Mpratyush54/nexus#16](https://github.com/Mpratyush54/nexus/issues/16)
(implementation-plan.md Phase 4.1 + 4.2).

## 1. Why the agent registry lives in SQL, not just `adapters/registry.go`

`adapters/registry.go` answers "where does this agent keep state on disk?"
(transcript paths for the harvester). The `agents` table answers different
questions at runtime: "is copilot enabled for this project?", "what is
cursor's token budget?", "where do I write push output?". Those answers must
be:

- **Per-project and mutable at runtime.** `project_agents.enabled` lets an
  admin turn off windsurf for one repo without redeploying the daemon — a
  compiled-in Go map cannot do that.
- **Consistent across the fleet.** Every daemon and the central server read
  the same rows; a local constant would drift between machines.
- **Queryable alongside memories.** `BudgetFor` and target-file lookups join
  naturally with project-scoped queries in the Context Builder and the
  materializer.

The Go side (`AgentRegistry` + `PostgresStore` methods in
`internal/store/agents_extra.go`) is a typed view over the table, not a
second source of truth: `DefaultAgents()` seeds mirror the migration's
`INSERT … ON CONFLICT DO UPDATE`, and agent **names** are kept identical to
the adapter registry (`claude`, `opencode`, `codex`, `antigravity`,
`copilot`, `cursor`, `windsurf`) so harvester paths, MCP tool routing, and
push targets never disagree on spelling.

## 2. Why per-agent budgets (10k / 8k / 6k) instead of one global cap

The Context Builder's 4000-char default is a safe floor, but push files are
read by the agent on every invocation with no retrieval step — whatever is
in `.cursorrules` is paid for on every prompt. Budgets encode that cost:

- **Pull agents (claude, opencode, codex, antigravity): 10000.** Served live
  via `memory_search`, so the Context Builder can rank and truncate per
  query; a generous cap preserves recall.
- **Copilot: 8000, markdown.** `copilot-instructions.md` supports structure;
  markdown headers survive truncation better than prose.
- **Cursor / Windsurf: 6000, plain text.** `.cursorrules` / `.windsurfrules`
  are flat text consumed wholesale; the tighter cap forces the materializer
  to keep only high-confidence items (sorted by confidence, trailing items
  dropped, output never exceeds budget).

`BudgetFor` falls back to 4000 for unknown names so a typo or a future
agent never produces an unbounded prompt.

## 3. Why a 5-second debounce on materialization

`MEMORY_CONFIRMED` events arrive in bursts (batch confirmations, promotion
sweeps, session-end deep analysis). Regenerating three files per event
would thrash the workspace, spam the file watcher with self-inflicted
change events, and race concurrent writes. The materializer rearms a single
`time.Timer` per trigger and only regenerates after 5s of quiet — a batch
of N confirmations costs one read + one write per target file. The trade-off
is bounded staleness (≤5s + render time), which the acceptance criterion
("regenerate within 5 seconds of stabilization") explicitly allows. Timer
re-arming drains the channel correctly so rapid bursts cannot leak timers.

## 4. Why managed-section delimiters instead of whole-file ownership

Push targets are shared with humans: users hand-edit `.cursorrules` to add
rules the system has never seen. Whole-file regeneration would either
destroy those edits or force a merge algorithm. Delimiters
(`<!-- BEGIN CENTRAL MEMORY — DO NOT EDIT -->` … `<!-- END CENTRAL MEMORY -->`)
split the file into two ownership zones:

- **Inside: machine-owned.** Replaced wholesale on each regeneration.
- **Outside: human-owned.** Preserved byte-for-byte, including whitespace.

`MergeManaged` appends a fresh section when none exists (never fails on
legacy files) and tolerates the ASCII `--` variant of the markers. The
watcher hook closes the loop: `OutsideEditChanged` / `OutsideEditProposal`
diff only the outside zones, so the materializer's own writes never
self-trigger, and genuine user edits surface as `PROPOSED` memory
candidates for the processor — satisfying the exit criterion "editing
`.cursorrules` manually causes extraction as PROPOSED" without any new
protocol.

## Consequences

- `go build ./...` and `go test ./internal/materializer/ ./internal/store/`
  are green; new code is stdlib-only apart from the pre-existing `pgx`
  dependency already used by `internal/store`.
- `AgentRegistry` carries its own state so `store.go` / `models.go` /
  `episodes.go` are untouched; the Postgres methods follow the existing
  `scanX` / `nullText` / `marshalPayload` conventions.
- Known limitation: `RenderMarkdown`/`RenderText` budget in chars (same
  approximation as the Context Builder's `TokenCount`); a tokenizer-aware
  cap can replace the char check without changing the merge/watcher logic.
