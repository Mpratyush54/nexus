-- 029_session_acl.down.sql
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS session_content_grants;
DROP TABLE IF EXISTS session_content_owners;
DROP INDEX IF EXISTS idx_snapshots_owner;
ALTER TABLE session_snapshots DROP COLUMN IF EXISTS owner_user_id;
