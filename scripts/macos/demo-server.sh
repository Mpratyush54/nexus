#!/usr/bin/env bash
# Starts a throwaway Nexus central server for macOS CI screenshots and demos.
#
#   Postgres (Homebrew, no Docker) in $RUNNER_TEMP/pg on 127.0.0.1:$PGPORT,
#   database "nexus", pgvector built against it when Homebrew has none, then
#   the Go server (cmd/server) on port $NEXUS_DEMO_PORT with migrations applied
#   at boot. Waits for /healthz and /readyz.
#
# Run from the repository root (or GITHUB_WORKSPACE). Fill it with
# scripts/macos/demo-seed.sh afterwards.
#
# Environment (all optional):
#   PG_FORMULA       Homebrew formula            (default postgresql@16)
#   PGPORT           Postgres port               (default 5432)
#   NEXUS_DEMO_PORT  server port                 (default 8787)
#   PGVECTOR_REF     pgvector tag to build       (default v0.8.0)
#   RUNNER_TEMP      scratch root                (default a mktemp dir)
#
# Leaves behind: logs/server.log, logs/postgres.log, logs/server.pid, and
# (on GitHub Actions) NEXUS_DEMO_SERVER / NEXUS_DEMO_SERVER_PID /
# NEXUS_DEMO_PGDATA / NEXUS_DEMO_PG_BIN in $GITHUB_ENV.
#
# Note: the server listens on ":$NEXUS_DEMO_PORT" (all interfaces); cmd/server
# has no bind-address setting. Clients use 127.0.0.1.
set -euo pipefail

cd "${GITHUB_WORKSPACE:-.}"
REPO=$PWD
mkdir -p logs dist

PG_FORMULA=${PG_FORMULA:-postgresql@16}
PGPORT=${PGPORT:-5432}
PORT=${NEXUS_DEMO_PORT:-8787}
PGVECTOR_REF=${PGVECTOR_REF:-v0.8.0}
TEMP_ROOT=${RUNNER_TEMP:-$(mktemp -d "${TMPDIR:-/tmp}/nexus-demo.XXXXXX")}
PGDATA_DIR="$TEMP_ROOT/pg"
SERVER_URL="http://127.0.0.1:$PORT"

log() { echo "[demo-server $(date +%T)] $*"; }
die() {
  echo "::error::demo-server: $*" >&2
  for f in logs/postgres.log logs/server.log; do
    if [ -s "$f" ]; then echo "---- tail $f ----" >&2; tail -n 60 "$f" >&2; fi
  done
  exit 1
}

# ---- 1. Postgres from Homebrew ---------------------------------------------
command -v brew > /dev/null || die "Homebrew is required"
if ! brew list --versions "$PG_FORMULA" > /dev/null 2>&1; then
  log "brew install $PG_FORMULA"
  HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 brew install "$PG_FORMULA"
fi
PG_BIN="$(brew --prefix "$PG_FORMULA")/bin"
[ -x "$PG_BIN/pg_ctl" ] || die "no pg_ctl in $PG_BIN"
log "postgres: $("$PG_BIN/postgres" --version)"

# migrations/001_initial.up.sql runs CREATE EXTENSION vector. Homebrew's
# pgvector formula only targets its newest PostgreSQL kegs, so build it
# against this one when the extension is missing.
PG_SHAREDIR=$("$PG_BIN/pg_config" --sharedir)
if [ ! -f "$PG_SHAREDIR/extension/vector.control" ]; then
  log "building pgvector $PGVECTOR_REF for $PG_FORMULA"
  SRC="$TEMP_ROOT/pgvector-src"
  rm -rf "$SRC"
  git clone --quiet --depth 1 --branch "$PGVECTOR_REF" https://github.com/pgvector/pgvector.git "$SRC"
  # OPTFLAGS="" avoids -march=native (not portable on Apple clang).
  make -C "$SRC" PG_CONFIG="$PG_BIN/pg_config" OPTFLAGS="" > logs/pgvector-build.log 2>&1 \
    || { tail -n 40 logs/pgvector-build.log >&2; die "pgvector build failed"; }
  make -C "$SRC" PG_CONFIG="$PG_BIN/pg_config" OPTFLAGS="" install >> logs/pgvector-build.log 2>&1 \
    || { tail -n 40 logs/pgvector-build.log >&2; die "pgvector install failed"; }
fi
[ -f "$PG_SHAREDIR/extension/vector.control" ] || die "pgvector is not installed in $PG_SHAREDIR/extension"

# ---- 2. Cluster on 127.0.0.1 ------------------------------------------------
if [ ! -f "$PGDATA_DIR/PG_VERSION" ]; then
  rm -rf "$PGDATA_DIR"
  mkdir -p "$PGDATA_DIR"
  log "initdb $PGDATA_DIR"
  "$PG_BIN/initdb" -D "$PGDATA_DIR" -U postgres -A trust -E UTF8 --no-locale > logs/initdb.log 2>&1 \
    || { cat logs/initdb.log >&2; die "initdb failed"; }
fi
if "$PG_BIN/pg_ctl" -D "$PGDATA_DIR" status > /dev/null 2>&1; then
  log "postgres already running"
else
  log "starting postgres on 127.0.0.1:$PGPORT"
  # TCP only: no Unix socket, so long temp paths cannot hit the socket limit.
  "$PG_BIN/pg_ctl" -D "$PGDATA_DIR" -l "$REPO/logs/postgres.log" -w -t 60 \
    -o "-c listen_addresses=127.0.0.1 -c port=$PGPORT -c unix_socket_directories=''" start \
    || die "postgres did not start"
fi
PSQL=("$PG_BIN/psql" -h 127.0.0.1 -p "$PGPORT" -U postgres -v ON_ERROR_STOP=1 -X -q)
if [ "$("${PSQL[@]}" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'nexus'")" != "1" ]; then
  "$PG_BIN/createdb" -h 127.0.0.1 -p "$PGPORT" -U postgres nexus || die "createdb nexus failed"
fi
# Fail here (clear message) rather than in the server's first migration.
"${PSQL[@]}" -d nexus -c "CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pgcrypto;" \
  || die "could not enable the vector/pgcrypto extensions"

# ---- 3. Build the server ------------------------------------------------------
log "go build ./cmd/server"
go build -o dist/central-server ./cmd/server

# ---- 4. Run it --------------------------------------------------------------
if [ -f logs/server.pid ] && kill -0 "$(cat logs/server.pid)" 2> /dev/null; then
  log "stopping the previous server (pid $(cat logs/server.pid))"
  kill "$(cat logs/server.pid)" || true
  sleep 1
fi
JWT_SECRET_VALUE=$(openssl rand -hex 32)
DATABASE_URL_VALUE="postgres://postgres@127.0.0.1:$PGPORT/nexus?sslmode=disable"
log "starting central-server on :$PORT"
# No mail settings: signup answers with dev_code. No OpenRouter key: memory
# extraction uses its heuristic fallback. Hash embeddings need no network.
nohup env \
  -u RESEND_API_KEY -u SMTP_HOST -u SMTP_PASSWORD -u OPENROUTER_API_KEY \
  -u DB_HOST -u DB_PORT -u DB_NAME -u DB_USER -u DB_PASSWORD -u DB_SSLMODE \
  PORT="$PORT" \
  DATABASE_URL="$DATABASE_URL_VALUE" \
  MIGRATIONS_DIR="$REPO/migrations" \
  JWT_SECRET="$JWT_SECRET_VALUE" \
  CENTRAL_MEMORY_LOCAL_DEV=1 \
  CENTRAL_EMBEDDING_PROVIDER=hash \
  ./dist/central-server > logs/server.log 2>&1 &
SERVER_PID=$!
echo "$SERVER_PID" > logs/server.pid

# ---- 5. Wait for health and readiness ---------------------------------------
ready=0
for _ in $(seq 1 240); do
  kill -0 "$SERVER_PID" 2> /dev/null || die "central-server exited during startup"
  if curl -fsS -m 2 "$SERVER_URL/healthz" > /dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done
[ "$ready" = 1 ] || die "/healthz did not answer within 120 s"
READY_BODY=""
for _ in $(seq 1 60); do
  if READY_BODY=$(curl -fsS -m 2 "$SERVER_URL/readyz" 2> /dev/null); then
    ready=2
    break
  fi
  sleep 0.5
done
if [ "$ready" != 2 ]; then
  curl -sS -m 2 "$SERVER_URL/readyz" >&2 || true
  die "/readyz did not report ready"
fi
log "ready: $READY_BODY"
grep -q "migrations current" logs/server.log || log "warning: no 'migrations current' line in logs/server.log"

if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "NEXUS_DEMO_SERVER=$SERVER_URL"
    echo "NEXUS_DEMO_SERVER_PID=$SERVER_PID"
    echo "NEXUS_DEMO_PGDATA=$PGDATA_DIR"
    echo "NEXUS_DEMO_PG_BIN=$PG_BIN"
  } >> "$GITHUB_ENV"
fi
echo "NEXUS_DEMO_SERVER=$SERVER_URL"
log "server pid $SERVER_PID, log logs/server.log; stop with: kill \$(cat logs/server.pid); \"$PG_BIN/pg_ctl\" -D \"$PGDATA_DIR\" stop"
