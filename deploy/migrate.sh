#!/bin/sh
# migrate.sh — run Postgres migrations on container boot, then exec the server.
# Used as ENTRYPOINT in Dockerfile.server: /migrate.sh /central-server
# Templates/notes only — no terraform apply, no Go changes.
set -eu

if [ "${1:-}" = "" ]; then
  echo "usage: migrate.sh <server-binary> [args...]" >&2
  exit 2
fi

if [ -z "${DATABASE_URL:-}" ]; then
  echo "migrate.sh: DATABASE_URL is not set; refusing to boot without a database." >&2
  exit 1
fi

MIGRATIONS_DIR="${MIGRATIONS_DIR:-/migrations}"

# Apply *.up.sql files in lexical order with psql (single transaction each).
# 001 enables pgcrypto + pgvector, so Aurora must allow the vector extension
# (see deploy/rds-notes.md) before first boot.
if command -v psql >/dev/null 2>&1; then
  if [ -d "$MIGRATIONS_DIR" ]; then
    for f in "$MIGRATIONS_DIR"/*.up.sql; do
      [ -e "$f" ] || break
      echo "migrate.sh: applying $f"
      psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$f"
    done
  else
    echo "migrate.sh: WARNING: migrations dir $MIGRATIONS_DIR missing; skipping." >&2
  fi
else
  echo "migrate.sh: WARNING: psql not found; skipping migration step." >&2
fi

exec "$@"
