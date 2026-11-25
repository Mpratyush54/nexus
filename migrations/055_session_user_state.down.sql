-- Local dev only (down migrations never ship in the image).
DROP INDEX IF EXISTS idx_agent_session_user_state_session;
DROP TABLE IF EXISTS agent_session_user_state;
