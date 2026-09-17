-- Rollback for 006_session_fks: drops everything the up migration creates,
-- in reverse dependency order so no FK violations occur.
-- Each DROP mirrors one up-migration ALTER in reverse order
-- (tasks → episodes → events). The session_id columns themselves are
-- preserved (they belong to 001/002; only the 006-added FK constraints are
-- dropped) so re-applying 001/002 stays untouched and 006 can be re-applied
-- cleanly afterwards. sessions table is untouched (owned by 003).
-- Extensions are untouched (owned by 001, shared with 002+).

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS fk_tasks_session;
ALTER TABLE episodes DROP CONSTRAINT IF EXISTS fk_episodes_session;
ALTER TABLE events DROP CONSTRAINT IF EXISTS fk_events_session;
