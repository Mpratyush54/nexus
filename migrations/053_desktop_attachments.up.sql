-- Images a browser sends with a desktop turn (relay protocol 2). Kept out of
-- the job payload so status reads and live streams never carry image bytes;
-- only the worker's claim returns them. Rows expire with the job (7 days).
ALTER TABLE desktop_execution_jobs ADD COLUMN IF NOT EXISTS attachments JSONB;
