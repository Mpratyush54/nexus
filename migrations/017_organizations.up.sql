-- Migration 017_organizations — teams above projects (issue #168 / Phase 8).
--
-- Organizations group projects for multi-project teams. Membership is
-- explicit in organization_members; projects.org_id links a project to
-- its owning org. Org ADMINS automatically receive full access to every
-- organization-owned project (enforced in IsProjectMember).
-- Forward-only step; rollback in 017_organizations.down.sql.
-- Idempotent: CREATE TABLE IF NOT EXISTS + ADD COLUMN IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS organizations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    slug        TEXT UNIQUE,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT organizations_name_nonempty CHECK (length(trim(name)) > 0)
);

CREATE TABLE IF NOT EXISTS organization_members (
    org_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'MEMBER'
        CHECK (role IN ('ADMIN', 'MEMBER')),
    granted_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX IF NOT EXISTS organization_members_user_id_idx
    ON organization_members (user_id);

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS org_id UUID REFERENCES organizations(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS projects_org_id_idx
    ON projects (org_id) WHERE org_id IS NOT NULL;
