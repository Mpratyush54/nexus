-- Sub-agent runs (Claude Code saves each as
-- <project>/<parent session>/subagents/agent-<hex>.jsonl) are captured as
-- their own sessions. The daemon now sends the parent's native id with them;
-- the timeline resolves it to the parent session at read time, so the
-- parent may be captured before or after its sub-agents. Older rows get it
-- the next time their daemon mirrors them; until then the timeline still
-- recognises them by their "agent-<hex>" native id.
--
-- This is a separate link from parent_session_id, which marks forks,
-- teleports and seeded chats.
ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS parent_native_id TEXT;

-- subagent_count on a listed parent: its sub-agents by the parent's key.
CREATE INDEX IF NOT EXISTS idx_agent_sessions_subagent_parent
    ON agent_sessions (project_id, harness, origin_machine_id, parent_native_id)
    WHERE parent_native_id IS NOT NULL;
