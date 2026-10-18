-- Harvester source-position indexes contain Unix milliseconds plus an
-- ordinal. INT silently worked in MemStore but overflowed PostgreSQL.
ALTER TABLE session_turns ALTER COLUMN idx TYPE BIGINT;
