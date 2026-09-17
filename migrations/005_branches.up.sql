-- 005_branches.up.sql: Copy-on-Write memory branching (Phase 5, nexus issue #17).
--
-- Depends on: 001_initial (projects, users, memory_items),
--             002_events (events, for forked_at_event_id provenance).
-- Idempotent: CREATE IF NOT EXISTS + guarded ALTERs + ON CONFLICT seeds.
--
-- Design (plan §5.1/§5.2): branches are lightweight pointer rows. Forking
-- inserts ONE row (parent_branch_id = source); zero memory rows are copied.
-- Reads resolve copy-on-write: walk branch -> parent -> ... -> main, first
-- key match wins. Writes always insert into the current branch, never mutate
-- parents. See docs/decisions/2026-09-17-branches.md for rationale.
--
-- Episodes are NOT branched (plan §5.3): a bug fix is a project-scoped fact.
-- Only memory_items carry branch_id.

-- ============================================================
-- MEMORY_BRANCHES — branch pointers, not data copies
-- ============================================================
CREATE TABLE IF NOT EXISTS memory_branches (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    owner_id            UUID REFERENCES users(id) ON DELETE SET NULL,
    parent_branch_id    UUID REFERENCES memory_branches(id) ON DELETE SET NULL,
    forked_at_event_id  BIGINT REFERENCES events(id) ON DELETE SET NULL,
    visibility          TEXT NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'shared')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_branches_project ON memory_branches(project_id);
CREATE INDEX IF NOT EXISTS idx_branches_parent ON memory_branches(parent_branch_id)
    WHERE parent_branch_id IS NOT NULL;

-- ============================================================
-- MEMORY_ITEMS.branch_id — CoW overlay pointer
-- ============================================================
-- NULL (or pointing at the project's "main" branch) means main-line memory.
-- Branch writes insert new rows tagged with the branch id; parent rows are
-- never updated, so the parent view is unchanged by construction.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'memory_items' AND column_name = 'branch_id'
    ) THEN
        ALTER TABLE memory_items
            ADD COLUMN branch_id UUID
            REFERENCES memory_branches(id) ON DELETE SET NULL;
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_memory_branch ON memory_items(branch_id)
    WHERE branch_id IS NOT NULL;

-- ============================================================
-- MAIN AUTO-BRANCH — every project gets a shared "main"
-- ============================================================
-- New projects get theirs from EnsureMainBranch in
-- internal/store/branches.go (get-or-create at write time); this seed covers
-- projects that already exist when the migration lands.
INSERT INTO memory_branches (project_id, name, visibility)
SELECT id, 'main', 'shared' FROM projects
ON CONFLICT (project_id, name) DO NOTHING;
