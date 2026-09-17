# ISSUE-29 — Close deferred session_id FKs (events / episodes / tasks → sessions)

- **Issue:** #29 — `events.session_id`, `episodes.session_id`,
  `tasks.session_id` have no FK (002 deferred, 003/004 never closed them)
- **Status:** Done (migration + docs; awaiting merge)
- **Assignee:** fix/audit-gofmt-49 agent
- **Scope constraint:** ONLY `migrations/006_session_fks.*`, `docs/`.
  Did NOT touch `001_*`–`005_*`, app code, or any other file.
- **Plan ref:** deferred-FK pattern from 001 (`episode_events.event_id` →
  002), 003 (`fk_memory_session`), 004 (`fk_events_agent`,
  `fk_session_participants_agent`); read `migrations/001_initial.up.sql`,
  `002_events.up.sql`, `003_sessions.up.sql` headers + tables first.

## What was built

| File | Contents |
|---|---|
| `migrations/006_session_fks.up.sql` | Three `ALTER TABLE ... ADD CONSTRAINT` FKs: `fk_events_session`, `fk_episodes_session`, `fk_tasks_session`, each `FOREIGN KEY (session_id) REFERENCES sessions(id)`, no `ON DELETE` action; header documents column-existence verification (001 lines 122/197, 002 line 15) and the skip-with-comment rule |
| `migrations/006_session_fks.down.sql` | Reverse-order rollback: `fk_tasks_session` → `fk_episodes_session` → `fk_events_session` (`DROP CONSTRAINT IF EXISTS`); columns, `sessions`, extensions untouched |
| `docs/decisions/ADR-029-session-fks.md` | Why-mandatory ADR (gap proof, options, no-cascade rationale, consequences) |
| `docs/issues/ISSUE-29.md` | This file |

## Decisions (see ADR-029 for rationale)

1. New migration 006 instead of back-editing 001–003 (forward-only
   migration hygiene).
2. All three columns verified to exist in 001/002 first — no skips needed
   (the up file records the line numbers; any absent column would have
   been skipped with a `-- SKIP` comment, not an `ALTER`).
3. No `ON DELETE` action (history outlives sessions — same as 003
   `fk_memory_session`, 004 `fk_events_agent`); NULLs stay allowed (all
   three columns nullable: cross-session episodes, session-less
   tasks/events).
4. Named constraints (`fk_*_session`) so the down migration drops them
   explicitly (same reason 005 named `fk_memory_branch`).

## Verification (structural SQL self-review — no live Postgres in this env)

- [x] `events.session_id` exists: `002_events.up.sql` line 15 (`UUID`,
      bare, nullable) — header lines 8–9 defer it pending 003/004.
- [x] `episodes.session_id` exists: `001_initial.up.sql` line 122
      (`UUID`, bare, nullable).
- [x] `tasks.session_id` exists: `001_initial.up.sql` line 197 (`UUID`,
      bare, nullable).
- [x] FK target exists: `sessions(id) UUID PRIMARY KEY` in
      `003_sessions.up.sql` lines 14–15; types match (UUID → UUID).
- [x] No 001–005 migration already adds these FKs (001: no
      `sessions`/`events` tables; 002 closes only `episode_events`;
      003 closes only `memory_items`; 004 closes only `agent_id` FKs;
      005 touches only branches) — no duplicate-constraint risk.
- [x] Up adds exactly 3 constraints; down drops exactly those 3, in
      reverse order (tasks → episodes → events), names match 1:1
      (`fk_events_session`, `fk_episodes_session`, `fk_tasks_session`),
      all drops use `IF EXISTS`.
- [x] Down preserves `session_id` columns (owned by 001/002), the
      `sessions` table (owned by 003), and extensions (owned by 001).
- [x] `git status` shows only the 4 new files (plus the pre-existing
      branch dirt in `main.go`, untouched); nothing else modified;
      NOT committed per instructions.

## Follow-ups (not this issue)

- Clean pre-existing orphan `session_id` values on deployed DBs before
  applying 006 (or the `ALTER` will fail); probe per table with
  `WHERE session_id IS NOT NULL AND session_id NOT IN (SELECT id FROM sessions)`.
- Live apply of 006 up/down against Aurora/pgvector in CI
  (`TEST_POSTGRES_DSN`); session-teardown code must clear/reassign child
  `session_id`s since deletes are now restrict-by-default.
