-- What the person's desktop agents are doing in each chat, published by
-- Nexus Desktop so the web chat list shows the same status dots as the
-- desktop sidebar: running, waiting for approval, a reply not yet read,
-- stopped with a problem.
--
-- One row per person and chat. status_at is when the status last changed,
-- read_at when the person last opened the chat on the web: a desktop that
-- still reports an unread reply older than read_at learns it was read.
-- Rows for idle, read chats are removed. session_id has no foreign key: a
-- desktop can report a chat before its first turn reaches the server.
--
-- Forward-only and idempotent.
CREATE TABLE IF NOT EXISTS agent_session_live_status (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    status     TEXT NOT NULL,
    unread     BOOLEAN NOT NULL DEFAULT false,
    status_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at    TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, session_id)
);
