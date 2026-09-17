-- 003_sessions.up.sql: Session layer + scoping (Phase 3, nexus issues #12/#13).
--
-- Depends on: 001_initial (projects, users, memory_items, episodes, tasks),
--             002_events (events).
-- Sessions scope multiplayer collaboration: participants join one active
-- session per project; memories created inside a session start
-- session-scoped (level='session', session_id set) and are promoted to
-- project scope only via the promotion flow (see internal/store/sessions.go).
-- New sessions inherit project + org CONFIRMED memories, never sibling
-- session memories (enforced in Go, not SQL — no cross-row visibility rule
-- can be expressed as a CHECK constraint).

-- ============================================================
-- SESSIONS
-- ============================================================
CREATE TABLE IF NOT EXISTS sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       TEXT,
    created_by  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_sessions_active ON sessions(project_id, is_active)
    WHERE is_active;

-- ============================================================
-- SESSION PARTICIPANTS
-- ============================================================
-- One row per (session, user-or-agent) membership. user_id and agent_id are
-- both nullable because agents join before they have a users row (agents
-- table lands in migration 004); at least one must be set (enforced below).
-- A surrogate PK is used instead of PRIMARY KEY(session_id, user_id):
-- Postgres treats NULLs as distinct in UNIQUE/PK, so a natural key cannot
-- prevent duplicate agent-only rows; the partial unique indexes below give
-- the real protection.
CREATE TABLE IF NOT EXISTS session_participants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    agent_id    UUID,
    role        TEXT NOT NULL DEFAULT 'MEMBER'
        CHECK (role IN ('OWNER', 'MEMBER', 'OBSERVER')),
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at     TIMESTAMPTZ,
    CHECK (user_id IS NOT NULL OR agent_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_participants_session
    ON session_participants(session_id);
CREATE INDEX IF NOT EXISTS idx_participants_active
    ON session_participants(session_id, left_at)
    WHERE left_at IS NULL;
-- A user can hold only one membership row per session (re-join reuses the
-- row by clearing left_at; see JoinSession in internal/store/sessions.go).
CREATE UNIQUE INDEX IF NOT EXISTS uq_participants_session_user
    ON session_participants(session_id, user_id)
    WHERE user_id IS NOT NULL;

-- ============================================================
-- SCOPING FKs — wire the bare UUID columns from 001/002 to sessions
-- ============================================================
-- memory_items.session_id: session-scoped memories (level='session').
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_memory_session'
    ) THEN
        ALTER TABLE memory_items
            ADD CONSTRAINT fk_memory_session
            FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE SET NULL;
    END IF;
END
$$;

-- tasks.session_id: tasks opened inside a session.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_task_session'
    ) THEN
        ALTER TABLE tasks
            ADD CONSTRAINT fk_task_session
            FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE SET NULL;
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_memory_session ON memory_items(session_id)
    WHERE session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id)
    WHERE session_id IS NOT NULL;
