-- 028_session_provenance.up.sql
-- Granular file/tool provenance + versioned session snapshots (Nexus Teleport).

CREATE TYPE file_op_type AS ENUM ('read', 'create', 'modify', 'delete', 'rename');

CREATE TABLE IF NOT EXISTS session_file_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    turn_index INT,                          -- correlate to conversation turn ([Audit Fix D1])
    harness TEXT NOT NULL,                   -- 'antigravity', 'cursor', 'copilot', 'gemini'
    tool_name TEXT NOT NULL,                 -- 'replace_file_content', 'write_to_file', 'view_file'
    file_path TEXT NOT NULL,                 -- repo-relative path e.g. 'internal/store/memory.go'
    op_type file_op_type NOT NULL,           -- 'read', 'modify', 'create', 'delete', 'rename'
    line_start INT,
    line_end INT,
    diff_hunk TEXT,                          -- unified diff snippet (capped at 8KB in Go) ([Audit Fix D3])
    content_hash TEXT,                       -- SHA256 of target content after edit
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS session_tool_executions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    harness TEXT NOT NULL,
    tool_name TEXT NOT NULL,                 -- 'run_command', 'bash', 'terminal'
    command_line TEXT NOT NULL,
    working_directory TEXT,
    exit_code INT,
    output_snippet TEXT,                     -- capped at 4KB, tail-truncated ([Audit Fix D2])
    truncated BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- TODO: application-level encryption deferred to security hardening phase ([Audit Fix D6])
CREATE TABLE IF NOT EXISTS session_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL,
    snapshot_version INT NOT NULL DEFAULT 1,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    harness TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    turn_count INT NOT NULL DEFAULT 0,
    git_branch TEXT,
    git_commit TEXT,
    git_dirty BOOLEAN DEFAULT false,
    uncommitted_diff BYTEA,                  -- gzip-compressed diff ([Audit Fix C4])
    diff_size_bytes INT,
    diff_truncated BOOLEAN NOT NULL DEFAULT false,
    transcript_payload BYTEA NOT NULL,      -- gzip-compressed JSON (not raw JSONB)
    artifacts_bundle BYTEA,                  -- compressed .tar.gz of brain artifacts
    source_machine_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, snapshot_version)
);

CREATE INDEX IF NOT EXISTS idx_file_ops_session ON session_file_operations(session_id, turn_index);
CREATE INDEX IF NOT EXISTS idx_file_ops_path ON session_file_operations(file_path);
CREATE INDEX IF NOT EXISTS idx_tool_exec_session ON session_tool_executions(session_id);
CREATE INDEX IF NOT EXISTS idx_snapshots_session ON session_snapshots(session_id, snapshot_version DESC);
CREATE INDEX IF NOT EXISTS idx_snapshots_project ON session_snapshots(project_id, created_at DESC);

-- Retention: keep only the latest 5 snapshots per session via store.PruneOldSnapshots
-- (cron / maintenance job every 6h). Weekly VACUUM on session_snapshots reclaim dead tuples.
