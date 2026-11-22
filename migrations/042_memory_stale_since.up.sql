-- A confirmed memory about a file can go out of date when that file changes.
-- stale_since records when the desktop reported a change to one of the
-- memory's files_affected after the memory was written. NULL means fresh.
-- Re-confirming or editing the memory clears it.
ALTER TABLE memory_items ADD COLUMN IF NOT EXISTS stale_since TIMESTAMPTZ;
