-- Local dev only (down migrations never ship in the image).
DROP INDEX IF EXISTS idx_snapshots_project_session_version;
DROP INDEX IF EXISTS idx_snapshots_project_owner_conversation;
DROP INDEX IF EXISTS idx_agent_sessions_project_owner;
DROP INDEX IF EXISTS idx_events_project_time_type;
