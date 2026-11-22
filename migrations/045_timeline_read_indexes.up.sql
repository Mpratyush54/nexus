-- Indexes for the chat list (GET /v1/timeline) and chat page reads.
--
-- Plain CREATE INDEX: migrations run one file per transaction, so
-- CONCURRENTLY is not available. Each build holds a SHARE lock on its table
-- (reads continue, writes wait) until it finishes. session_grants is small.
-- The trigram index on session_turns is the long one (measured ~2.7s per
-- 60k turns locally; turn uploads wait meanwhile). On a large database,
-- create it by hand first, without blocking writes:
--   CREATE EXTENSION IF NOT EXISTS pg_trgm;
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_turns_text_trgm
--     ON session_turns USING gin (LOWER(text_preview) gin_trgm_ops);
-- and this migration then finds it in place.

-- Grants a viewer holds: timeline and read checks look them up by grantee.
CREATE INDEX IF NOT EXISTS idx_session_grants_grantee
    ON session_grants (grantee_user_id, session_id)
    WHERE revoked_at IS NULL AND grantee_user_id IS NOT NULL;

-- Timeline text search (q): ListTimeline tests turns with
-- LOWER(text_preview) LIKE '%q%', which this index answers for q of three or
-- more characters. pg_trgm ships with PostgreSQL contrib and is a trusted
-- extension (PG13+), but a server without it must still boot: then search
-- keeps scanning turns as before.
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_trgm unavailable (%); timeline search will scan turns', SQLERRM;
END
$$;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_session_turns_text_trgm
                 ON session_turns USING gin (LOWER(text_preview) gin_trgm_ops)';
    END IF;
END
$$;
