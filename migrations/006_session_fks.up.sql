-- Migration 006_session_fks — close deferred session_id FKs left bare by 001/002.
-- Source of truth: issue #29 (events.session_id, episodes.session_id,
-- tasks.session_id have no FK — 002 deferred, 003/004 never closed them).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 001_initial + 002_events + 003_sessions:
-- sessions(id) comes from 003; the three child columns were verified to
-- exist before writing this file (see below) so no skips were needed.
-- Forward-only step 6 of N; rollback in 006_session_fks.down.sql.
--
-- Column-existence verification (001 first, then 002):
-- - events.session_id UUID (bare, nullable) — 002_events.up.sql line 15;
--   header lines 8-9 defer it pending 003/004 ("their parent tables
--   (sessions, agents) land in migrations 003/004").
-- - episodes.session_id UUID (bare, nullable) — 001_initial.up.sql line 122
--   ("may span sessions").
-- - tasks.session_id UUID (bare, nullable) — 001_initial.up.sql line 197.
-- All three exist, so all three FKs are added. (Had any column been absent,
-- it would have been skipped here with a "-- SKIP: <table>.<column> absent"
-- comment instead of an ALTER.)
--
-- No ON DELETE action (matches 003 fk_memory_session and 004 fk_events_agent):
-- event log, episode arcs, and tasks outlive their session; session deletes
-- must not cascade into history. NULLs stay allowed (all three columns are
-- nullable: cross-session episodes, session-less tasks/events).
--
-- Idempotency (issue #107): each ALTER is guarded by a pg_constraint
-- existence check, so re-runs and partial runs are safe no-ops instead of
-- fatal "constraint already exists" crashes.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_events_session'
    ) THEN
        ALTER TABLE events
            ADD CONSTRAINT fk_events_session
            FOREIGN KEY (session_id) REFERENCES sessions(id);
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_episodes_session'
    ) THEN
        ALTER TABLE episodes
            ADD CONSTRAINT fk_episodes_session
            FOREIGN KEY (session_id) REFERENCES sessions(id);
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_tasks_session'
    ) THEN
        ALTER TABLE tasks
            ADD CONSTRAINT fk_tasks_session
            FOREIGN KEY (session_id) REFERENCES sessions(id);
    END IF;
END
$$;
