-- Migration 012_project_members — explicit project membership (issue #149).
--
-- Project membership was derived (any workspace row conferred access), so
-- any authenticated account could claim any project by registering a
-- workspace on it. Membership is now explicit: the project creator
-- (projects.created_by, claimed at resolve time) plus granted rows here.
-- Workspace registration requires membership; it never creates it.
--
-- Backfill (one-time, for pre-012 databases): creators become OWNERs and
-- distinct workspace users become MEMBERs, preserving existing access.
-- Forward-only step 12 of N; rollback in 012_project_members.down.sql.
-- Idempotent: CREATE TABLE IF NOT EXISTS + ON CONFLICT DO NOTHING.

CREATE TABLE IF NOT EXISTS project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'MEMBER'
        CHECK (role IN ('OWNER', 'MEMBER')),
    granted_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

INSERT INTO project_members (project_id, user_id, role)
SELECT id, created_by, 'OWNER' FROM projects WHERE created_by IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO project_members (project_id, user_id, role)
SELECT DISTINCT project_id, user_id FROM workspaces WHERE user_id IS NOT NULL
ON CONFLICT DO NOTHING;
