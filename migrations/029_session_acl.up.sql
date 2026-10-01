-- 029_session_acl.up.sql
-- P0 task 1 (product spec 4.5, D9, 10.5 step 1): session content is
-- owner-or-grantee. Existing snapshots become owner-only. Org ADMIN and
-- project session:read do not grant access to someone else's content.
-- audit_events is the append-only tenant hash chain (spec 10.4).

ALTER TABLE session_snapshots
    ADD COLUMN IF NOT EXISTS owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

-- Backfill from the collaborative session creator when the snapshot's
-- session_id is a sessions row.
UPDATE session_snapshots ss
SET owner_user_id = s.created_by
FROM sessions s
WHERE ss.owner_user_id IS NULL
  AND ss.session_id = s.id;

-- Otherwise the uploading machine's registered workspace user on the same
-- project. DISTINCT ON keeps one user when several checkouts share a machine.
UPDATE session_snapshots ss
SET owner_user_id = picked.user_id
FROM (
    SELECT DISTINCT ON (project_id, machine_id)
           project_id, machine_id, user_id
    FROM workspaces
    WHERE machine_id <> ''
    ORDER BY project_id, machine_id, last_seen DESC NULLS LAST, created_at DESC
) picked
WHERE ss.owner_user_id IS NULL
  AND ss.source_machine_id IS NOT NULL
  AND ss.source_machine_id <> ''
  AND picked.project_id = ss.project_id
  AND picked.machine_id = ss.source_machine_id;

CREATE INDEX IF NOT EXISTS idx_snapshots_owner ON session_snapshots(owner_user_id);

-- One owner row per session content id (snapshot and/or provenance).
-- NULL owner_user_id means the owner could not be resolved: hidden from
-- everyone except project admins, who may assign or delete it.
CREATE TABLE IF NOT EXISTS session_content_owners (
    session_id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO session_content_owners (session_id, project_id, owner_user_id)
SELECT DISTINCT ON (session_id) session_id, project_id, owner_user_id
FROM session_snapshots
ORDER BY session_id, snapshot_version DESC
ON CONFLICT (session_id) DO UPDATE
    SET owner_user_id = COALESCE(session_content_owners.owner_user_id, EXCLUDED.owner_user_id);

INSERT INTO session_content_owners (session_id, project_id, owner_user_id)
SELECT DISTINCT ON (session_id) session_id, project_id, NULL::uuid
FROM session_file_operations
ORDER BY session_id, created_at DESC
ON CONFLICT (session_id) DO NOTHING;

INSERT INTO session_content_owners (session_id, project_id, owner_user_id)
SELECT DISTINCT ON (session_id) session_id, project_id, NULL::uuid
FROM session_tool_executions
ORDER BY session_id, created_at DESC
ON CONFLICT (session_id) DO NOTHING;

UPDATE session_content_owners o
SET owner_user_id = s.created_by
FROM sessions s
WHERE o.owner_user_id IS NULL
  AND o.session_id = s.id;

CREATE INDEX IF NOT EXISTS idx_session_content_owners_project
    ON session_content_owners(project_id);

-- Person-specific grants (D9). Team-wide sharing is a later explicit act.
CREATE TABLE IF NOT EXISTS session_content_grants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grantee_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_session_content_grants_active
    ON session_content_grants (session_id, grantee_user_id)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_session_content_grants_grantee
    ON session_content_grants (grantee_user_id)
    WHERE revoked_at IS NULL;

-- Append-only audit log. Partitioned by month with a default partition so
-- an insert in a month that has no dedicated partition still lands.
-- prev_hash is a hash chain per tenant (NULL tenant = platform events).
CREATE TABLE IF NOT EXISTS audit_events (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id UUID,
    actor_user_id TEXT,
    actor_kind TEXT NOT NULL DEFAULT 'user'
        CHECK (actor_kind IN ('user', 'admin', 'super_admin', 'support', 'system', 'agent')),
    action TEXT NOT NULL,
    resource_kind TEXT,
    resource_id TEXT,
    project_id UUID,
    ip TEXT,
    device_id TEXT,
    user_agent TEXT,
    reason TEXT,
    request_id TEXT,
    outcome TEXT NOT NULL DEFAULT 'ok',
    prev_hash TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);

CREATE TABLE IF NOT EXISTS audit_events_y2026m09 PARTITION OF audit_events FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE IF NOT EXISTS audit_events_y2026m10 PARTITION OF audit_events FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE IF NOT EXISTS audit_events_y2026m11 PARTITION OF audit_events FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE IF NOT EXISTS audit_events_y2026m12 PARTITION OF audit_events FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m01 PARTITION OF audit_events FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m02 PARTITION OF audit_events FOR VALUES FROM ('2027-02-01') TO ('2027-03-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m03 PARTITION OF audit_events FOR VALUES FROM ('2027-03-01') TO ('2027-04-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m04 PARTITION OF audit_events FOR VALUES FROM ('2027-04-01') TO ('2027-05-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m05 PARTITION OF audit_events FOR VALUES FROM ('2027-05-01') TO ('2027-06-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m06 PARTITION OF audit_events FOR VALUES FROM ('2027-06-01') TO ('2027-07-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m07 PARTITION OF audit_events FOR VALUES FROM ('2027-07-01') TO ('2027-08-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m08 PARTITION OF audit_events FOR VALUES FROM ('2027-08-01') TO ('2027-09-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m09 PARTITION OF audit_events FOR VALUES FROM ('2027-09-01') TO ('2027-10-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m10 PARTITION OF audit_events FOR VALUES FROM ('2027-10-01') TO ('2027-11-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m11 PARTITION OF audit_events FOR VALUES FROM ('2027-11-01') TO ('2027-12-01');
CREATE TABLE IF NOT EXISTS audit_events_y2027m12 PARTITION OF audit_events FOR VALUES FROM ('2027-12-01') TO ('2028-01-01');
CREATE TABLE IF NOT EXISTS audit_events_default PARTITION OF audit_events DEFAULT;

CREATE INDEX IF NOT EXISTS idx_audit_events_tenant ON audit_events (tenant_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_project ON audit_events (project_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events (actor_user_id, at DESC);
