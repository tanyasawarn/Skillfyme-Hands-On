#!/usr/bin/env bash
# run-content-ci.sh [activity-selector ...]
#
# The single entry point the content-ci CI job (and a human on the
# runner box) calls. It:
#
#   1. seeds the skill graph into Postgres (content-ci.ts loads activity
#      YAML which references skill slugs; lint-adjacent checks and the
#      spec reader need them present),
#   2. runs practice-core/scripts/content-ci.ts against the given
#      selectors (or the whole library if none given),
#   3. exits with content-ci.ts's own exit code (0 = all selected
#      activities passed null-path + golden-path + flake + timing +
#      cost; non-zero = at least one failed, or an explicitly-named
#      activity had no runnable golden path).
#
# Required env (set by the workflow / the runner's systemd unit):
#   DATABASE_URL            Postgres the orchestrator + seeds share
#   ORCHESTRATOR_GRPC_ADDRESS   default localhost:50051
#   ORCHESTRATOR_SHARED_SECRET  must match the running orchestrator's
#   REDIS_URL, NATS_URL     (content-ci.ts itself doesn't need these, but
#                            the orchestrator it talks to does)
# Optional:
#   CI_FLAKE_RUNS  default 5   CI_BUDGET_USD  default 0.08
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT/practice-core"

: "${DATABASE_URL:?DATABASE_URL must be set}"
: "${ORCHESTRATOR_SHARED_SECRET:?ORCHESTRATOR_SHARED_SECRET must be set (must match the running orchestrator)}"
export ORCHESTRATOR_GRPC_ADDRESS="${ORCHESTRATOR_GRPC_ADDRESS:-localhost:50051}"

echo "==> content-ci: seeding skill graph"
npx ts-node -r tsconfig-paths/register scripts/seed-skills.ts
npx ts-node -r tsconfig-paths/register scripts/seed-skills-genai.ts
if [ -f scripts/seed-skills-sre.ts ]; then
  npx ts-node -r tsconfig-paths/register scripts/seed-skills-sre.ts
fi

echo "==> content-ci: orchestrator at ${ORCHESTRATOR_GRPC_ADDRESS}"
if [ "$#" -eq 0 ]; then
  echo "==> content-ci: FULL LIBRARY run"
else
  echo "==> content-ci: selected -> $*"
fi

# content-ci.ts exit code is the result of this whole script.
exec npx ts-node -r tsconfig-paths/register scripts/content-ci.ts "$@"
