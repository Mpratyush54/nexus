-- 036_session_grant_live: D15 live vs point-in-time shares.
-- live=true covers current and future complete versions.
-- live=false pins the grant to version_id (required when not live).
-- Team shares are always live (enforced in the API).

ALTER TABLE session_grants
    ADD COLUMN IF NOT EXISTS live BOOLEAN NOT NULL DEFAULT true;

-- Point-in-time grants must name a version.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'session_grants_pit_needs_version'
    ) THEN
        ALTER TABLE session_grants
            ADD CONSTRAINT session_grants_pit_needs_version
            CHECK (live OR version_id IS NOT NULL);
    END IF;
END $$;
