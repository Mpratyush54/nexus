-- Rollback for 003_sessions: drops everything the up migration creates,
-- in reverse dependency order so no FK violations occur.
-- The memory_items FK is dropped first (its column stays: it belongs to
-- 001 and later migrations must not remove it). Indexes on sessions /
-- session_participants die with their tables (no separate DROP INDEX
-- needed); idx_memory_session lives on memory_items so it is dropped
-- explicitly. Extensions are untouched (owned by 001, shared with 002+).

ALTER TABLE memory_items DROP CONSTRAINT IF EXISTS fk_memory_session;
DROP INDEX IF EXISTS idx_memory_session;
DROP TABLE IF EXISTS session_participants;
DROP TABLE IF EXISTS sessions;
