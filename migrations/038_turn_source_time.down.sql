DROP INDEX IF EXISTS idx_session_turns_source_time;
ALTER TABLE session_turns DROP COLUMN IF EXISTS source_ts;
