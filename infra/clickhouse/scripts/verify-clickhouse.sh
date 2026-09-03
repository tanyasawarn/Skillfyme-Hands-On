#!/usr/bin/env bash
# Phase 3 1.5 — round-trip verify of the analytics store.
#   1. insert a synthetic attempt_events row
#   2. confirm it lands in practice_analytics.attempt_events
#   3. confirm the hourly rollup materialised view aggregated it
set -euo pipefail

CH_URL="${CLICKHOUSE_URL:-http://localhost:8123}"
CH_USER="${CLICKHOUSE_USER:-default}"
CH_PASS="${CLICKHOUSE_PASSWORD:-}"

q() {
  curl -sf "${CH_URL}" \
    --user "${CH_USER}:${CH_PASS}" \
    --data-binary "$1"
}

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
pass() { printf '\033[1;32mPASS: %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

log "ping"
curl -sf "${CH_URL}/ping" | grep -q "Ok" || fail "ClickHouse not answering at ${CH_URL}"
pass "ClickHouse up"

log "schema present"
q "EXISTS TABLE practice_analytics.attempt_events" | grep -q 1 || fail "attempt_events table missing"
q "EXISTS TABLE practice_analytics.attempt_hourly" | grep -q 1 || fail "attempt_hourly table missing"
pass "attempt_events + attempt_hourly + MV exist"

ATTEMPT="verify-$(date +%s)"
HOUR_NOW="$(date -u +%Y-%m-%dT%H:00:00)"

log "insert a synthetic MILESTONE_GATED event for ${ATTEMPT}"
q "INSERT INTO practice_analytics.attempt_events
   (attempt_id, tenant_id, activity_id, course_id, user_id, seq, type, actor, occurred_at, payload)
   VALUES
   ('${ATTEMPT}', 'ten-1', 'act-1', 'crs-1', 'usr-1', 1, 'MILESTONE_SUBMITTED', 'LEARNER', now64(3), '{}'),
   ('${ATTEMPT}', 'ten-1', 'act-1', 'crs-1', 'usr-1', 2, 'MILESTONE_GATED', 'SYSTEM', now64(3), '{\"outcome\":\"GATED_PASS\"}')"
pass "2 rows inserted"

log "raw stream read-back"
N="$(q "SELECT count() FROM practice_analytics.attempt_events WHERE attempt_id = '${ATTEMPT}'")"
[ "$N" = "2" ] || fail "expected 2 raw rows, got ${N}"
pass "raw events land (${N} rows)"

log "hourly rollup"
# the MV writes on insert; the merged view aggregates the AggregateFunction state
ROLLUP="$(q "SELECT events, milestones_gated FROM practice_analytics.attempt_hourly_merged
             WHERE attempt_id = '${ATTEMPT}' AND hour = toDateTime('${HOUR_NOW}')")"
echo "  rollup row (events, milestones_gated): ${ROLLUP:-<none>}"
EVENTS="$(printf '%s' "$ROLLUP" | awk '{print $1}')"
GATED="$(printf '%s' "$ROLLUP" | awk '{print $2}')"
[ "${EVENTS:-0}" -ge 2 ] || fail "hourly rollup events = ${EVENTS:-0}, expected >= 2"
[ "${GATED:-0}" -ge 1 ] || fail "hourly rollup milestones_gated = ${GATED:-0}, expected >= 1"
pass "hourly rollup aggregated the events (events=${EVENTS}, milestones_gated=${GATED})"

log "cleanup"
q "ALTER TABLE practice_analytics.attempt_events DELETE WHERE attempt_id = '${ATTEMPT}'" >/dev/null || true
pass "synthetic rows removed"

echo
pass "ClickHouse analytics store verified end-to-end (insert → raw read → hourly rollup)"
