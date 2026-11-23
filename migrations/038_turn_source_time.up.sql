-- A nullable source timestamp is deliberately distinct from receipt time.
-- Never backfill this with ts: older clients wrote upload time into ts.
ALTER TABLE session_turns ADD COLUMN IF NOT EXISTS source_ts TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_session_turns_source_time
    ON session_turns (session_id, source_ts) WHERE source_ts IS NOT NULL;
