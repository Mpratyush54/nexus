-- 001_initial.up.sql: Core schema for multiplayer agent memory platform
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username    TEXT UNIQUE NOT NULL,
    email       TEXT UNIQUE,
    settings    JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- PROJECTS
-- ============================================================
CREATE TABLE IF NOT EXISTS projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_url   TEXT,
    root_commit     TEXT,
    folder_name     TEXT NOT NULL,
    display_name    TEXT,
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(canonical_url),
    UNIQUE(root_commit)
);

-- ============================================================
-- WORKSPACES
-- ============================================================
CREATE TABLE IF NOT EXISTS workspaces (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id              UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id                 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    machine_id              TEXT NOT NULL,
    path                    TEXT NOT NULL,
    branch                  TEXT,
    commit_sha              TEXT,
    is_dirty                BOOLEAN NOT NULL DEFAULT false,
    is_online               BOOLEAN NOT NULL DEFAULT false,
    is_designated_processor BOOLEAN NOT NULL DEFAULT false,
    last_seen               TIMESTAMPTZ,
    daemon_url              TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(machine_id, path)
);

-- ============================================================
-- MEMORY ITEMS (5-tier hierarchy, pgvector indexed)
-- ============================================================
CREATE TABLE IF NOT EXISTS memory_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID REFERENCES projects(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    session_id      UUID,
    org_id          UUID,
    key             TEXT NOT NULL,
    content         TEXT NOT NULL CHECK (length(content) >= 20 AND length(content) <= 2000),
    context_snippet TEXT,
    level           TEXT NOT NULL DEFAULT 'project' CHECK (level IN ('organization', 'project', 'personal', 'session')),
    scope           TEXT NOT NULL DEFAULT 'fact' CHECK (scope IN ('fact', 'preference', 'decision', 'constraint', 'pattern', 'episode_summary')),
    embedding       vector(1536),
    tags            TEXT[] DEFAULT '{}',
    confidence      REAL NOT NULL DEFAULT 1.0 CHECK (confidence >= 0.0 AND confidence <= 1.0),
    status          TEXT NOT NULL DEFAULT 'PROPOSED' CHECK (status IN ('PROPOSED', 'CONFIRMED', 'REJECTED', 'SUPERSEDED')),
    source          TEXT,
    source_event_id BIGINT,
    proposed_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    confirmed_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    superseded_by   UUID REFERENCES memory_items(id) ON DELETE SET NULL,
    use_count       INTEGER NOT NULL DEFAULT 0,
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_memory_embedding ON memory_items USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
CREATE INDEX IF NOT EXISTS idx_memory_project ON memory_items(project_id);
CREATE INDEX IF NOT EXISTS idx_memory_level ON memory_items(level);
CREATE INDEX IF NOT EXISTS idx_memory_status ON memory_items(status);
CREATE INDEX IF NOT EXISTS idx_memory_tags ON memory_items USING GIN(tags);
CREATE INDEX IF NOT EXISTS idx_memory_user ON memory_items(user_id);

-- ============================================================
-- EPISODES (Bug & Incident narratives with vector search)
-- ============================================================
CREATE TABLE IF NOT EXISTS episodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id      UUID,
    title           TEXT NOT NULL,
    episode_type    TEXT NOT NULL CHECK (episode_type IN ('bug_fix', 'feature', 'refactor', 'incident', 'investigation', 'onboarding')),
    trigger         TEXT,
    investigation   TEXT,
    root_cause      TEXT,
    resolution      TEXT,
    verification    TEXT,
    tags            TEXT[] DEFAULT '{}',
    embedding       vector(1536),
    files_involved  TEXT[] DEFAULT '{}',
    error_patterns  TEXT[] DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'INVESTIGATING', 'RESOLVED', 'WONT_FIX')),
    opened_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    resolved_by     UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_episodes_project ON episodes(project_id);
CREATE INDEX IF NOT EXISTS idx_episodes_type ON episodes(episode_type);
CREATE INDEX IF NOT EXISTS idx_episodes_status ON episodes(status);
CREATE INDEX IF NOT EXISTS idx_episodes_embedding ON episodes USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);
CREATE INDEX IF NOT EXISTS idx_episodes_errors ON episodes USING GIN(error_patterns);
CREATE INDEX IF NOT EXISTS idx_episodes_files ON episodes USING GIN(files_involved);
CREATE INDEX IF NOT EXISTS idx_episodes_tags ON episodes USING GIN(tags);

-- ============================================================
-- EPISODE EVENTS
-- ============================================================
CREATE TABLE IF NOT EXISTS episode_events (
    episode_id  UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    event_id    BIGINT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('trigger', 'investigation', 'attempt', 'fix', 'verification', 'context')),
    note        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (episode_id, event_id)
);

-- ============================================================
-- WATCHED FILES (Passive extraction from instruction files)
-- ============================================================
CREATE TABLE IF NOT EXISTS watched_files (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path            TEXT NOT NULL,
    last_hash       TEXT,
    file_type       TEXT NOT NULL CHECK (file_type IN ('claude_md', 'cursorrules', 'copilot_instructions', 'windsurfrules', 'custom')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, path)
);

-- ============================================================
-- TASKS
-- ============================================================
CREATE TABLE IF NOT EXISTS tasks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    episode_id  UUID REFERENCES episodes(id) ON DELETE SET NULL,
    session_id  UUID,
    title       TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'IN_PROGRESS', 'DONE', 'BLOCKED')),
    assigned_to UUID,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
