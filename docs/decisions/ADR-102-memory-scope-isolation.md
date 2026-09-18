# ADR-102 — Memory Scope Isolation: Org Tier as Global Tier, MCP Identity, Session/Project Invariant

- **ADR ID:** ADR-102-memory-scope-isolation
- **Date:** 2026-09-17
- **Author:** issue-#102 agent
- **Issue:** #102 audit: Memory scope isolation leak and invalid MCP scope combinations
- **Status:** Accepted

## Context

`SearchMemory` treated every NULL-project row as globally readable, so personal/session memories with NULL `project_id` leaked across projects; `memory_write` let callers create `personal`/`session` rows with no owner; `CreateSessionMemory` let a session of Project A adopt a memory tagged Project B. The task spec pins the intended semantics: the org tier is the global tier by design, MCP `user_id`/`session_id` are optional args with per-level requirements, and the MCP Store interface must not be widened.

## Options Considered

1. **NULL-project visible only when `level = 'organization'` (chosen).** SQL: `WHERE (project_id = $1::uuid OR (project_id IS NULL AND level = 'organization'))`, mirrored in `MemStore` Go logic. Pros: single-rule fix in both search paths; org tier remains genuinely global; no migration needed. Cons: pre-existing NULL-project personal/session rows become unsearchable (hidden, not deleted).
2. **NULL-project visible only when `scope = 'global'`.** Rejected: no `global` scope exists in the codebase (`Scope` is fact/preference/decision/constraint/pattern/episode_summary; `Level` carries the tier). The issue text's `scope='global'` does not map to the schema.
3. **MCP: widen Store with session/user lookup to verify existence (rejected).** The spec forbids widening the MCP Store interface; existence stays enforced by the session flow, and the tool description documents that.
4. **Session invariant: reject on any project mismatch, including blank item project (rejected).** Blank-project session memories are legal (org-orphan notes) and pre-date this issue; only an explicitly set, differing `ProjectID` is rejected, keeping `ErrNotFound` for unknown sessions.

## Decision

- Search scoping per option 1 in `PostgresStore.SearchMemory`, `SearchMemoryVector`, and `MemStore.SearchMemory`.
- `handleMemoryWrite`: optional `user_id`/`session_id` args; `personal` requires non-blank `user_id`, `session` requires non-blank `session_id` (400 `invalidParams`); IDs stored on the item; description notes session-flow enforcement.
- Both `CreateSessionMemory` impls fetch the session and reject `item.ProjectID`-vs-`session.ProjectID` mismatches descriptively.
- `ListSessionVisibleMemories` verified already constrained (own rows + project/org inheritance, sibling isolation) and deliberately untouched.

## Why (Rationale)

Option 1 is the only reading consistent with both the schema (`level` owns the tier axis) and the task spec's explicit SQL (`level = 'organization'`). MemStore parity keeps DB-free tests meaningful (`TestSearchMemoryNullProjectOrgOnly` would fail under the old predicate). Not widening the MCP Store preserves the package's stdlib-only seam (`server_test.go` fake needs no new methods). The blank-project carve-out avoids breaking existing org-orphan session writes while still closing the cross-project hole (`TestCreateSessionMemoryCrossProjectRejected`).

## Consequences

- Stale NULL-project personal/session rows are hidden from search, not repaired — a backfill/migration is a follow-up.
- `memory_write` clients writing `personal`/`session` levels must now supply identity args.
- Postgres `CreateSessionMemory` now performs a `GetSession` round-trip (MemStore already did the lookup).

## Alternatives Rejected

- `scope='global'` gating (option 2): no such scope value exists.
- MCP Store widening (option 3): forbidden by spec; unnecessary.
- Blank-project rejection (option 4): over-broad; breaks legal orphan writes.
