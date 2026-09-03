#!/usr/bin/env bash
# Seed prerequisite mastery for load-test users so the eligibility prereq gate
# passes under load. Same fixture stance as evaluation/phase1/smoke/run-smoke.sh's
# single-user seed (a real learner reaches this state by completing
# prerequisites) — this is a test fixture, not a product-code bypass.
#
# For the local python shake-out against the compose `app` profile. The real
# multi-node run (0.4) uses provisioned load-test tenants instead.
#
# Usage:
#   evaluation/phase1/load/seed-load-users.sh [N]   # default N = LOAD_VUS or 200
#
# It seeds the demo user the smoke test uses (55555555-…) — with dev-login
# every VU authenticates as that same user, so one row set covers all VUs.
# If your build's dev-login mints distinct users, extend the SQL to a
# generate_series over the load-user id range.
set -euo pipefail

N="${1:-${LOAD_VUS:-200}}"
PSQL_DB="${LOAD_PSQL_DB:-practice_engine}"
PSQL_USER="${LOAD_PSQL_USER:-practice}"

echo "seeding prereq mastery for the load-test demo user (covers all dev-login VUs; N=${N} noted for reference)"

docker compose exec -T postgres psql -U "$PSQL_USER" -d "$PSQL_DB" -v ON_ERROR_STOP=1 <<'SQL'
-- linux.navigate-filesystem's REQUIRES closure: devops.fundamentals, linux.cli
INSERT INTO skill.skill_mastery (user_id, skill_id, p_mastery, evidence_count, band, last_evidence_at)
SELECT '55555555-5555-5555-5555-555555555555', s.id, 0.80, 3, 'Proficient', now()
FROM skill.skill s
WHERE s.slug IN ('devops.fundamentals','linux.cli')
ON CONFLICT (user_id, skill_id)
DO UPDATE SET p_mastery = EXCLUDED.p_mastery, band = EXCLUDED.band, last_evidence_at = now();
SQL

echo "done."
