-- 003_sessions.down.sql: Rollback the session layer.
-- Reverse order: scoping FKs/indexes -> participants -> sessions.

ALTER TABLE memory_items DROP CONSTRAINT IF EXISTS fk_memory_session;
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS fk_task_session;

DROP INDEX IF EXISTS idx_memory_session;
DROP INDEX IF EXISTS idx_tasks_session;

DROP TABLE IF EXISTS session_participants CASCADE;
DROP TABLE IF EXISTS sessions CASCADE;
