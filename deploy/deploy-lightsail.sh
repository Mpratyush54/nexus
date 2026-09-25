#!/usr/bin/env bash
# deploy/deploy-lightsail.sh — Automated deploy script to update Lightsail
set -euo pipefail

HOST="${LIGHTSAIL_HOST:-13.205.221.207}"
USER="${LIGHTSAIL_USER:-ubuntu}"
KEY="${LIGHTSAIL_KEY:-$HOME/.ssh/central-memory-key.pem}"

echo "==> Deploying Central Memory to Lightsail ($HOST)..."
ssh -i "$KEY" -o StrictHostKeyChecking=no "$USER@$HOST" << 'REMOTE_COMMANDS'
  set -euo pipefail
  cd /opt/central-memory/repo
  echo "Pulling latest changes..."
  git pull origin master
  echo "Rebuilding and restarting stack..."
  docker compose up -d --build
  echo "Stack updated successfully!"
REMOTE_COMMANDS

echo "==> Verifying service health..."
sleep 5
curl -fsS "http://$HOST/healthz" | grep -q '"ok":true'
echo "==> Service is healthy and live!"
