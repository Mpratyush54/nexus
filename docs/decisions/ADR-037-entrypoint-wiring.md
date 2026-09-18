# ADR-037 (entrypoint) - Wire PostgresStore into cmd/server

- **ADR ID:** ADR-037-entrypoint-wiring
- **Date:** 2026-09-18
- **Author:** ParthKhandelwal537
- **Issue:** #37 Postgres-backed server.Store adapter (closing piece)
- **Status:** Accepted

## Context

The store adapter itself (`PostgresStore`, vector search) landed via
#65, but `cmd/server/main.go` still booted `stubStore` (fail-closed
500s) unconditionally — the adapter was never used.

## Decisions

1. `DATABASE_URL` (or assembled `DB_*` parts, reusing the existing
   `resolveConfig` contract) selects `newPostgresServer`: connect,
   `RunMigrations` (idempotent, safe alongside `deploy/migrate.sh`),
   hub attach + `StartBridge(ctx, hub, "")` for all projects.
2. Unset DSN keeps the stub + 503 `/auth/login` + `"store":"stub"`
   readyz label (existing audit tests pin this; `newHandler`
   signature unchanged). Real backend reports `"store":"postgres"`
   and serves the server's own login handler.
3. Boot without DB fails hard (clear error, container restarts) rather
   than serving 500s silently.
