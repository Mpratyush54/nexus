-- Rollback for 009_ephemeral_level: restores the 4-tier level CHECK.
-- Fails while ephemeral rows exist — re-level or delete them first.

ALTER TABLE memory_items
    DROP CONSTRAINT IF EXISTS memory_items_level_check;

ALTER TABLE memory_items
    ADD CONSTRAINT memory_items_level_check
    CHECK (level IN ('organization', 'project', 'personal', 'session'));
