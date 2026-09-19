-- 024_api_token_agent: bind MCP API tokens to a project agent identity.

ALTER TABLE api_tokens
    ADD COLUMN IF NOT EXISTS agent_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS api_tokens_user_agent_idx
    ON api_tokens (user_id, agent_id)
    WHERE revoked_at IS NULL AND agent_id <> '';
