-- Migration 007_branch_upkeep — Phase 5 branch upkeep (plan §5.4).
-- Source of truth: implementation-plan.md §5.4 (30-day auto-archive +
-- persisted staleness surfacing).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 005_branches: memory_branches (005) gains two columns;
-- memory_items is untouched. Forward-only step 7 of N; rollback in
-- 007_branch_upkeep.down.sql.
--
-- Columns (verified against migrations/005_branches.up.sql table/column names):
-- 1. memory_branches.archived_at TIMESTAMPTZ (NULL = active; set when the
--    30-day auto-archive fires via BranchStore.ArchiveBranch).
-- 2. memory_branches.potentially_stale BOOLEAN DEFAULT false (persisted
--    DetectStale signal via BranchStore.MarkStale / SurfaceStaleness;
--    DetectStale itself stays pure in branch_diff.go, issue #18).

ALTER TABLE memory_branches
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS potentially_stale BOOLEAN DEFAULT false;
