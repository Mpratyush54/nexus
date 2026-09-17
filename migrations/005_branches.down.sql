-- 005_branches.down.sql: Rollback copy-on-write memory branching.
-- Reverse order: branch column/index -> auto-main rows -> branch table.
-- Memory content is preserved: only the branch tag is dropped (rows stay).

DROP INDEX IF EXISTS idx_memory_branch;

ALTER TABLE memory_items DROP COLUMN IF EXISTS branch_id;

DELETE FROM memory_branches WHERE name = 'main' AND parent_branch_id IS NULL;

DROP INDEX IF EXISTS idx_branches_parent;
DROP INDEX IF EXISTS idx_branches_project;

DROP TABLE IF EXISTS memory_branches CASCADE;
