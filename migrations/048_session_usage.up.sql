-- 048_session_usage.up.sql
-- Token usage per model call or turn, for chats captured from agent
-- transcripts and chats run live in Nexus. A separate table rather than
-- columns on session_turns: usage often arrives on lines that are not
-- dialogue (Codex token_count, tool-only Claude lines), and one API call's
-- usage repeats across several lines, so rows are keyed by the agent's own
-- identity for the call (usage_key), not by turn.
-- cost_usd is NULL unless the agent reported a cost; estimates are computed
-- at read time from the server's price table and never stored.

CREATE TABLE IF NOT EXISTS session_usage (
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    usage_key TEXT NOT NULL,
    turn_idx BIGINT,
    model TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_read_tokens BIGINT NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens BIGINT NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cost_usd DOUBLE PRECISION CHECK (cost_usd IS NULL OR cost_usd >= 0),
    source_ts TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, usage_key)
);

-- Summaries filter by when the call happened (source time, else receipt).
CREATE INDEX IF NOT EXISTS idx_session_usage_at
    ON session_usage ((COALESCE(source_ts, created_at)));
