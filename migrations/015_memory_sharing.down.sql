-- Rollback for 015_memory_sharing: drops share grants and visibility column.
-- WARNING: restores pre-015 behavior (all members see all project memories).

DROP TABLE IF EXISTS memory_shares;

DROP INDEX IF EXISTS idx_memory_visibility;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'memory_items_visibility_check'
    ) THEN
        ALTER TABLE memory_items DROP CONSTRAINT memory_items_visibility_check;
    END IF;
END
$$;

ALTER TABLE memory_items DROP COLUMN IF EXISTS visibility;
