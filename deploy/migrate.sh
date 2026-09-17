#!/bin/sh
# migrate.sh — run Postgres migrations on container boot, then exec the server.
# Used as ENTRYPOINT in Dockerfile.server: migrate.sh /usr/local/bin/central-server
#
# DANGER: forward-only. Applies migrations/*.up.sql ONLY. NEVER apply
# *.down.sql in any shared environment: 001.down drops all tables +
# extensions, 004.down DELETEs seeds, 005.down drops branch_id (destroys CoW
# tags). Down-migrations exist for local dev reset only and are NOT baked
# into the image (see Dockerfile.server). There is no confirmation wrapper
# on purpose: this script cannot see *.down.sql even if asked.
#
# DSN contract (single source of truth: deploy/secrets-notes.md; mirrored in
# cmd/server/main.go resolveDatabaseURL — keep the two in sync):
#   1. DATABASE_URL verbatim when set (ECS prod, secrets-notes layout).
#   2. Else assembled from DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD +
#      DB_SSLMODE (ECS discrete secrets; compose dev). DB_SSLMODE defaults to
#      "require" (prod); compose overrides to "disable" with
#      CENTRAL_MEMORY_LOCAL_DEV=1 for local Postgres.
set -eu

if [ "${1:-}" = "" ]; then
  echo "usage: migrate.sh <server-binary> [args...]" >&2
  exit 2
fi

# Assemble DATABASE_URL from discrete parts when it is not set outright.
if [ -z "${DATABASE_URL:-}" ]; then
  if [ -n "${DB_HOST:-}" ] && [ -n "${DB_NAME:-}" ] && [ -n "${DB_USER:-}" ]; then
    DB_PORT="${DB_PORT:-5432}"
    DB_SSLMODE="${DB_SSLMODE:-require}"
    # NOTE: user/password are URL-encoded minimally (special chars in generated
    # secrets are limited to -_ by secrets.tf override_special).
    DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD:-}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=${DB_SSLMODE}"
    export DATABASE_URL
    echo "migrate.sh: assembled DATABASE_URL from DB_* parts (sslmode=${DB_SSLMODE})." >&2
  else
    echo "migrate.sh: DATABASE_URL is not set and DB_HOST/DB_NAME/DB_USER are incomplete; refusing to boot without a database." >&2
    exit 1
  fi
fi

# Prod guardrail: refuse a non-require sslmode unless this is an explicit
# local-dev boot (compose sets CENTRAL_MEMORY_LOCAL_DEV=1). Mirrors the
# /readyz gate in cmd/server/main.go.
case "$DATABASE_URL" in
  *sslmode=require*)
    ;;
  *)
    if [ "${CENTRAL_MEMORY_LOCAL_DEV:-}" != "1" ]; then
      echo "migrate.sh: DSN sslmode is not require and CENTRAL_MEMORY_LOCAL_DEV != 1; refusing (Aurora runs rds.force_ssl=1)." >&2
      exit 1
    fi
    echo "migrate.sh: WARNING: non-require sslmode with CENTRAL_MEMORY_LOCAL_DEV=1 (local dev only)." >&2
    ;;
esac

MIGRATIONS_DIR="${MIGRATIONS_DIR:-/migrations}"

# Apply *.up.sql files in lexical order with psql (single transaction each).
# 001 enables pgcrypto + pgvector, so the Aurora master must pre-provision
# extension privilege (CREATE EXTENSION needs rds_superuser or equivalent —
# see deploy/rds-notes.md) before first boot.
if command -v psql >/dev/null 2>&1; then
  if [ -d "$MIGRATIONS_DIR" ]; then
    matched=0
    for f in "$MIGRATIONS_DIR"/*.up.sql; do
      [ -e "$f" ] || break
      matched=1
      echo "migrate.sh: applying $f"
      psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$f"
    done
    if [ "$matched" = "0" ]; then
      echo "migrate.sh: WARNING: no *.up.sql in $MIGRATIONS_DIR; skipping." >&2
    fi
  else
    echo "migrate.sh: WARNING: migrations dir $MIGRATIONS_DIR missing; skipping." >&2
  fi
else
  echo "migrate.sh: WARNING: psql not found; skipping migration step." >&2
fi

exec "$@"
