# ADR-029 — Session FKs (events / episodes / tasks → sessions)

- **ADR ID:** ADR-029-session-fks
- **Date:** 2026-09-17
- **Author:** fix/audit-gofmt-49 agent
- **Issue:** #29 Close deferred `session_id` FKs (`migrations/006_session_fks.*`)
- **Status:** Accepted

## Context

Three `session_id` columns exist as bare UUIDs with no referential integrity:

1. `events.session_id` — `migrations/002_events.up.sql` line 15. The 002
   header (lines 8–9) explicitly defers it: "session_id/agent_id stay plain
   UUID here: their parent tables (sessions, agents) land in migrations
   003/004."
2. `episodes.session_id` — `migrations/001_initial.up.sql` line 122
   ("may span sessions").
3. `tasks.session_id` — `migrations/001_initial.up.sql` line 197.

004 closed only the `agent_id` FKs (`session_participants.agent_id`,
`events.agent_id`); 003 closed only `memory_items.session_id`
(`fk_memory_session`). No migration 001–005 adds any of the three
session FKs, so orphaned `session_id` values are currently possible.
(`memory_items.session_id` is already enforced by 003 and is out of scope.)

Constraints: pure-DDL migration only (Aurora Serverless v2 Postgres +
pgvector, cross-platform, no filesystem paths); touch nothing except new
migration files + docs (no edits to 001–005, no app-code changes).

## Options Considered

1. **(Chosen) New migration 006 adding three named FKs, no `ON DELETE`
   action.** `fk_events_session`, `fk_episodes_session`,
   `fk_tasks_session`, each `FOREIGN KEY (session_id) REFERENCES
   sessions(id)`; down migration drops them in reverse order with
   `IF EXISTS`, preserving the columns.
   Pros: matches the established deferred-FK pattern (001
   `episode_events.event_id` → 002, 003 `agent_id` → 004, 003
   `fk_memory_session`); named constraints keep the down migration
   explicit (same reason 005 named `fk_memory_branch`); no-cascade keeps
   history alive. Cons: none material — strictly adds enforcement.
2. **Back-edit 001/002/003 to add the FKs inline.**
   Pros: fewer files. Cons: rewrites applied-migration history (breaks
   forward-only chain, invalidates existing Aurora deployments);
   rejected on migration hygiene alone.
3. **Add FKs with `ON DELETE CASCADE` / `SET NULL`.**
   Pros: automatic cleanup. Cons: events are an append-only log and
   episodes/tasks are long-lived arcs — session deletes must not destroy
   history (same reasoning as 003's no-cascade `fk_memory_session` and
   004's no-cascade `fk_events_agent`); all three columns are nullable
   already, so `SET NULL` adds nothing. Rejected.

## Decision

- `migrations/006_session_fks.up.sql`: three `ALTER TABLE ... ADD
  CONSTRAINT` statements (`events`, `episodes`, `tasks`), each verified
  against 001/002 before writing (all three columns exist, so no skips;
  the file documents the verification with line numbers and states the
  skip-with-comment rule). No `ON DELETE` action; NULLs allowed.
- `migrations/006_session_fks.down.sql`: three `DROP CONSTRAINT IF
  EXISTS` statements in exact reverse order (tasks → episodes → events);
  columns, `sessions` table, and extensions untouched.
- Scope kept to the three session FKs per the issue spec; no 001–005
  edits, no app-code changes.

## Why (Rationale)

- **The gap is real, verified by reading the headers + tables first:**
  001 has no `sessions`/`events` tables (so the FKs could not exist
  there); 002's header defers `session_id` and its body closes only
  `episode_events.event_id`; 003's body closes only `memory_items`; 004
  closes only `agent_id` FKs; 005 touches only branches. Nothing 001–005
  references `sessions(id)` from these three columns.
- **FK targets and columns resolve:** child columns exist (001 lines
  122/197, 002 line 15, all nullable UUID); parent `sessions(id) UUID
  PRIMARY KEY` exists (003 lines 14–15). Types match (UUID → UUID).
- **Pattern consistency:** named constraints + reverse-order down file
  mirror 003 (`fk_memory_session`) and 004 (`fk_events_agent`,
  `fk_session_participants_agent`); the down file mirrors the up file
  1:1 (self-review checklist in `docs/issues/ISSUE-29.md`).
- **No-cascade is forced by history semantics:** the event log and
  episode/task arcs outlive sessions, exactly as 003/004 document for
  memories and agent history.

## Consequences

- Orphaned `session_id` values are rejected going forward; pre-existing
  orphans (if any in deployed DBs) must be cleaned before applying 006
  or the `ALTER` will fail — operators should probe with
  `SELECT ... WHERE session_id IS NOT NULL AND session_id NOT IN
  (SELECT id FROM sessions)` per table first.
- `sessions` deletes are now blocked while child rows reference them
  (restrict-by-default); session-teardown code must clear/reassign child
  `session_id`s or delete children first.
- Follow-up (not this issue): application-layer join validation /
  integration test applying 006 up/down against live Postgres
  (`TEST_POSTGRES_DSN`); no live Postgres in this env, so verification
  here is structural SQL review only.

## Alternatives Rejected

- Back-editing 001–003 (option 2): rewrites migration history.
- Cascading / `SET NULL` deletes (option 3): destroys or rewrites
  append-only history on session delete.
