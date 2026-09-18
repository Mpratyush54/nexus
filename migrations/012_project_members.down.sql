-- Rollback for 012_project_members: drops explicit membership.
-- WARNING: membership-gated routes fail closed without this table.

DROP TABLE IF EXISTS project_members;
