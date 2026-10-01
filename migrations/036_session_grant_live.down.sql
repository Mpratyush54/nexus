ALTER TABLE session_grants
    DROP CONSTRAINT IF EXISTS session_grants_pit_needs_version;

ALTER TABLE session_grants
    DROP COLUMN IF EXISTS live;
