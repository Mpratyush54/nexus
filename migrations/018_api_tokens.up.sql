-- API tokens for user/agent scoped keys with revocation (issue #161).
CREATE TABLE IF NOT EXISTS api_tokens (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_prefix TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  scopes       TEXT[] NOT NULL DEFAULT '{}',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS api_tokens_user_id_idx ON api_tokens (user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS api_tokens_prefix_idx ON api_tokens (token_prefix) WHERE revoked_at IS NULL;
