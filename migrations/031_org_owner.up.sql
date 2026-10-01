-- 031_org_owner.up.sql
-- P1 org administration: Owner, Admin, Member. Existing ADMIN rows stay
-- ADMIN. The org creator is backfilled to OWNER (product spec 10.5 step 2).

DO $$
DECLARE cname text;
BEGIN
    SELECT c.conname INTO cname
    FROM pg_constraint c
    WHERE c.conrelid = 'organization_members'::regclass
      AND c.contype = 'c'
      AND pg_get_constraintdef(c.oid) ILIKE '%ADMIN%'
      AND pg_get_constraintdef(c.oid) ILIKE '%MEMBER%'
    LIMIT 1;
    IF cname IS NOT NULL THEN
        EXECUTE format('ALTER TABLE organization_members DROP CONSTRAINT %I', cname);
    END IF;
END $$;

ALTER TABLE organization_members
    DROP CONSTRAINT IF EXISTS organization_members_role_check;

ALTER TABLE organization_members
    ADD CONSTRAINT organization_members_role_check
    CHECK (role IN ('OWNER', 'ADMIN', 'MEMBER'));

UPDATE organization_members om
SET role = 'OWNER'
FROM organizations o
WHERE om.org_id = o.id
  AND o.created_by IS NOT NULL
  AND om.user_id = o.created_by
  AND om.role = 'ADMIN';
