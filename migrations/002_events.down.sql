-- Rollback for 002_events: drops everything the up migration creates, in
-- reverse dependency order so no FK violations occur.
-- The episode_events.event_id column itself is preserved (it belongs to 001;
-- only the 002-added FK constraint is dropped) so re-applying 001 stays
-- untouched and 002 can be re-applied cleanly afterwards.
-- Indexes die with the events table, but are dropped explicitly for clarity.

DROP TRIGGER IF EXISTS events_notify ON events;
DROP FUNCTION IF EXISTS notify_event();
ALTER TABLE episode_events DROP CONSTRAINT IF EXISTS fk_episode_events_event;
DROP INDEX IF EXISTS idx_events_episode;
DROP INDEX IF EXISTS idx_events_project_type;
DROP INDEX IF EXISTS idx_events_project_time;
DROP TABLE IF EXISTS events;
