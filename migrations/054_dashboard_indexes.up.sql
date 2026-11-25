-- Indexes for the project dashboard (GET /projects/{id}/dashboard).
--
-- Forward-only and idempotent. Plain CREATE INDEX: migrations run one file
-- per transaction, so CONCURRENTLY is not available; each build holds a
-- SHARE lock on its table (reads continue, writes wait) until it finishes.
-- events is the largest of these. On a large database, create its index by
-- hand first without blocking writes, and this migration finds it in place:
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_events_project_time_type
--     ON events (project_id, created_at) INCLUDE (event_type, agent_id);

-- Event counts per day and type over the past year: an index-only scan
-- instead of reading every event row (payloads can be hundreds of KB).
CREATE INDEX IF NOT EXISTS idx_events_project_time_type
    ON events (project_id, created_at) INCLUDE (event_type, agent_id);

-- One person's chats in a project (agent activity calendar).
CREATE INDEX IF NOT EXISTS idx_agent_sessions_project_owner
    ON agent_sessions (project_id, owner_user_id);

-- Does this chat also exist as an archived transcript? (activity dedup)
CREATE INDEX IF NOT EXISTS idx_snapshots_project_owner_conversation
    ON session_snapshots (project_id, owner_user_id, harness, conversation_id);

-- Latest archived version of each session in a project.
CREATE INDEX IF NOT EXISTS idx_snapshots_project_session_version
    ON session_snapshots (project_id, session_id, snapshot_version DESC);
