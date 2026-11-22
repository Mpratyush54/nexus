#!/usr/bin/env bash
# deploy/lightsail-remote.sh — runs ON the Lightsail box, streamed by
# deploy/deploy-lightsail.sh as `ssh host bash -s -- <image> <commit> <registry>`.
#
# Swaps the nexus-server container to a prebuilt ECR image. Nothing is built
# here (the 1 GB box cannot build Go without swapping for 30+ minutes) and
# Postgres is never stopped or recreated.
#
#   1. docker pull <image> (already logged in to ECR by the caller), logout.
#   2. Tag the running server image as nexus-server:rollback.
#   3. Recreate ONLY the server container on the new image.
#   4. Wait for /healthz (and the expected commit, when given).
#   5. Unhealthy -> recreate the server on nexus-server:rollback, exit 1.
#   6. Healthy -> record the image, drop older server images, prune.
set -euo pipefail

IMAGE="${1:?usage: lightsail-remote.sh <image> [expected-commit] [registry]}"
EXPECT_COMMIT="${2:-}"
REGISTRY="${3:-}"

BASE=/opt/central-memory
ENV_FILE="$BASE/repo/.env"                       # written by sync-secrets.py
STATE_DIR="$BASE/deploy"
COMPOSE_FILE="$STATE_DIR/docker-compose.lightsail.yml"  # uploaded by the caller
ROLLBACK_TAG=nexus-server:rollback
HEALTH_URL=http://127.0.0.1:8080/healthz

ecr_logout() {
  if [ -n "$REGISTRY" ]; then
    docker logout "$REGISTRY" >/dev/null 2>&1 || true
  fi
}
trap ecr_logout EXIT

compose() {
  local image="$1"
  shift
  # Project name must stay "repo": existing containers and the pgdata volume
  # (repo_pgdata) were created under it.
  NEXUS_SERVER_IMAGE="$image" docker compose -p repo --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

# wait_healthy [commit] — poll /healthz for up to ~4 minutes (migrations run
# on boot). With a commit, the response must also name that commit so an old
# container still answering can never pass.
wait_healthy() {
  local want="${1:-}" body i
  for i in $(seq 1 80); do
    body="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
    if printf '%s' "$body" | grep -q '"ok":true'; then
      if [ -z "$want" ] || printf '%s' "$body" | grep -q "$want"; then
        return 0
      fi
    fi
    sleep 3
  done
  return 1
}

[ -f "$ENV_FILE" ] || { echo "missing $ENV_FILE (run sync-secrets.py)" >&2; exit 1; }
[ -f "$COMPOSE_FILE" ] || { echo "missing $COMPOSE_FILE" >&2; exit 1; }

echo "Pulling $IMAGE ..."
timeout 600 docker pull "$IMAGE"
ecr_logout

# The image is built for linux/amd64 on GitHub's runners; refuse a mismatch
# before touching the running container.
img_arch="$(docker image inspect -f '{{.Architecture}}' "$IMAGE")"
case "$(uname -m)" in
  x86_64) host_arch=amd64 ;;
  aarch64) host_arch=arm64 ;;
  *) host_arch="$(uname -m)" ;;
esac
if [ "$img_arch" != "$host_arch" ]; then
  echo "image architecture $img_arch does not match host $host_arch; not deploying" >&2
  exit 1
fi

prev_id="$(docker inspect -f '{{.Image}}' nexus-server 2>/dev/null || true)"
if [ -n "$prev_id" ]; then
  docker tag "$prev_id" "$ROLLBACK_TAG"
  echo "Previous server image $prev_id tagged $ROLLBACK_TAG"
else
  echo "No running nexus-server container; nothing to roll back to."
fi

echo "Switching server to $IMAGE ..."
if docker ps -a --format '{{.Names}}' | grep -qx nexus-postgres; then
  compose "$IMAGE" up -d --no-build --no-deps --force-recreate server
else
  # First install on a fresh box: bring up Postgres too.
  compose "$IMAGE" up -d --no-build
fi

if ! wait_healthy "$EXPECT_COMMIT"; then
  echo "New server image failed /healthz. Last log lines:" >&2
  docker logs --tail 40 nexus-server 2>&1 | sed 's/^/  | /' >&2 || true
  if [ -n "$prev_id" ]; then
    echo "Rolling back to $ROLLBACK_TAG ($prev_id) ..." >&2
    compose "$ROLLBACK_TAG" up -d --no-build --no-deps --force-recreate server
    if wait_healthy; then
      echo "Rollback healthy; production is on the previous image." >&2
    else
      echo "ROLLBACK ALSO UNHEALTHY: manual action needed (see deploy/README.md)." >&2
    fi
  fi
  exit 1
fi
echo "Server healthy on $IMAGE"

# Record what is running so manual compose commands know the image.
if [ -f "$STATE_DIR/current-image" ]; then
  cp "$STATE_DIR/current-image" "$STATE_DIR/previous-image"
fi
printf '%s\n' "$IMAGE" > "$STATE_DIR/current-image"

# Cleanup: keep the running image and the rollback image; drop other ECR
# pulls and the old on-box builds (repo-server), then dangling layers and
# build cache left by the old build-on-box flow.
new_id="$(docker inspect -f '{{.Image}}' nexus-server)"
repo_ref="${IMAGE%%@*}"
case "${repo_ref##*/}" in
  *:*) repo_ref="${repo_ref%:*}" ;;
esac
for repo in "$repo_ref" repo-server; do
  for id in $(docker images -q --no-trunc "$repo" | sort -u); do
    if [ "$id" = "$new_id" ] || [ "$id" = "$prev_id" ]; then
      continue
    fi
    docker rmi -f "$id" >/dev/null 2>&1 || true
  done
done
docker image prune -f >/dev/null 2>&1 || true
docker builder prune -f >/dev/null 2>&1 || true
echo "Deploy complete."
