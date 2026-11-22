-- One-off cleanup of empty chats created by a desktop capture bug: the
-- daemon used a transcript file's base name as the conversation id, so
-- multi-conversation stores (state.vscdb, opencode.db, ai-code-tracking.db)
-- and Antigravity/Gemini brain logs (transcript*.jsonl, whose id is the
-- parent folder) became chats named "state", "opencode", "transcript"...
-- The desktop no longer does this.
--
-- Conservative: a row is removed only when ALL hold
--   * native_id is one of those file-name stems,
--   * it has no turns, versions, grants, guest links, teleports or forks,
--   * no memory, provenance row or snapshot references it.
-- Such a row holds nothing but its own columns, which are archived in
-- agent_sessions_cleanup_046 first (046 down restores them), so the delete
-- is reversible. Idempotent: a rerun finds nothing left to remove.

CREATE TABLE IF NOT EXISTS agent_sessions_cleanup_046 (
    id UUID PRIMARY KEY,
    row_data JSONB NOT NULL,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

WITH doomed AS (
    SELECT s.*
      FROM agent_sessions s
     WHERE s.native_id IN ('state', 'opencode', 'ai-code-tracking',
                           'transcript', 'transcript_full', 'harvest-smoke-test')
       AND NOT EXISTS (SELECT 1 FROM session_turns t WHERE t.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM session_versions v WHERE v.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM session_grants g WHERE g.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM guest_links l WHERE l.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM teleports tp WHERE tp.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM agent_sessions c WHERE c.parent_session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM memory_items m WHERE m.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM session_file_operations f WHERE f.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM session_tool_executions e WHERE e.session_id = s.id)
       AND NOT EXISTS (SELECT 1 FROM session_snapshots ss WHERE ss.session_id = s.id)
), archived AS (
    INSERT INTO agent_sessions_cleanup_046 (id, row_data)
    SELECT d.id, to_jsonb(d) FROM doomed d
    ON CONFLICT (id) DO NOTHING
    RETURNING id
)
DELETE FROM agent_sessions s
 USING archived a
 WHERE s.id = a.id;
