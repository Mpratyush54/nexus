-- Local dev only (down migrations never ship in the image): put the archived
-- empty sessions back. Rows whose project or owner is gone stay archived.
INSERT INTO agent_sessions
SELECT r.*
  FROM agent_sessions_cleanup_046 c,
       jsonb_populate_record(NULL::agent_sessions, c.row_data) r
 WHERE EXISTS (SELECT 1 FROM projects p WHERE p.id = r.project_id)
   AND (r.owner_user_id IS NULL OR EXISTS (SELECT 1 FROM users u WHERE u.id = r.owner_user_id))
   AND (r.parent_session_id IS NULL OR EXISTS (SELECT 1 FROM agent_sessions a WHERE a.id = r.parent_session_id))
ON CONFLICT DO NOTHING;
DROP TABLE IF EXISTS agent_sessions_cleanup_046;
