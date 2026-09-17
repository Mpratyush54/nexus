-- Migration 004_agents — Phase 4 agent registry + push materializer targets.
-- Source of truth: implementation-plan.md §4.1 (transcribed exactly for the
-- two tables and the seven seed rows; cursor/windsurf output files and
-- formats come from the plan's INSERT: cursor → '.cursorrules'/'text',
-- windsurf → '.windsurfrules'/'text').
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 001 (+002/003 when present): project_agents references
-- projects (001). The two ALTERs complete deferred FKs left as bare UUIDs by
-- earlier migrations — session_participants.agent_id (003, same deferred-FK
-- pattern 001 used for episode_events.event_id → events(id)) and
-- events.agent_id (002: "their parent tables (sessions, agents) land in
-- migrations 003/004").
-- Forward-only step 4 of N; rollback in 004_agents.down.sql.

-- ============================================================
-- AGENTS — pull (live MCP) vs push (materialized instruction file)
-- ============================================================
CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT UNIQUE NOT NULL,
    adapter_type    TEXT NOT NULL CHECK (adapter_type IN ('pull', 'push')),
    capabilities    JSONB,
    context_budget  INTEGER NOT NULL DEFAULT 4000,
    output_file     TEXT,
    output_format   TEXT,
    created_at      TIMESTAMPTZ DEFAULT now()
);

INSERT INTO agents (name, adapter_type, context_budget, output_file, output_format) VALUES
    ('claude',    'pull', 10000, NULL, NULL),
    ('opencode',  'pull', 10000, NULL, NULL),
    ('codex',     'pull', 10000, NULL, NULL),
    ('antigravity','pull', 10000, NULL, NULL),
    ('copilot',   'push', 8000,  '.github/copilot-instructions.md', 'markdown'),
    ('cursor',    'push', 6000,  '.cursorrules', 'text'),
    ('windsurf',  'push', 6000,  '.windsurfrules', 'text');

-- ============================================================
-- PROJECT AGENTS — per-project enablement + config overrides
-- ============================================================
-- A row's absence means "no explicit per-project setting" (the project
-- inherits the global agent defaults); a row with enabled = false opts the
-- project out. config carries per-project overrides as JSON, e.g.
-- '{"context_budget": 5000}' — read by internal/store/agents.go
-- EffectiveBudget, not enforced in SQL.
CREATE TABLE project_agents (
    project_id  UUID NOT NULL REFERENCES projects(id),
    agent_id    UUID NOT NULL REFERENCES agents(id),
    enabled     BOOLEAN DEFAULT true,
    config      JSONB,
    PRIMARY KEY (project_id, agent_id)
);

-- ============================================================
-- DEFERRED FKS — agent_id columns left bare by 002/003
-- ============================================================
-- 003 left session_participants.agent_id unenforced pending this table
-- (see ADR-012 consequences); 002 left events.agent_id bare for the same
-- reason. No ON DELETE action: session history and the event log outlive
-- any agent row, and agent rows are never deleted (only seed-inserted).
ALTER TABLE session_participants
    ADD CONSTRAINT fk_session_participants_agent
    FOREIGN KEY (agent_id) REFERENCES agents(id);

ALTER TABLE events
    ADD CONSTRAINT fk_events_agent
    FOREIGN KEY (agent_id) REFERENCES agents(id);
