-- A rollback is only possible when all existing indexes fit in INT.
ALTER TABLE session_turns ALTER COLUMN idx TYPE INTEGER;
