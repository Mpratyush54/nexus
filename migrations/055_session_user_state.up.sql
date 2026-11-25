-- Per-person chat state: a custom title (rename), pinned and archived.
--
-- Kept apart from agent_sessions because capture upserts overwrite
-- agent_sessions.title, and because the state belongs to the viewer, not
-- the chat: a chat shared with someone can be pinned or renamed by them
-- without changing it for its owner. session_id has no foreign key so
-- archived snapshot chats (session_snapshots, no agent_sessions row) can be
-- pinned too; deleting a chat removes its rows (DeleteAgentSession).
--
-- Forward-only and idempotent.
CREATE TABLE IF NOT EXISTS agent_session_user_state (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id  TEXT NOT NULL,
    title       TEXT,
    pinned_at   TIMESTAMPTZ,
    archived_at TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, session_id)
);

-- Deleting a chat clears everyone's state for it.
CREATE INDEX IF NOT EXISTS idx_agent_session_user_state_session
    ON agent_session_user_state (session_id);
