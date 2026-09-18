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
    # URL-encode user/password (issue #145): reserved characters in
    # operator-supplied credentials used to break the DSN (the old code
    # relied on secrets.tf charset limits, which do not cover manual
    # secrets). Subshell keeps LC_ALL=C byte semantics local to encoding.
    urlencode() (
      LC_ALL=C
      str=$1
      out=
      while [ -n "$str" ]; do
        c=$(printf '%.1s' "$str")
        str=${str#?}
        case $c in
          [A-Za-z0-9_.~-]) out=$out$c ;;
          *) out=$out$(printf '%%%02X' "'$c") ;;
        esac
      done
      printf '%s' "$out"
    )
    ENC_USER=$(urlencode "$DB_USER")
    ENC_PASSWORD=$(urlencode "${DB_PASSWORD:-}")
    DATABASE_URL="postgres://${ENC_USER}:${ENC_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=${DB_SSLMODE}"
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

# Apply *.up.sql files in lexical order, versioned + locked (issue #150):
# the same ledger the Go runner (internal/store RunMigrations +
# schema_migrations + advisory lock) uses. Schema and version rule mirror
# db.go schemaMigrationsDDL/migrationVersion exactly (version = filename
# minus ".up.sql"):
#   1. Ensure the ledger table.
#   2. Read applied versions.
#   3. Apply ONLY pending files, one transaction each, inside a single psql
#      session holding pg_advisory_lock for the whole run — concurrent
#      boots serialize instead of racing replays.
# A repeat boot with everything applied is lock + no-op (no replays).
# 001 enables pgcrypto + pgvector, so the Aurora master must pre-provision
# extension privilege (CREATE EXTENSION needs rds_superuser or equivalent —
# see deploy/rds-notes.md) before first boot.
if command -v psql >/dev/null 2>&1; then
  if [ -d "$MIGRATIONS_DIR" ]; then
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"
    applied=$(psql "$DATABASE_URL" -tAX -c "SELECT version FROM schema_migrations" 2>/dev/null || true)
    script=$(mktemp)
    {
      echo "SELECT pg_advisory_lock(hashtext('central-memory-migrations'));"
      for f in "$MIGRATIONS_DIR"/*.up.sql; do
        [ -e "$f" ] || break
        base=$(basename "$f")
        ver=${base%.up.sql}
        case "
$applied
" in
          *"
$ver
"*) echo "-- skip $base (already applied)";;
          *)
             echo "\\echo 'migrate.sh: applying $f'"
             echo "BEGIN;"
             echo "\\ir $f"
             echo "INSERT INTO schema_migrations (version) VALUES ('$ver') ON CONFLICT DO NOTHING;"
             echo "COMMIT;"
             ;;
        esac
      done
      echo "SELECT pg_advisory_unlock(hashtext('central-memory-migrations'));"
    } > "$script"
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -f "$script"
    rm -f "$script"
  else
    echo "migrate.sh: WARNING: migrations dir $MIGRATIONS_DIR missing; skipping." >&2
  fi
else
  echo "migrate.sh: WARNING: psql not found; skipping migration step." >&2
fi

exec "$@"
