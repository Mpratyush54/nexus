-- Rollback for 010_session_expiry: drop the sweeper index and column.
-- Ending sessions already swept stays ended (is_active/ended_at are owned
-- by 003); only the TTL override is removed. Guarded for safe re-runs.

DROP INDEX IF EXISTS idx_sessions_expiry;

ALTER TABLE sessions DROP COLUMN IF EXISTS expires_at;
