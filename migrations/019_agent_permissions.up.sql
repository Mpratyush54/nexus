-- Migration 019_agent_permissions — per-agent MCP access control (issue #166).
--
-- Modes: read_only | propose_only | full | blocked.
-- rate_limit is calls/minute (0 = unlimited).
-- tool_name='*' holds agent-level mode/rate; other rows are per-tool allow flags.
-- Idempotent: CREATE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS agent_permissions (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    agent_id    TEXT NOT NULL,
    tool_name   TEXT NOT NULL DEFAULT '*',
    allowed     BOOLEAN NOT NULL DEFAULT true,
    mode        TEXT NOT NULL DEFAULT 'full'
                CHECK (mode IN ('read_only', 'propose_only', 'full', 'blocked')),
    rate_limit  INTEGER NOT NULL DEFAULT 60 CHECK (rate_limit >= 0),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, agent_id, tool_name)
);

CREATE INDEX IF NOT EXISTS idx_agent_permissions_project
    ON agent_permissions(project_id);
