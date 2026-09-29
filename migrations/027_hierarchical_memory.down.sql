-- Migration 027_hierarchical_memory.down.sql
DROP INDEX IF EXISTS idx_memory_files_affected;
DROP INDEX IF EXISTS idx_memory_week_bucket;
DROP INDEX IF EXISTS idx_memory_supersedes_key;
DROP INDEX IF EXISTS idx_memory_category;

ALTER TABLE memory_items
  DROP COLUMN IF EXISTS category,
  DROP COLUMN IF EXISTS files_affected,
  DROP COLUMN IF EXISTS tools_used,
  DROP COLUMN IF EXISTS supersedes_key,
  DROP COLUMN IF EXISTS outcome,
  DROP COLUMN IF EXISTS week_bucket;
