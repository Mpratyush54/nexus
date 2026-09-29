-- 028_session_provenance.down.sql
DROP TABLE IF EXISTS session_snapshots;
DROP TABLE IF EXISTS session_tool_executions;
DROP TABLE IF EXISTS session_file_operations;
DROP TYPE IF EXISTS file_op_type;
