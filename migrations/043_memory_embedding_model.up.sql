-- Record which model produced each memory_items.embedding so the re-embed
-- backfill (central-server reembed) can find rows whose vector came from a
-- different model (HashEmbed or an older model) and redo only those.
-- NULL = unknown/legacy (treated as stale). Additive and nullable: old
-- server images ignore the column, so rollback needs no down migration.
-- The vector(1536) column is unchanged: narrower models (bge-small, 384
-- dims) are zero-padded, which preserves cosine similarity exactly.
ALTER TABLE memory_items ADD COLUMN IF NOT EXISTS embedding_model text;
