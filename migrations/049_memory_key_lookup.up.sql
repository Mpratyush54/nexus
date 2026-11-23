-- Exact-key memory lookups (FindMemoryByKey): the project brief
-- (key project/brief, read on every MCP initialize and project_knowledge
-- call) and harvest session summaries (key session/<id>). They used a
-- substring search over every row's content; this index answers them
-- directly.
CREATE INDEX IF NOT EXISTS idx_memory_project_key
    ON memory_items (project_id, "key")
    WHERE superseded_by IS NULL;
