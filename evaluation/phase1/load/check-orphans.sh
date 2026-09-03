#!/usr/bin/env bash
# Zero-orphan gate for the Phase 1 load run (doc §13.1 / PHASE1_MVP_COMPLETION
# §7: "Zero orphan environments sustained during + 1h after the run —
# reaper_orphans_found_total == 0").
#
# Run it TWICE: immediately after the load run, then again ≥ 1h later. Both
# must report PASS. The gate is:
#   increase(orchestrator_reaper_orphans_found_total[<window>]) == 0
# which this script approximates by snapshotting the counter, sleeping the
# window, and asserting it did not move — plus a live namespace cross-check
# against the reaper's record if a kubeconfig is available.
#
# Usage:
#   evaluation/phase1/load/check-orphans.sh [WINDOW_SECONDS]
#
# Env:
#   LOAD_ORCH_METRICS_URL   orchestrator /metrics base (default http://localhost:9090)
#   ORPHAN_WINDOW_SECONDS   observation window (default 3600 = 1h; arg overrides)
#   KUBECONFIG_HOST         optional kubeconfig for the namespace cross-check
#   ORPHAN_NS_PREFIX        workspace namespace prefix (default "env-")
set -euo pipefail

ORCH_METRICS="${LOAD_ORCH_METRICS_URL:-http://localhost:9090}"
WINDOW="${1:-${ORPHAN_WINDOW_SECONDS:-3600}}"
KUBECONFIG_HOST="${KUBECONFIG_HOST:-.local/k3s-output/kubeconfig.yaml}"
NS_PREFIX="${ORPHAN_NS_PREFIX:-env-}"

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
pass() { printf '\033[1;32mPASS: %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

metric() {
  # $1 = metric name; prints the summed value across all label sets, or 0
  curl -sf "${ORCH_METRICS}/metrics" \
    | awk -v m="$1" '$0 !~ /^#/ && index($0, m) == 1 { s += $NF } END { printf "%d", s+0 }'
}

log "snapshotting orchestrator_reaper_orphans_found_total"
BEFORE="$(metric orchestrator_reaper_orphans_found_total)"
DESTROYED_BEFORE="$(metric orchestrator_reaper_destroyed_total)"
echo "orphans_found_total   = ${BEFORE}"
echo "reaper_destroyed_total = ${DESTROYED_BEFORE}"

if [ "$BEFORE" -ne 0 ]; then
  fail "orphans_found_total is already ${BEFORE} (must be 0). The load run leaked at least one namespace the reaper had no record of."
fi

log "observing for ${WINDOW}s (the sustained window)"
SLEPT=0
STEP=60
while [ "$SLEPT" -lt "$WINDOW" ]; do
  sleep "$STEP"
  SLEPT=$((SLEPT + STEP))
  NOW="$(metric orchestrator_reaper_orphans_found_total)"
  if [ "$NOW" -ne "$BEFORE" ]; then
    fail "orphans_found_total moved ${BEFORE} -> ${NOW} at +${SLEPT}s. Non-zero increase over the window = orphan environments."
  fi
  printf '  +%4ds  orphans_found_total=%s  (unchanged)\n' "$SLEPT" "$NOW"
done

AFTER="$(metric orchestrator_reaper_orphans_found_total)"
[ "$AFTER" -eq "$BEFORE" ] || fail "orphans_found_total ${BEFORE} -> ${AFTER} over the window"

log "live namespace cross-check"
if command -v kubectl >/dev/null 2>&1 && [ -f "$KUBECONFIG_HOST" ]; then
  LEFT="$(KUBECONFIG="$KUBECONFIG_HOST" kubectl get ns -o name 2>/dev/null \
    | sed 's#namespace/##' | grep -c "^${NS_PREFIX}" || true)"
  echo "workspace namespaces still present (${NS_PREFIX}*): ${LEFT}"
  if [ "${LEFT:-0}" -ne 0 ]; then
    KUBECONFIG="$KUBECONFIG_HOST" kubectl get ns 2>/dev/null | grep "^${NS_PREFIX}" || true
    fail "${LEFT} workspace namespace(s) still present after the run + window. Cross-check with the reaper's env.environment records before calling this PASS."
  fi
else
  echo "(kubectl or kubeconfig not available — skipping the namespace cross-check; the counter gate above still holds)"
fi

pass "increase(orchestrator_reaper_orphans_found_total[${WINDOW}s]) == 0 and no leaked workspace namespaces"
echo
echo "Record this output in evaluation/phase1/results/loadtest-<date>.md."
echo "Run again ≥ 1h after the load run for the 'sustained + 1h' half of the gate."
