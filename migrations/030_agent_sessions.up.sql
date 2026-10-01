-- 030_agent_sessions.up.sql
-- Cloud session model (product spec 4.2): native harness IDs, two-phase
-- versions, content-addressed blob metadata, turns, and person grants.
-- Blob bytes live here for the dev verifier. Production uploads go to S3;
-- complete still requires every referenced hash to be present.

CREATE TABLE IF NOT EXISTS agent_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    harness TEXT NOT NULL,
    native_id TEXT NOT NULL,
    origin_machine_id TEXT NOT NULL DEFAULT '',
    workspace_root_hint TEXT,
    title TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at TIMESTAMPTZ,
    visibility TEXT NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'team')),
    parent_session_id UUID REFERENCES agent_sessions(id) ON DELETE SET NULL,
    lineage_kind TEXT
        CHECK (lineage_kind IS NULL OR lineage_kind IN ('fork', 'teleport', 'seeded')),
    summary TEXT,
    UNIQUE (project_id, harness, native_id, origin_machine_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_owner
    ON agent_sessions (owner_user_id, last_active_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_project
    ON agent_sessions (project_id, last_active_at DESC);

CREATE TABLE IF NOT EXISTS session_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    version INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    turn_count INT NOT NULL DEFAULT 0,
    manifest_sha256 TEXT,
    manifest JSONB,
    uploaded_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    state TEXT NOT NULL DEFAULT 'uploading'
        CHECK (state IN ('uploading', 'complete', 'transcript_only', 'failed')),
    UNIQUE (session_id, version)
);

CREATE TABLE IF NOT EXISTS blobs (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    sha256 TEXT NOT NULL,
    size BIGINT NOT NULL,
    chunk_count INT NOT NULL DEFAULT 1,
    kind TEXT NOT NULL DEFAULT 'plain'
        CHECK (kind IN ('plain', 'chunked', 'secret_envelope')),
    purpose TEXT NOT NULL DEFAULT 'file'
        CHECK (purpose IN ('file', 'transcript')),
    verified_at TIMESTAMPTZ,
    body BYTEA,
    PRIMARY KEY (project_id, sha256)
);

CREATE TABLE IF NOT EXISTS session_turns (
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    idx INT NOT NULL,
    role TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    text_preview TEXT,
    tool_calls JSONB NOT NULL DEFAULT '[]'::jsonb,
    blob_ref TEXT,
    PRIMARY KEY (session_id, idx)
);

CREATE TABLE IF NOT EXISTS session_grants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    version_id UUID REFERENCES session_versions(id) ON DELETE CASCADE,
    grantee_user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    grantee_team BOOLEAN NOT NULL DEFAULT false,
    granted_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    CHECK (grantee_user_id IS NOT NULL OR grantee_team)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_grants_user
    ON session_grants (session_id, grantee_user_id)
    WHERE revoked_at IS NULL AND grantee_user_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_grants_team
    ON session_grants (session_id)
    WHERE revoked_at IS NULL AND grantee_team;

CREATE TABLE IF NOT EXISTS storage_usage (
    plan_scope TEXT PRIMARY KEY,
    bytes_used BIGINT NOT NULL DEFAULT 0,
    bytes_cap BIGINT NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'ok'
        CHECK (state IN ('ok', 'warn', 'full'))
);
