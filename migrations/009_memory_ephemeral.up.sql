-- Migration 009_memory_ephemeral — 5th memory tier 'ephemeral' (issue #30).
-- Source of truth: the plan promises 5 tiers
-- (organization|project|personal|session|ephemeral); 001 enforced 4.
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 001_initial (memory_items.level CHECK).
-- Forward-only step 9 of N; rollback in 009_memory_ephemeral.down.sql.
--
-- The 001 level CHECK is an inline (auto-named) constraint, so it is
-- dropped by name and re-added with the 5-tier set. Both statements are
-- guarded for idempotency (issue #107): re-runs are safe no-ops.
-- Existing rows are unaffected (the new set is a superset of the old).

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'memory_items_level_check'
    ) THEN
        ALTER TABLE memory_items DROP CONSTRAINT memory_items_level_check;
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'memory_items_level_check'
    ) THEN
        ALTER TABLE memory_items
            ADD CONSTRAINT memory_items_level_check
            CHECK (level IN ('organization', 'project', 'personal', 'session', 'ephemeral'));
    END IF;
END
$$;
