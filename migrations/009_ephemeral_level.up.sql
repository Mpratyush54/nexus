-- Migration 009_ephemeral_level — widen the memory_items level CHECK.
--
-- Issue #30: the plan promises 5 lifetime tiers (Organization → Project →
-- Personal → Session → Ephemeral) but 001 shipped a 4-value CHECK. This
-- migration adds 'ephemeral' (shortest lifetime: working memory that must
-- never outlive its session) without touching any other constraint.
--
-- The 001 CHECK is inline (auto-named memory_items_level_check by
-- Postgres), so widen = drop + re-add under the same name. Version-tracked
-- (schema_migrations), forward-only; rollback restores the 4-tier CHECK
-- (fails if ephemeral rows exist — delete/re-level them first).

ALTER TABLE memory_items
    DROP CONSTRAINT IF EXISTS memory_items_level_check;

ALTER TABLE memory_items
    ADD CONSTRAINT memory_items_level_check
    CHECK (level IN ('organization', 'project', 'personal', 'session', 'ephemeral'));
