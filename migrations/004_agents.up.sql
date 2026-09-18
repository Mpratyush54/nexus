-- 004_agents.up.sql: Multi-agent registry + per-project enablement (Phase 4.1).
--
-- Depends on: 001_initial (projects), 003_sessions (sessions, if present).
-- Idempotent: CREATE IF NOT EXISTS + DO NOTHING seeds so reruns are safe.
-- Seed inserts never overwrite existing rows (issue #119): hand-tuned
-- context_budgets survive re-runs; use UpsertAgent to change a definition.
-- Agent names align with adapters/registry.go (claude, opencode, codex,
-- antigravity, copilot, cursor, windsurf). Pull agents are served live via
-- MCP (internal/mcp); push agents are served via generated instruction
-- files (internal/materializer).

-- ============================================================
-- AGENTS — identity, capabilities, budgets, push targets
-- ============================================================
CREATE TABLE IF NOT EXISTS agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT UNIQUE NOT NULL,
    adapter_type    TEXT NOT NULL CHECK (adapter_type IN ('pull', 'push')),
    capabilities    JSONB NOT NULL DEFAULT '{}',
    context_budget  INTEGER NOT NULL DEFAULT 4000 CHECK (context_budget > 0),
    output_file     TEXT,
    output_format   TEXT CHECK (output_format IS NULL OR output_format IN ('markdown', 'text')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Pull agents have no output_file (served live over MCP).
-- Push agents map to the instruction file the materializer regenerates.
INSERT INTO agents (name, adapter_type, capabilities, context_budget, output_file, output_format) VALUES
    ('claude',     'pull', '{"mcp": true, "tools": ["memory_search", "memory_write", "memory_reflect", "episode_search"]}', 10000, NULL, NULL),
    ('opencode',   'pull', '{"mcp": true, "tools": ["memory_search", "memory_write", "memory_reflect", "episode_search"]}', 10000, NULL, NULL),
    ('codex',      'pull', '{"mcp": true, "tools": ["memory_search", "memory_write", "episode_search"]}',                  10000, NULL, NULL),
    ('antigravity','pull', '{"mcp": true, "tools": ["memory_search", "memory_write", "episode_search"]}',                  10000, NULL, NULL),
    ('copilot',    'push', '{"instruction_file": true}', 8000, '.github/copilot-instructions.md', 'markdown'),
    ('cursor',     'push', '{"instruction_file": true}', 6000, '.cursorrules', 'text'),
    ('windsurf',   'push', '{"instruction_file": true}', 6000, '.windsurfrules', 'text')
ON CONFLICT (name) DO NOTHING;

-- ============================================================
-- PROJECT_AGENTS — per-project enablement + config overrides
-- ============================================================
CREATE TABLE IF NOT EXISTS project_agents (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    config      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_project_agents_project ON project_agents(project_id);
