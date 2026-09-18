-- Rollback for 013_roles: restores OWNER/MEMBER-only membership roles.
-- Custom project_roles rows are dropped. ADMIN/EDITOR/VIEWER/custom
-- memberships collapse to MEMBER (OWNER stays OWNER).

DROP TABLE IF EXISTS project_roles;

ALTER TABLE project_members DROP CONSTRAINT IF EXISTS project_members_role_check;

UPDATE project_members
   SET role = CASE WHEN role = 'OWNER' THEN 'OWNER' ELSE 'MEMBER' END;

ALTER TABLE project_members ALTER COLUMN role SET DEFAULT 'MEMBER';

ALTER TABLE project_members
    ADD CONSTRAINT project_members_role_check
    CHECK (role IN ('OWNER', 'MEMBER'));
