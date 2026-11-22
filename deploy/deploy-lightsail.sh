#!/usr/bin/env bash
# deploy/deploy-lightsail.sh — Automated deploy script to update Lightsail
set -euo pipefail

HOST="${LIGHTSAIL_HOST:-13.205.221.207}"
USER="${LIGHTSAIL_USER:-ubuntu}"
KEY="${LIGHTSAIL_KEY:-$HOME/.ssh/central-memory-key.pem}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> Syncing secrets from AWS Secrets Manager to Lightsail..."
if command -v python3 >/dev/null 2>&1; then
  python3 "$SCRIPT_DIR/sync-secrets.py"
elif command -v python >/dev/null 2>&1; then
  python "$SCRIPT_DIR/sync-secrets.py"
else
  echo "[-] Warning: python/python3 not found; proceeding without automatic secrets sync."
fi

echo "==> Deploying Central Memory to Lightsail ($HOST)..."
ssh -i "$KEY" -o StrictHostKeyChecking=no "$USER@$HOST" << 'REMOTE_COMMANDS'
  set -euo pipefail
  cd /opt/central-memory/repo
  echo "Pulling latest changes..."
  git pull origin master
  echo "Rebuilding and restarting stack..."
  # Project name must stay "repo" to match existing container names (nexus-*).
  # If postgres already exists under another compose project label, rebuild server only.
  COMPOSE=(docker compose -p repo --env-file .env -f deploy/docker-compose.lightsail.yml)
  "${COMPOSE[@]}" build
  if docker ps -a --format '{{.Names}}' | grep -qx nexus-postgres; then
    echo "postgres container already present — recreating server only"
    # Embeddings sidecar first (no-op when unchanged) so the server's startup
    # backfill finds it. Its failure must never block the API deploy.
    "${COMPOSE[@]}" up -d --no-deps embeddings \
      || echo "WARN: embeddings sidecar did not start; API falls back to HashEmbed + keyword search"
    "${COMPOSE[@]}" up -d --no-deps --force-recreate server
  else
    "${COMPOSE[@]}" up -d
  fi
  echo "Stack updated successfully!"
REMOTE_COMMANDS

echo "==> Verifying service health..."
sleep 5
curl -fsS "http://$HOST/healthz" | grep -q '"ok":true'
echo "==> Service is healthy and live!"
