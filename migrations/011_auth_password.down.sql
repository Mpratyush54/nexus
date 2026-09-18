-- Rollback for 011_auth_password: drops password_hash.
-- All logins revert to failing closed (no password to verify against).

ALTER TABLE users DROP COLUMN IF EXISTS password_hash;
