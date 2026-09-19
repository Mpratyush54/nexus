DROP INDEX IF EXISTS api_tokens_user_agent_idx;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS agent_id;
