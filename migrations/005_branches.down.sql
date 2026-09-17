-- Rollback for 005_branches: drops everything the up migration creates,
-- in reverse dependency order so no FK violations occur.
-- The memory_items.branch_id COLUMN is dropped (it belongs to 005, unlike
-- 003 where the session_id column belonged to 001 and was preserved).
-- Indexes on memory_branches die with their table (no separate DROP INDEX
-- needed); idx_memory_branch lives on memory_items so it is dropped
-- explicitly. Extensions are untouched (owned by 001, shared with 002+).

DROP INDEX IF EXISTS idx_memory_branch;
ALTER TABLE memory_items DROP CONSTRAINT IF EXISTS fk_memory_branch;
ALTER TABLE memory_items DROP COLUMN IF EXISTS branch_id;
DROP TABLE IF EXISTS memory_branches;
