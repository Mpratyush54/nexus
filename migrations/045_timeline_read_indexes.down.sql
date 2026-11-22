-- Local dev only (down migrations never ship in the image). pg_trgm stays.
DROP INDEX IF EXISTS idx_session_turns_text_trgm;
DROP INDEX IF EXISTS idx_session_grants_grantee;
