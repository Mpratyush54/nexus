-- 044_code_links.up.sql
-- Memories linked to code. code_entities are the files (and, later,
-- symbols) of a project, keyed by their repo-relative path as the desktop
-- reports it (forward slashes). entity_links tie an entity to a memory or a
-- cloud session: 'touched' (an agent edited it, from tool calls),
-- 'decided_about' (a memory's files_affected), 'mentions'. observed_hash is
-- the file's content hash when the link was made; when the desktop later
-- reports a different hash, linked CONFIRMED memories are marked stale.

CREATE TABLE IF NOT EXISTS code_entities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'symbol')),
    path TEXT NOT NULL,
    symbol TEXT,
    content_hash TEXT,
    last_seen_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_code_entities_identity
    ON code_entities (project_id, kind, path, (COALESCE(symbol, '')));
CREATE INDEX IF NOT EXISTS idx_code_entities_project_lower_path
    ON code_entities (project_id, lower(path));

CREATE TABLE IF NOT EXISTS entity_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    entity_id UUID NOT NULL REFERENCES code_entities(id) ON DELETE CASCADE,
    src_kind TEXT NOT NULL CHECK (src_kind IN ('memory', 'session')),
    src_id TEXT NOT NULL,
    relation TEXT NOT NULL CHECK (relation IN ('touched', 'decided_about', 'mentions')),
    source TEXT NOT NULL CHECK (source IN ('tool_call', 'llm')),
    confidence REAL NOT NULL DEFAULT 1,
    observed_hash TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entity_id, src_kind, src_id, relation)
);

CREATE INDEX IF NOT EXISTS idx_entity_links_src
    ON entity_links (project_id, src_kind, src_id);
CREATE INDEX IF NOT EXISTS idx_entity_links_entity_created
    ON entity_links (entity_id, created_at DESC);
