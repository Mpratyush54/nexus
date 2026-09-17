-- Rollback for 008_processor_election: drops the partial unique index so
-- multiple online designatees are again possible (pre-036 behaviour).
-- The workspaces columns are untouched (owned by 001_initial).

DROP INDEX IF EXISTS uq_workspaces_designated_processor_online;
