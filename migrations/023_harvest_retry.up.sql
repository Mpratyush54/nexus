-- 023_harvest_retry: backoff retries for transient OpenRouter failures (429/5xx).

ALTER TABLE harvest_jobs
    ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS harvest_jobs_queued_ready_idx
    ON harvest_jobs (created_at ASC)
    WHERE status = 'queued';
