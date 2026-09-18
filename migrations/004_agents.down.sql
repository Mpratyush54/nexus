-- 004_agents.down.sql: Rollback the multi-agent registry.
-- Reverse order: dependent table first, then seeds, then base table.
--
-- Non-destructive scoping (issue #119): only enablement rows pointing at
-- the seven seeded agents are removed. Enablement rows for custom agents
-- (added post-deploy via UpsertAgent) are preserved when only the seed
-- list is rolled back. Dropping the tables below still removes everything;
-- prefer seed-only rollback (the two DELETEs) when custom agents exist.

DELETE FROM project_agents WHERE agent_id IN (
    SELECT id FROM agents WHERE name IN
        ('claude', 'opencode', 'codex', 'antigravity', 'copilot', 'cursor', 'windsurf')
);

DELETE FROM agents WHERE name IN
    ('claude', 'opencode', 'codex', 'antigravity', 'copilot', 'cursor', 'windsurf');

DROP TABLE IF EXISTS project_agents;
DROP TABLE IF EXISTS agents;
