-- Rollback for 004_agents: drops everything the up migration creates, in
-- reverse dependency order so no FK violations occur.
-- The deferred FKs are dropped first (their columns stay: agent_id belongs
-- to 002/003 and later migrations must not remove it). project_agents dies
-- with its rows; agents dies with its seed rows (re-applying the up file
-- re-seeds). Extensions are untouched (owned by 001, shared with 002+).

ALTER TABLE events DROP CONSTRAINT IF EXISTS fk_events_agent;
ALTER TABLE session_participants DROP CONSTRAINT IF EXISTS fk_session_participants_agent;
DROP TABLE IF EXISTS project_agents;
DROP TABLE IF EXISTS agents;
