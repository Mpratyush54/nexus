-- Migration 027_hierarchical_memory.up.sql: Structured hierarchical memory and state reconciliation
ALTER TABLE memory_items
  ADD COLUMN IF NOT EXISTS category TEXT DEFAULT 'general',
  ADD COLUMN IF NOT EXISTS files_affected TEXT[] DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS tools_used TEXT[] DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS supersedes_key TEXT,
  ADD COLUMN IF NOT EXISTS outcome TEXT DEFAULT 'active',
  ADD COLUMN IF NOT EXISTS week_bucket TEXT;

CREATE INDEX IF NOT EXISTS idx_memory_category ON memory_items(project_id, category);
CREATE INDEX IF NOT EXISTS idx_memory_supersedes_key ON memory_items(project_id, supersedes_key) WHERE supersedes_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memory_week_bucket ON memory_items(project_id, week_bucket);
CREATE INDEX IF NOT EXISTS idx_memory_files_affected ON memory_items USING GIN(files_affected);
