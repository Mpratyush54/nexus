-- 004_agents.down.sql: Rollback the multi-agent registry.
-- Reverse order: dependent table first, then seeds, then base table.

DELETE FROM project_agents;

DELETE FROM agents WHERE name IN
    ('claude', 'opencode', 'codex', 'antigravity', 'copilot', 'cursor', 'windsurf');

DROP TABLE IF EXISTS project_agents;
DROP TABLE IF EXISTS agents;
