# ADR-030 - Ephemeral Memory Level (5th Tier)

- **ADR ID:** ADR-030-ephemeral-level
- **Date:** 2026-09-17
- **Author:** ParthKhandelwal537
- **Issue:** #30 plan promises 5 lifetime tiers incl. Ephemeral; code enforced 4
- **Status:** Accepted

## Context

The plan's Locked Decisions table lists 5 tiers (Organization →
Project → Personal → Session → Ephemeral), but migration 001's CHECK
and every validator enforced 4. Per the assignee's call, the plan is
NOT amended — the code gains the fifth tier instead.

## Decisions

1. **Migration 009** widens the CHECK (`DROP CONSTRAINT IF EXISTS` +
   re-add under the same `memory_items_level_check` name). Rollback
   restores 4 tiers (fails while ephemeral rows exist).
2. **Shortest lifetime wins**: `context.LevelRank("ephemeral") = 4`,
   above session; `ResolveOverrides` and the render shadowing follow
   with no special-casing. New `<ephemeral>` first-memory section in
   `AssembleXML` (`ContextInput.EphemeralItems`).
3. **Never guessed**: heuristic classification (`ClassifyLevel`) keeps
   defaulting SESSION; `LevelEphemeral` exists in daemon/store/mcp
   vocabularies so producers (handoff working memory today) can assign
   it explicitly. Validators (`ValidateMemoryLevel`, MCP
   `validLevels`) accept it.
4. **No retention change**: expiry/GC for ephemeral rows is a follow-up;
   the tier is scoping + override priority only.

## Consequences

- Pre-009 databases read/write exactly as before until 009 applies
  (runner applies it on next boot; nullable-safe, additive).
- `implementation-plan.md` needs no edit — it already specifies 5.
