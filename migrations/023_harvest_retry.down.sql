DROP INDEX IF EXISTS harvest_jobs_queued_ready_idx;
ALTER TABLE harvest_jobs
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS attempt_count;
