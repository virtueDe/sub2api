#!/usr/bin/env bash
# Update only the Sub2API application container on a remote host.
# The caller must provide a Docker-capable user (docker group or equivalent).

set -Eeuo pipefail

DEPLOY_DIR=${1:?deployment directory is required}
COMPOSE_FILE=${2:?compose file is required}
IMAGE=${3:?image is required}

cd "$DEPLOY_DIR"
test -f "$COMPOSE_FILE"
test -n "$IMAGE"

compose() {
  docker compose -f "$COMPOSE_FILE" "$@"
}

compose config --quiet

# Keep a recoverable copy of the environment before changing the image pin.
timestamp=$(date +%Y%m%d-%H%M%S)
if [ -f .env ]; then
  cp -- .env ".env.backup.${timestamp}"
else
  : > .env
fi

tmp_env=".env.tmp.${timestamp}"
awk -v image="$IMAGE" '
  BEGIN { updated = 0 }
  /^SUB2API_IMAGE=/ {
    print "SUB2API_IMAGE=" image
    updated = 1
    next
  }
  { print }
  END {
    if (!updated) print "SUB2API_IMAGE=" image
  }
' .env > "$tmp_env"
mv -- "$tmp_env" .env

compose pull sub2api
compose up -d --no-deps sub2api
echo "Deployment started: ${IMAGE}"
