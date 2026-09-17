-- Migration 003_sessions — Phase 3 session schema.
-- Source of truth: implementation-plan.md §3.1 (transcribed with two
-- documented validity fixes, see below) plus the session-isolation rule
-- of §3.3 (memories default to session-scoped; promotion clears session_id).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies cleanly on top of 001 alone: sessions references only projects
-- and users (both in 001). The events table (002) is not referenced here.
-- Forward-only step 3 of N; rollback in 003_sessions.down.sql.

-- ============================================================
-- SESSIONS — a live multiplayer working session within a project
-- ============================================================
CREATE TABLE sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    title       TEXT,
    created_by  UUID NOT NULL REFERENCES users(id),
    is_active   BOOLEAN DEFAULT true,
    created_at  TIMESTAMPTZ DEFAULT now(),
    ended_at    TIMESTAMPTZ
);

-- ============================================================
-- SESSION PARTICIPANTS — users and agents currently/formerly in a session
-- ============================================================
-- DEVIATION from plan §3.1 (validity fix, see ADR-012): the plan's
-- PRIMARY KEY (session_id, COALESCE(user_id, gen_random_uuid())) is not
-- valid Postgres — a volatile function cannot appear in a primary key,
-- and a PK row cannot represent re-join history. Surrogate id preserves
-- join/leave history (one row per join; Leave stamps left_at), while two
-- partial unique indexes enforce single ACTIVE membership per user/agent
-- per session.
CREATE TABLE session_participants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id),
    agent_id    UUID,                                    -- FK to agents(id) deferred to migration 004,
                                                        -- same deferred-FK pattern as 001 episode_events.event_id
    role        TEXT DEFAULT 'MEMBER'
        CHECK (role IN ('OWNER', 'MEMBER', 'OBSERVER')),
    joined_at   TIMESTAMPTZ DEFAULT now(),
    left_at     TIMESTAMPTZ,
    CHECK (user_id IS NOT NULL OR agent_id IS NOT NULL)
);

-- One active seat per user per session; left (historical) rows are exempt
-- so re-join after Leave inserts a fresh row.
CREATE UNIQUE INDEX uq_session_participants_active_user
    ON session_participants(session_id, user_id)
    WHERE user_id IS NOT NULL AND left_at IS NULL;

-- Same for agent seats (agent-only participants carry NULL user_id).
CREATE UNIQUE INDEX uq_session_participants_active_agent
    ON session_participants(session_id, agent_id)
    WHERE agent_id IS NOT NULL AND left_at IS NULL;

CREATE INDEX idx_sessions_project ON sessions(project_id);
CREATE INDEX idx_sessions_active ON sessions(project_id) WHERE is_active;
CREATE INDEX idx_session_participants_session ON session_participants(session_id);

-- ============================================================
-- SESSION-SCOPED MEMORIES — plan §3.1: link session memories to sessions
-- ============================================================
-- memory_items.session_id exists since 001 as a bare UUID. Plan §3.3:
-- memories default to session-scoped; promotion to project scope clears
-- session_id (sets NULL) and flips level to 'project' — new sessions then
-- inherit project/org CONFIRMED memories but never sibling-session rows
-- (enforced in internal/store/sessions.go IsVisibleToSession).
-- No ON DELETE action: memories outlive their session so the promotion
-- counter (same key in 3+ sessions, §2.7) can still observe them.
ALTER TABLE memory_items
    ADD CONSTRAINT fk_memory_session
    FOREIGN KEY (session_id) REFERENCES sessions(id);

CREATE INDEX idx_memory_session ON memory_items(session_id);
