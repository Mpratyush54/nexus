-- Rollback for 009_memory_ephemeral: restore the 4-tier level CHECK.
-- Rows already stored with level='ephemeral' block the restore (CHECK
-- violation); re-tier or delete them first. Guarded for safe re-runs.

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
            CHECK (level IN ('organization', 'project', 'personal', 'session'));
    END IF;
END
$$;
