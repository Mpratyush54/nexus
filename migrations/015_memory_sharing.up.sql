-- Migration 015_memory_sharing — per-memory visibility + share grants (issue #164).
--
-- Visibility modes (lowercase, matching branch visibility style):
--   private  — only the creator (proposed_by / user_id)
--   shared   — creator + explicit memory_shares rows (user or role)
--   project  — all project members (DEFAULT; preserves pre-015 behavior)
--   public   — visible without membership (open-source / link-share)
--
-- Forward-only step 15 of N; rollback in 015_memory_sharing.down.sql.
-- Idempotent: IF NOT EXISTS / guarded constraint adds.

ALTER TABLE memory_items
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'project';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'memory_items_visibility_check'
    ) THEN
        ALTER TABLE memory_items
            ADD CONSTRAINT memory_items_visibility_check
            CHECK (visibility IN ('private', 'shared', 'project', 'public'));
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_memory_visibility ON memory_items(visibility);

CREATE TABLE IF NOT EXISTS memory_shares (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    memory_id            UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    shared_with_user_id  UUID REFERENCES users(id) ON DELETE CASCADE,
    shared_with_role     TEXT,
    shared_by            UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (shared_with_user_id IS NOT NULL AND shared_with_role IS NULL)
        OR (shared_with_user_id IS NULL AND shared_with_role IS NOT NULL)
    )
);

-- One grant per (memory, user) and per (memory, role).
CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_shares_user
    ON memory_shares (memory_id, shared_with_user_id)
    WHERE shared_with_user_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_shares_role
    ON memory_shares (memory_id, shared_with_role)
    WHERE shared_with_role IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_memory_shares_memory ON memory_shares(memory_id);
CREATE INDEX IF NOT EXISTS idx_memory_shares_user_id ON memory_shares(shared_with_user_id)
    WHERE shared_with_user_id IS NOT NULL;
