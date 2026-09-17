-- Migration 008_processor_election — single designated Memory Processor per project.
-- Source of truth: implementation-plan.md §6.3 (designated-processor failover
-- when the owner goes offline) plus the ISSUE-2 follow-up that deferred
-- failover out of the storage layer.
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 001_initial alone: constrained columns (project_id,
-- is_online, is_designated_processor) all belong to the 001 workspaces table.
-- No dependency on 002–007. Forward-only step 8 of N; rollback in
-- 008_processor_election.down.sql.
--
-- PROBLEM (issue #36): SetDesignatedProcessor (internal/store/workspaces.go)
-- is a bare per-row flip — nothing stops two workspaces of the same project
-- from both carrying is_designated_processor, and a dead owner keeps the flag
-- while extraction silently stops (the daemon processor gates on it).
--
-- FIX: a partial unique index enforcing at most one DESIGNATED *AND ONLINE*
-- workspace per project. Both flags are in the predicate (verified against
-- 001_initial.up.sql: workspaces.project_id UUID, is_online BOOLEAN DEFAULT
-- false, is_designated_processor BOOLEAN DEFAULT false):
--   - A second online designatee conflicts instead of silently forking
--     extraction (compare-and-set in ElectDesignatedProcessor relies on this
--     as the arbiter; the loser retries).
--   - A stale owner swept offline (ReassignStaleDesignated clears both flags)
--     drops out of the index, so electing its successor never conflicts with
--     the corpse row.
-- Columns are nullable in 001, so NULL flags are excluded from the index the
-- same way false is (NULL is never equal to TRUE in the predicate) — no
-- COALESCE needed, matching the bare-WHERE style of 003
-- (uq_session_participants_active_*, idx_sessions_active).

CREATE UNIQUE INDEX uq_workspaces_designated_processor_online
    ON workspaces(project_id)
    WHERE is_designated_processor AND is_online;
