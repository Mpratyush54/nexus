-- Rollback for 001_initial: drops everything the up migration creates,
-- in reverse dependency order so no FK violations occur.
-- Indexes die with their tables (no separate DROP INDEX needed).
-- Extensions are intentionally NOT dropped: they are shared cluster-level
-- infra that later migrations (002+) also rely on, and on managed RDS
-- DROP EXTENSION requires elevated privileges the app role may not have.

DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS watched_files;
DROP TABLE IF EXISTS episode_events;
DROP TABLE IF EXISTS episodes;
DROP TABLE IF EXISTS memory_items;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS users;
