-- Rollback for 007_branch_upkeep: drops exactly the two columns the up
-- migration adds, in reverse order. memory_branches itself (005) and
-- memory_items are untouched.

ALTER TABLE memory_branches DROP COLUMN IF EXISTS potentially_stale;
ALTER TABLE memory_branches DROP COLUMN IF EXISTS archived_at;
