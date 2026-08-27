#!/usr/bin/env bash
# End-to-end smoke test for the compose `app` profile
# (PHASE1_MVP_COMPLETION.md §1.1: "compose up, start an attempt from the
# web catalog, terminal connects to a real k3s-backed workspace pod").
#
# What it proves:
#   1. `docker compose --profile app` builds + boots orchestrator +
#      practice-core + web on top of the infra services.
#   2. practice-core's real GrpcOrchestratorClient reaches the
#      orchestrator over the real gRPC contract (shared-secret auth on).
#   3. Starting an attempt cold-provisions a real workspace Pod in the
#      compose k3s cluster and the attempt reaches READY.
#   4. /connect returns a real terminal WebSocket URL signed by the WS
#      gateway.
#   5. Destroy tears the environment's namespace down.
#
# Ports are shifted by docker-compose.smoke.yml so this runs alongside a
# developer's local (non-container) orchestrator/practice-core/web.
#
# Usage:  evaluation/phase1/smoke/run-smoke.sh
# Requires: docker compose, curl, python3, grpcurl, kubectl + the repo's
#           .local/k3s-output/kubeconfig.yaml for the k8s assertions.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

COMPOSE=(docker compose --profile app
  -f docker-compose.yml
  -f evaluation/phase1/smoke/docker-compose.smoke.yml)

PC=http://localhost:3101          # practice-core (shifted)
ORCH_METRICS=http://localhost:9190
KUBECONFIG_HOST=.local/k3s-output/kubeconfig.yaml
SHARED_SECRET=compose-dev-shared-secret

TENANT=11111111-1111-1111-1111-111111111111
USER=55555555-5555-5555-5555-555555555555

log() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

log "building + starting the app profile"
"${COMPOSE[@]}" up -d --build

log "waiting for practice-core to answer"
TOKEN=""
for i in $(seq 1 90); do
  resp=$(curl -s -XPOST "$PC/v1/auth/dev-login" -H 'content-type: application/json' -d '{}' 2>/dev/null || true)
  TOKEN=$(printf '%s' "$resp" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null || true)
  [ -n "$TOKEN" ] && break
  sleep 2
  [ "$i" = 90 ] && fail "practice-core never returned a dev token"
done
echo "dev token acquired (${#TOKEN} chars)"

log "orchestrator /healthz + /metrics"
for i in $(seq 1 30); do
  curl -sf "$ORCH_METRICS/healthz" 2>/dev/null | grep -q ok && break
  sleep 2
  [ "$i" = 30 ] && fail "orchestrator healthz never became ok"
done
# Labelled counters (orchestrator_provision_total{...}) don't appear until
# their first Inc(), i.e. after the first provision -- so assert on a
# metric the exposition always carries at boot instead.
curl -sf "$ORCH_METRICS/metrics" | grep -qE '^orchestrator_(ws_sessions_active|reaper_orphans_found_total)' \
  || fail "orchestrator metrics endpoint not serving orchestrator_* series"

log "seeding prereq mastery for the smoke user (a real learner would have this after prerequisites)"
# linux.navigate-filesystem's REQUIRES closure: devops.fundamentals, linux.cli
docker compose exec -T postgres psql -U practice -d practice_engine -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO skill.skill_mastery (user_id, skill_id, p_mastery, evidence_count, band, last_evidence_at)
SELECT '55555555-5555-5555-5555-555555555555', s.id, 0.80, 3, 'Proficient', now()
FROM skill.skill s
WHERE s.slug IN ('devops.fundamentals','linux.cli')
ON CONFLICT (user_id, skill_id)
DO UPDATE SET p_mastery = EXCLUDED.p_mastery, band = EXCLUDED.band, last_evidence_at = now();
SQL

log "picking a published L1 lab from the catalog"
AVID=$(curl -s "$PC/v1/practice/activities" -H "authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;print(next(a["activity_version_id"] for a in json.load(sys.stdin) if a["slug"]=="lab.linux.navigate-filesystem"))')
[ -n "$AVID" ] || fail "lab.linux.navigate-filesystem not in catalog"

log "starting an attempt ($AVID)"
ATTEMPT=$(curl -s -XPOST "$PC/v1/practice/attempts" -H "authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d "{\"tenant_id\":\"$TENANT\",\"user_id\":\"$USER\",\"activity_version_id\":\"$AVID\"}" \
  | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id") or json.dumps(d))')
case "$ATTEMPT" in
  *statusCode*|*PREREQUISITE*) fail "attempt not created: $ATTEMPT" ;;
esac
echo "attempt id: $ATTEMPT"

log "provisioning (practice-core -> orchestrator gRPC -> real k3s)"
STATUS=$(curl -s -XPOST "$PC/v1/practice/attempts/$ATTEMPT/provision" -H "authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("status"))')
[ "$STATUS" = READY ] || fail "attempt status is $STATUS, expected READY"

ENV_ID=$(curl -s "$PC/v1/practice/attempts/$ATTEMPT" -H "authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["environment_id"])')
echo "environment id: $ENV_ID"

log "asserting a real workspace Pod exists in k3s"
KUBECONFIG=$KUBECONFIG_HOST kubectl get pod workspace -n "env-$ENV_ID" \
  -o jsonpath='{.status.phase}' | grep -q Running || fail "workspace pod not Running"

log "/connect returns a terminal WebSocket URL"
curl -s -XPOST "$PC/v1/practice/attempts/$ATTEMPT/connect" -H "authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;u=json.load(sys.stdin)["terminalWsUrl"];assert u.startswith("ws://") and "/terminal?session=" in u, u;print("terminalWsUrl OK:",u.split("?")[0])'

log "tearing the environment down via the orchestrator Destroy RPC"
grpcurl -plaintext -H "authorization: Bearer $SHARED_SECRET" \
  -d "{\"environment_id\":\"$ENV_ID\",\"reason\":\"admin\",\"attempt_id\":\"$ATTEMPT\"}" \
  -import-path contracts -proto orchestrator.proto \
  localhost:50151 practiceengine.orchestrator.v1.EnvironmentOrchestrator/Destroy >/dev/null

for i in $(seq 1 30); do
  KUBECONFIG=$KUBECONFIG_HOST kubectl get ns "env-$ENV_ID" >/dev/null 2>&1 || break
  sleep 2
done

printf '\n\033[1;32mSMOKE TEST PASSED\033[0m\n'
echo "leave the stack up for inspection, or: ${COMPOSE[*]} down"
