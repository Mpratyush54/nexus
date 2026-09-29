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
  docker compose -p repo --env-file .env -f deploy/docker-compose.lightsail.yml up -d --build
  echo "Stack updated successfully!"
REMOTE_COMMANDS

echo "==> Verifying service health..."
sleep 5
curl -fsS "http://$HOST/healthz" | grep -q '"ok":true'
echo "==> Service is healthy and live!"
