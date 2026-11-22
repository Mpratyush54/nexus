-- 047_code_changes.up.sql
-- Line-level change history. One row is one hunk of an agent's file edit
-- (an Edit/MultiEdit/Write/apply_patch tool call captured by the desktop):
-- the changed line range in the file right after the edit, short
-- before/after snippets (at most 3 lines each, never whole files), and the
-- file's content hash when the desktop read it. Line numbers are in the
-- coordinates of that read (observed_at); later hunks shift earlier ranges
-- at query time. (045 and 046 are taken by other branches.)

CREATE TABLE IF NOT EXISTS code_changes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    entity_id UUID NOT NULL REFERENCES code_entities(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL,
    hunk_index INT NOT NULL DEFAULT 0 CHECK (hunk_index >= 0),
    start_line INT NOT NULL CHECK (start_line >= 1),
    end_line INT NOT NULL,
    removed_lines INT NOT NULL DEFAULT 0 CHECK (removed_lines >= 0),
    added_lines INT NOT NULL DEFAULT 0 CHECK (added_lines >= 0),
    before_snippet TEXT NOT NULL DEFAULT '',
    after_snippet TEXT NOT NULL DEFAULT '',
    after_hash TEXT,
    changed_at TIMESTAMPTZ NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (end_line >= start_line),
    UNIQUE (entity_id, session_id, tool_call_id, hunk_index)
);

CREATE INDEX IF NOT EXISTS idx_code_changes_entity_observed
    ON code_changes (entity_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_code_changes_session
    ON code_changes (project_id, session_id);
