#!/usr/bin/env bash
# Phase 3 1.4 / B9 — end-to-end verification of the platform Git host.
#
#   FORGEJO_BASE_URL=http://localhost:3300 \
#   FORGEJO_ADMIN_TOKEN=<token from bootstrap-admin.sh> \
#     infra/git-hosting/scripts/verify-git-hosting.sh
#
# Proves the exact operations practice-core/src/modules/project/git.service.ts
# performs on project enrol + per milestone:
#   1. create a per-learner org
#   2. create the learner's project repo, seeded (auto_init)
#   3. push a commit over HTTP with a scoped token
#   4. read the commit history + a file's content back via the API
#   5. confirm the repo is NOT inside any learner sandbox (retention: it
#      survives the post-project nuke) — checked structurally here by
#      asserting the repo lives on the platform host, not a sandbox URL
#   6. clean up the throwaway org/repo
set -euo pipefail

BASE="${FORGEJO_BASE_URL:-http://localhost:3300}"
TOKEN="${FORGEJO_ADMIN_TOKEN:?set FORGEJO_ADMIN_TOKEN (see bootstrap-admin.sh)}"
API="$BASE/api/v1"
ADMIN_USER="${FORGEJO_ADMIN_USER:-platform-admin}"

ORG="verify-learner-$$"
REPO="verify-proj-$$"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; cleanup' EXIT

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
pass() { printf '\033[1;32mPASS: %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

api() { curl -sf -H "Authorization: token $TOKEN" -H 'content-type: application/json' "$@"; }

cleanup() {
  curl -s -XDELETE -H "Authorization: token $TOKEN" "$API/repos/$ORG/$REPO" -o /dev/null || true
  curl -s -XDELETE -H "Authorization: token $TOKEN" "$API/orgs/$ORG" -o /dev/null || true
}

log "health"
curl -sf "$BASE/api/healthz" >/dev/null || fail "Forgejo /api/healthz not OK at $BASE"
pass "Forgejo reachable"

log "1. create per-learner org '$ORG'"
api -XPOST "$API/orgs" -d "{\"username\":\"$ORG\",\"visibility\":\"private\"}" >/dev/null \
  || fail "could not create org"
pass "org created"

log "2. create seeded project repo '$ORG/$REPO'"
api -XPOST "$API/orgs/$ORG/repos" \
  -d "{\"name\":\"$REPO\",\"private\":true,\"auto_init\":true,\"default_branch\":\"main\"}" >/dev/null \
  || fail "could not create repo"
CLONE_URL="$(api "$API/repos/$ORG/$REPO" | python3 -c 'import sys,json;print(json.load(sys.stdin)["clone_url"])')"
pass "repo created — clone_url=$CLONE_URL"

log "3. push a milestone commit over HTTP + token"
AUTH_URL="$(printf '%s' "$CLONE_URL" | sed "s#://#://$ADMIN_USER:$TOKEN@#")"
git clone -q "$AUTH_URL" "$WORK/repo"
cd "$WORK/repo"
printf '# Design\n\nMilestone 1 architecture design.\n' > DESIGN.md
git -c user.email=verify@platform -c user.name=verify add -A
git -c user.email=verify@platform -c user.name=verify commit -q -m "milestone: design"
git push -q origin main
cd - >/dev/null
pass "commit pushed"

log "4. read commit history + file content back via the API"
COMMITS="$(api "$API/repos/$ORG/$REPO/commits?limit=10")"
N="$(printf '%s' "$COMMITS" | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))')"
[ "$N" -ge 2 ] || fail "expected >=2 commits, got $N"
HEAD_MSG="$(printf '%s' "$COMMITS" | python3 -c 'import sys,json;print(json.load(sys.stdin)[0]["commit"]["message"].strip())')"
[ "$HEAD_MSG" = "milestone: design" ] || fail "HEAD commit message wrong: '$HEAD_MSG'"
CONTENT="$(api "$API/repos/$ORG/$REPO/contents/DESIGN.md" | python3 -c 'import sys,json,base64;print(base64.b64decode(json.load(sys.stdin)["content"]).decode())')"
printf '%s' "$CONTENT" | grep -q "Milestone 1 architecture design" || fail "DESIGN.md content not readable via API"
pass "history ($N commits) + file content readable — this is what the viva generator (3.8) consumes"

log "5. retention / isolation check"
case "$CLONE_URL" in
  "$BASE"/*) pass "repo is hosted on the platform Git host, not a learner sandbox — survives the post-project nuke by construction" ;;
  *) fail "clone_url $CLONE_URL is not on the platform host $BASE" ;;
esac

log "6. cleanup (also runs on any earlier failure via trap)"
cleanup
pass "throwaway org/repo removed"

echo
pass "platform Git host verified end-to-end (create org → seed repo → push → read history → retention)"
