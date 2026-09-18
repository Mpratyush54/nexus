-- Migration 013_roles — granular RBAC and custom project roles (issue #163).
--
-- Expands project_members.role beyond OWNER/MEMBER to the four built-in
-- roles (OWNER, ADMIN, EDITOR, VIEWER) plus custom role names defined in
-- project_roles. Existing MEMBER rows become EDITOR (write access without
-- member management), matching the pre-RBAC "full collaborator" behavior.
--
-- Built-in permission sets live in application code; project_roles stores
-- custom roles with granular permission arrays.
-- Forward-only step; rollback in 013_roles.down.sql.
-- Idempotent: IF NOT EXISTS / IF EXISTS / ON CONFLICT-safe updates.

CREATE TABLE IF NOT EXISTS project_roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    permissions TEXT[] NOT NULL DEFAULT '{}',
    is_builtin  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name),
    CONSTRAINT project_roles_name_nonempty CHECK (length(trim(name)) > 0)
);

CREATE INDEX IF NOT EXISTS project_roles_project_id_idx ON project_roles (project_id);

-- Expand membership role CHECK (drop auto-named column check from 012).
ALTER TABLE project_members DROP CONSTRAINT IF EXISTS project_members_role_check;

UPDATE project_members SET role = 'EDITOR' WHERE role = 'MEMBER';

ALTER TABLE project_members ALTER COLUMN role SET DEFAULT 'EDITOR';

-- Built-ins plus custom names (app also requires custom names in project_roles).
ALTER TABLE project_members
    ADD CONSTRAINT project_members_role_check
    CHECK (
        role IN ('OWNER', 'ADMIN', 'EDITOR', 'VIEWER')
        OR role ~ '^[A-Za-z][A-Za-z0-9 _-]{0,63}$'
    );
