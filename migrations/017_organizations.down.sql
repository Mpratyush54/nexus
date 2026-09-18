-- Rollback for 017_organizations: drops org membership and org_id on projects.
-- WARNING: org-scoped routes fail closed without these tables/columns.

DROP INDEX IF EXISTS projects_org_id_idx;
ALTER TABLE projects DROP COLUMN IF EXISTS org_id;
DROP INDEX IF EXISTS organization_members_user_id_idx;
DROP TABLE IF EXISTS organization_members;
DROP TABLE IF EXISTS organizations;
