-- Migration 016_memory_versions — edit history for memory items (issue #162).
--
-- Every PUT /memory/{id} snapshots the pre-edit state here before applying
-- the update, so GET /history and POST /revert can reconstruct prior content.
-- Forward-only step; rollback in 016_memory_versions.down.sql.
-- Idempotent: CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS memory_versions (
  id          BIGSERIAL PRIMARY KEY,
  memory_id   UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
  version     INT NOT NULL,
  key         TEXT NOT NULL,
  content     TEXT NOT NULL,
  tags        TEXT[] DEFAULT '{}',
  level       TEXT,
  scope       TEXT,
  edited_by   UUID REFERENCES users(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(memory_id, version)
);

CREATE INDEX IF NOT EXISTS idx_memory_versions_memory_id
  ON memory_versions(memory_id, version DESC);
