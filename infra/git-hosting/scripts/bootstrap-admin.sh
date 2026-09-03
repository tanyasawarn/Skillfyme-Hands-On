#!/usr/bin/env bash
# Phase 3 1.4 / B9. Idempotently create the Forgejo admin user + an API
# token, and print an export line for practice-core.
#
#   infra/git-hosting/scripts/bootstrap-admin.sh
#
# Safe to re-run: the user is created only if absent; a fresh token is
# minted each run (old ones stay valid — revoke in the UI if needed).
set -euo pipefail

CONTAINER="${FORGEJO_CONTAINER:-practice-engine-forgejo-1}"
ADMIN_USER="${FORGEJO_ADMIN_USER:-platform-admin}"
ADMIN_PASS="${FORGEJO_ADMIN_PASSWORD:-platform-admin-dev-pw-123}"
ADMIN_EMAIL="${FORGEJO_ADMIN_EMAIL:-platform-admin@practice.local}"
TOKEN_NAME="${FORGEJO_TOKEN_NAME:-practice-core-$(date +%s)}"
BASE_URL="${FORGEJO_BASE_URL:-http://localhost:3300}"

log() { printf '\033[1;34m== %s\033[0m\n' "$*"; }

if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  echo "Forgejo container '$CONTAINER' is not running." >&2
  echo "Start it: docker compose --profile git-hosting -f docker-compose.yml -f infra/git-hosting/compose/docker-compose.git-hosting.yml up -d" >&2
  exit 1
fi

log "ensuring admin user '$ADMIN_USER'"
if docker exec -u git "$CONTAINER" forgejo admin user list --admin 2>/dev/null | awk '{print $2}' | grep -qx "$ADMIN_USER"; then
  echo "admin user already exists"
else
  docker exec -u git "$CONTAINER" forgejo admin user create \
    --admin --username "$ADMIN_USER" --password "$ADMIN_PASS" \
    --email "$ADMIN_EMAIL" --must-change-password=false
fi

log "minting an API token '$TOKEN_NAME' (scopes: write:admin, write:organization, write:repository, write:user)"
TOKEN="$(docker exec -u git "$CONTAINER" forgejo admin user generate-access-token \
  --username "$ADMIN_USER" --token-name "$TOKEN_NAME" \
  --scopes 'write:admin,write:organization,write:repository,write:user' --raw)"

if [ -z "$TOKEN" ]; then
  echo "token generation returned empty" >&2
  exit 1
fi

log "done — add these to practice-core/.env"
echo
echo "FORGEJO_BASE_URL=$BASE_URL"
echo "FORGEJO_ADMIN_TOKEN=$TOKEN"
echo
echo "(verify: infra/git-hosting/scripts/verify-git-hosting.sh)"
