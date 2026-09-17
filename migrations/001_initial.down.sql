-- 001_initial.down.sql: Rollback core schema
DROP TABLE IF EXISTS tasks CASCADE;
DROP TABLE IF EXISTS watched_files CASCADE;
DROP TABLE IF EXISTS episode_events CASCADE;
DROP TABLE IF EXISTS episodes CASCADE;
DROP TABLE IF EXISTS memory_items CASCADE;
DROP TABLE IF EXISTS workspaces CASCADE;
DROP TABLE IF EXISTS projects CASCADE;
DROP TABLE IF EXISTS users CASCADE;

-- Extensions created by 001_initial.up.sql; drop last so reruns start clean.
DROP EXTENSION IF EXISTS "vector";
DROP EXTENSION IF EXISTS "pgcrypto";
