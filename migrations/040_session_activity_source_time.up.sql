-- A session's last_active_at is when its conversation last moved, not when
-- Nexus uploaded it. Capture used to stamp every imported (old) chat with the
-- upload time, so history imports jumped to the top of everyone's chat list.
-- Repair from the turns' source time; sessions without one are left alone.
UPDATE agent_sessions s
   SET last_active_at = t.latest
  FROM (SELECT session_id, MAX(source_ts) AS latest
          FROM session_turns
         WHERE source_ts IS NOT NULL
         GROUP BY session_id) t
 WHERE s.id = t.session_id
   AND s.last_active_at IS DISTINCT FROM t.latest;
