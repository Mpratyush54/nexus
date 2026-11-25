-- Local dev only (down migrations never ship in the image).
DROP INDEX IF EXISTS idx_agent_sessions_subagent_parent;
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS parent_native_id;
