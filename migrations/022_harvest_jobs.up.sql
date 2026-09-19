-- 022_harvest_jobs: raw turn batches queued for OpenRouter extraction.
-- Daemon uploads turns; API shows raw rows immediately; worker processes
-- one job at a time (SQS and/or DB poll) without heuristic PROPOSED scrap.

CREATE TABLE IF NOT EXISTS harvest_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    dedupe_key      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','processing','done','failed','duplicate')),
    source          TEXT NOT NULL DEFAULT '',
    turns           JSONB NOT NULL DEFAULT '[]',
    raw_preview     TEXT NOT NULL DEFAULT '',
    turn_count      INTEGER NOT NULL DEFAULT 0,
    result_count    INTEGER NOT NULL DEFAULT 0,
    error           TEXT NOT NULL DEFAULT '',
    provider        TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    UNIQUE (project_id, dedupe_key)
);

CREATE INDEX IF NOT EXISTS harvest_jobs_status_created_idx
    ON harvest_jobs (status, created_at ASC)
    WHERE status IN ('queued', 'processing');

CREATE INDEX IF NOT EXISTS harvest_jobs_project_created_idx
    ON harvest_jobs (project_id, created_at DESC);
