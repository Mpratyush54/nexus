-- Migration 010_session_expiry — per-session TTL for the expiry sweeper
-- (issue #119, plan §2.7 grace model).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 003_sessions (sessions table).
-- Forward-only step 10 of N; rollback in 010_session_expiry.down.sql.
--
-- expires_at is NULL by default: the sweeper (ExpireStaleSessions) falls
-- back to created_at + DefaultSessionTTL (7d) for such rows. The partial
-- index keeps the sweeper's scan cheap. All statements are idempotent
-- (issue #107): re-runs are safe no-ops.

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at)
    WHERE is_active AND expires_at IS NOT NULL;
