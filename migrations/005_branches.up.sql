-- Migration 005_branches — Phase 5 memory branching (copy-on-write).
-- Source of truth: implementation-plan.md §§5.1–5.2 (transcribed exactly,
-- with documented operational additions, see below).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 001_initial + 002_events: memory_branches references
-- projects/users (001), memory_items (001), and events(id) for
-- forked_at_event_id (002, BIGSERIAL → BIGINT FK). Apply 002 before 005.
-- Forward-only step 5 of N; rollback in 005_branches.down.sql.
--
-- OPERATIONAL ADDITIONS beyond the plan text (all index/constraint naming):
-- 1. Three indexes (plan names none): per-project branch lookup, parent-chain
--    walk, and branch-scoped item lookup — the exact predicates
--    internal/store/branches.go issues.
-- 2. The memory_items.branch_id FK is NAMED (fk_memory_branch) instead of the
--    plan's inline REFERENCES, so the down migration can drop it explicitly —
--    same pattern as 003's fk_memory_session.
--
-- AUTO-CREATE MAIN (plan: "Every project gets a 'main' branch
-- automatically"): implemented at the application layer via
-- BranchStore.EnsureMainBranch (INSERT ... ON CONFLICT (project_id, name)
-- DO NOTHING, visibility 'shared'), NOT via a DB trigger — triggers add
-- hidden write paths and privilege surface on Aurora, and an explicit call
-- keeps branch creation testable without a live database. See ADR-017.

-- ============================================================
-- MEMORY BRANCHES — copy-on-write branch heads (plan §5.1, exact)
-- ============================================================
CREATE TABLE memory_branches (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id),
    name                TEXT NOT NULL,
    owner_id            UUID REFERENCES users(id),
    parent_branch_id    UUID REFERENCES memory_branches(id),
    forked_at_event_id  BIGINT REFERENCES events(id),
    visibility          TEXT DEFAULT 'private'
        CHECK (visibility IN ('private', 'shared')),
    created_at          TIMESTAMPTZ DEFAULT now(),
    UNIQUE(project_id, name)
);

CREATE INDEX idx_memory_branches_project ON memory_branches(project_id);
CREATE INDEX idx_memory_branches_parent ON memory_branches(parent_branch_id);

-- Every project gets a "main" branch automatically (application-layer;
-- see header note + EnsureMainBranch). Branch-scoped items follow:
ALTER TABLE memory_items
    ADD COLUMN branch_id UUID,
    ADD CONSTRAINT fk_memory_branch
    FOREIGN KEY (branch_id) REFERENCES memory_branches(id);

CREATE INDEX idx_memory_branch ON memory_items(branch_id);
