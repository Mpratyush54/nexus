-- Migration 001_initial — Phase 1 core tables & pgvector.
-- Source of truth: implementation-plan.md §1.1 (transcribed exactly).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Forward-only step 1 of N; rollback in 001_initial.down.sql.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";

-- ============================================================
-- USERS
-- ============================================================
CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username    TEXT UNIQUE NOT NULL,
    email       TEXT UNIQUE,
    settings    JSONB DEFAULT '{}',          -- LLM provider, API key ref, preferences
    created_at  TIMESTAMPTZ DEFAULT now()
);

-- ============================================================
-- PROJECTS
-- ============================================================
CREATE TABLE projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_url   TEXT,                    -- normalized git remote URL
    root_commit     TEXT,                    -- git rev-list --max-parents=0 HEAD
    folder_name     TEXT NOT NULL,           -- fallback: leaf dir name
    display_name    TEXT,
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE(canonical_url),
    UNIQUE(root_commit)
);

-- ============================================================
-- WORKSPACES
-- ============================================================
CREATE TABLE workspaces (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    user_id     UUID NOT NULL REFERENCES users(id),
    machine_id  TEXT NOT NULL,               -- hostname or hardware UUID
    path        TEXT NOT NULL,               -- absolute local path
    branch      TEXT,                        -- current git branch
    commit_sha  TEXT,                        -- current HEAD
    is_dirty    BOOLEAN DEFAULT false,
    is_online   BOOLEAN DEFAULT false,
    is_designated_processor BOOLEAN DEFAULT false,  -- runs Memory Processor
    last_seen   TIMESTAMPTZ,
    daemon_url  TEXT,                        -- ws://localhost:PORT
    created_at  TIMESTAMPTZ DEFAULT now(),
    UNIQUE(machine_id, path)
);

-- ============================================================
-- MEMORY ITEMS — inference-optimized, vector-searchable
-- ============================================================
CREATE TABLE memory_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Scope & ownership
    project_id      UUID REFERENCES projects(id),       -- NULL for org-level
    user_id         UUID REFERENCES users(id),           -- set for personal memories
    session_id      UUID,                                -- set for session-scoped
    org_id          UUID,                                -- future: organization table

    -- Identity
    key             TEXT NOT NULL,                        -- machine key: "testing/framework"

    -- LLM-optimized content
    content         TEXT NOT NULL                         -- natural language, 20-500 chars enforced
        CHECK (length(content) >= 20 AND length(content) <= 2000),
    context_snippet TEXT,                                -- 1-2 line provenance: "Decided by Alice during auth refactor"

    -- Classification
    level           TEXT NOT NULL DEFAULT 'project'
        CHECK (level IN ('organization', 'project', 'personal', 'session')),
    scope           TEXT NOT NULL DEFAULT 'fact'
        CHECK (scope IN ('fact', 'preference', 'decision', 'constraint', 'pattern', 'episode_summary')),

    -- Search
    embedding       vector(1536),                        -- for semantic search
    tags            TEXT[],

    -- Inference metadata
    confidence      REAL DEFAULT 1.0                     -- decays over time
        CHECK (confidence >= 0.0 AND confidence <= 1.0),

    -- Lifecycle
    status          TEXT DEFAULT 'PROPOSED'
        CHECK (status IN ('PROPOSED', 'CONFIRMED', 'REJECTED', 'SUPERSEDED')),
    source          TEXT,                                 -- "user:alice", "agent:claude", "processor", "extractor:git_diff"
    source_event_id BIGINT,                              -- links back to originating event
    proposed_by     UUID REFERENCES users(id),
    confirmed_by    UUID REFERENCES users(id),
    superseded_by   UUID REFERENCES memory_items(id),

    -- Usage tracking (for confidence decay + relevance)
    use_count       INTEGER DEFAULT 0,
    last_used_at    TIMESTAMPTZ,

    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- Vector similarity index (IVFFlat for <100K items; switch to HNSW at scale)
CREATE INDEX idx_memory_embedding ON memory_items
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
CREATE INDEX idx_memory_project ON memory_items(project_id);
CREATE INDEX idx_memory_level ON memory_items(level);
CREATE INDEX idx_memory_status ON memory_items(status);
CREATE INDEX idx_memory_tags ON memory_items USING GIN(tags);
CREATE INDEX idx_memory_user ON memory_items(user_id);

-- ============================================================
-- EPISODES — retrievable bug/incident/feature arcs
-- ============================================================
CREATE TABLE episodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    session_id      UUID,                                -- may span sessions

    -- Identity
    title           TEXT NOT NULL,                        -- "Fixed auth timeout on WebSocket upgrade"
    episode_type    TEXT NOT NULL
        CHECK (episode_type IN ('bug_fix', 'feature', 'refactor', 'incident', 'investigation', 'onboarding')),

    -- The story arc
    trigger         TEXT,                                 -- what started it: "ConnectionTimeout in ws.go:142"
    investigation   TEXT,                                 -- what was tried: "Checked pool settings, traced pgx lifecycle"
    root_cause      TEXT,                                 -- why it happened: "pool_max_conn_lifetime too short for long-lived WS"
    resolution      TEXT,                                 -- what fixed it: "Increased to 30m, added health check ping"
    verification    TEXT,                                 -- how we know it's fixed: "Load test passed, 0 timeouts over 2h"

    -- Searchability
    tags            TEXT[],
    embedding       vector(1536),                        -- embed the full narrative for similarity search
    files_involved  TEXT[],                               -- ["internal/server/ws.go", "internal/store/db.go"]
    error_patterns  TEXT[],                               -- ["ConnectionTimeout", "pgx pool exhausted"]

    -- Status
    status          TEXT DEFAULT 'OPEN'
        CHECK (status IN ('OPEN', 'INVESTIGATING', 'RESOLVED', 'WONT_FIX')),

    -- Lifecycle
    opened_at       TIMESTAMPTZ DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    created_by      UUID REFERENCES users(id),
    resolved_by     UUID REFERENCES users(id)
);

CREATE INDEX idx_episodes_project ON episodes(project_id);
CREATE INDEX idx_episodes_type ON episodes(episode_type);
CREATE INDEX idx_episodes_status ON episodes(status);
CREATE INDEX idx_episodes_embedding ON episodes
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);
CREATE INDEX idx_episodes_errors ON episodes USING GIN(error_patterns);
CREATE INDEX idx_episodes_files ON episodes USING GIN(files_involved);
CREATE INDEX idx_episodes_tags ON episodes USING GIN(tags);

-- ============================================================
-- EPISODE EVENTS — links events to their episode
-- ============================================================
CREATE TABLE episode_events (
    episode_id  UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    event_id    BIGINT NOT NULL,                         -- REFERENCES events(id), added in migration 002
    role        TEXT NOT NULL
        CHECK (role IN ('trigger', 'investigation', 'attempt', 'fix', 'verification', 'context')),
    note        TEXT,                                    -- optional annotation
    created_at  TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (episode_id, event_id)
);

-- ============================================================
-- WATCHED FILES — for passive extraction from instruction files
-- ============================================================
CREATE TABLE watched_files (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path            TEXT NOT NULL,                        -- relative to workspace root
    last_hash       TEXT,                                -- SHA256 of last known content
    file_type       TEXT NOT NULL
        CHECK (file_type IN ('claude_md', 'cursorrules', 'copilot_instructions',
                             'windsurfrules', 'custom')),
    created_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE(workspace_id, path)
);

-- ============================================================
-- TASKS
-- ============================================================
CREATE TABLE tasks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    episode_id  UUID REFERENCES episodes(id),            -- bug fix task links to its episode
    session_id  UUID,
    title       TEXT NOT NULL,
    description TEXT,
    status      TEXT DEFAULT 'OPEN'
        CHECK (status IN ('OPEN', 'IN_PROGRESS', 'DONE', 'BLOCKED')),
    assigned_to UUID,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
