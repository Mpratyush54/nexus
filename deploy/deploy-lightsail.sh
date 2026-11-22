#!/usr/bin/env bash
# deploy/deploy-lightsail.sh — roll the Lightsail API to a prebuilt ECR image.
#
# The box no longer builds anything: GitHub Actions builds and pushes the
# server image to ECR, and this script makes the box pull that exact image.
#
# Required env:
#   NEXUS_SERVER_IMAGE  <registry>/central-memory-server@sha256:<digest>
#                       (a :tag also works for manual runs)
#   ECR_TOKEN           output of `aws ecr get-login-password`; sent to the box
#                       over SSH stdin only, never on a command line
# Optional env:
#   EXPECT_COMMIT       git SHA /healthz must report (CI passes github.sha)
#   LIGHTSAIL_HOST / LIGHTSAIL_USER / LIGHTSAIL_KEY
#   REMOTE_TIMEOUT      seconds for the on-box pull+swap (default 900)
set -euo pipefail

HOST="${LIGHTSAIL_HOST:-13.205.221.207}"
USER="${LIGHTSAIL_USER:-ubuntu}"
KEY="${LIGHTSAIL_KEY:-$HOME/.ssh/central-memory-key.pem}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${NEXUS_SERVER_IMAGE:?set NEXUS_SERVER_IMAGE to the ECR image (registry/repo@sha256:...)}"
EXPECT_COMMIT="${EXPECT_COMMIT:-}"
REMOTE_TIMEOUT="${REMOTE_TIMEOUT:-900}"

if [ -z "${ECR_TOKEN:-}" ]; then
  echo "ECR_TOKEN is empty (export ECR_TOKEN=\$(aws ecr get-login-password --region ap-south-1))" >&2
  exit 1
fi
# Values below are interpolated into a remote command line: allow only plain
# image-reference / hex characters.
if ! [[ "$IMAGE" =~ ^[A-Za-z0-9._/-]+(:[A-Za-z0-9._-]+)?(@sha256:[0-9a-f]{64})?$ ]]; then
  echo "NEXUS_SERVER_IMAGE has unexpected characters: $IMAGE" >&2
  exit 1
fi
if ! [[ "$EXPECT_COMMIT" =~ ^[0-9a-f]*$ ]]; then
  echo "EXPECT_COMMIT must be a hex git SHA" >&2
  exit 1
fi
REGISTRY="${IMAGE%%/*}"

# Keepalives + connect timeout so a dead link fails in about a minute instead
# of hanging the job.
SSH=(ssh -i "$KEY" -o StrictHostKeyChecking=no -o BatchMode=yes
  -o ConnectTimeout=20 -o ServerAliveInterval=15 -o ServerAliveCountMax=4
  "$USER@$HOST")

echo "==> Syncing secrets from AWS Secrets Manager to Lightsail..."
if command -v python3 >/dev/null 2>&1; then
  python3 "$SCRIPT_DIR/sync-secrets.py"
elif command -v python >/dev/null 2>&1; then
  python "$SCRIPT_DIR/sync-secrets.py"
else
  echo "[-] Warning: python/python3 not found; proceeding without automatic secrets sync."
fi

echo "==> Uploading compose file to $HOST..."
timeout 60 "${SSH[@]}" "mkdir -p /opt/central-memory/deploy \
  && cat > /opt/central-memory/deploy/docker-compose.lightsail.yml.tmp \
  && mv /opt/central-memory/deploy/docker-compose.lightsail.yml.tmp /opt/central-memory/deploy/docker-compose.lightsail.yml" \
  < "$SCRIPT_DIR/docker-compose.lightsail.yml"

echo "==> Logging the box in to ECR ($REGISTRY)..."
printf '%s\n' "$ECR_TOKEN" \
  | timeout 60 "${SSH[@]}" "docker login --username AWS --password-stdin $REGISTRY" >/dev/null

echo "==> Pulling and switching to $IMAGE on $HOST..."
timeout "$REMOTE_TIMEOUT" "${SSH[@]}" "bash -s -- '$IMAGE' '$EXPECT_COMMIT' '$REGISTRY'" \
  < "$SCRIPT_DIR/lightsail-remote.sh"

echo "==> Verifying public health endpoint..."
for _ in 1 2 3 4 5 6; do
  if curl -fsS --max-time 10 "http://$HOST/healthz" | grep -q '"ok":true'; then
    echo "==> Service is healthy and live!"
    exit 0
  fi
  sleep 5
done
echo "Public /healthz check failed (on-box check passed); investigate networking." >&2
exit 1
