#!/usr/bin/env bash
# Namespace-churn soak at 3× projected peak.
#
# PLAN_PHASE3_PROJECTS.md G2 / Phase3_Stages.md 0.3. Pass criteria and the
# rationale are in evaluation/phase1/soak/README.md. Executing this on a real
# multi-node cluster and committing the result is 0.4 (T1) / 0.5 (T2).
#
# Drives EnvironmentOrchestrator/Provision + /Destroy directly over gRPC at
# 3×SOAK_PEAK sustained concurrency, each env cycling every
# SOAK_ENV_LIFETIME_SEC, then holds SOAK_HOLD_MIN watching for orphans.
#
# Requires: grpcurl, curl, awk, python3; kubectl + a kubeconfig for the
#           namespace cross-check (optional but strongly recommended).
set -euo pipefail

# ---- parameters ---------------------------------------------------------------
SOAK_PEAK="${SOAK_PEAK:-50}"
SOAK_MULTIPLIER="${SOAK_MULTIPLIER:-3}"
SOAK_DURATION_MIN="${SOAK_DURATION_MIN:-60}"
SOAK_HOLD_MIN="${SOAK_HOLD_MIN:-60}"
SOAK_ENV_LIFETIME_SEC="${SOAK_ENV_LIFETIME_SEC:-45}"
SOAK_TIER="${SOAK_TIER:-T1}"
SOAK_BLUEPRINT="${SOAK_BLUEPRINT:-bp.test.v1}"
SOAK_ORCH_ADDR="${SOAK_ORCH_ADDR:-localhost:50051}"
SOAK_ORCH_METRICS_URL="${SOAK_ORCH_METRICS_URL:-http://localhost:9090}"
SOAK_SHARED_SECRET="${SOAK_SHARED_SECRET:-compose-dev-shared-secret}"
SOAK_KUBECONFIG="${SOAK_KUBECONFIG:-.local/k3s-output/kubeconfig.yaml}"
SOAK_NS_PREFIX="${SOAK_NS_PREFIX:-env-}"
SOAK_PROTO_ROOT="${SOAK_PROTO_ROOT:-contracts}"

CONCURRENCY=$(( SOAK_PEAK * SOAK_MULTIPLIER ))
case "$SOAK_TIER" in
  T1) TIER_ENUM="TIER_T1_SHARED_CONTAINER" ;;
  T2) TIER_ENUM="TIER_T2_ISOLATED_MICROVM" ;;
  *) echo "SOAK_TIER must be T1 or T2 (got '$SOAK_TIER')" >&2; exit 2 ;;
esac

WORKDIR="$(mktemp -d)"
PROVISIONED="$WORKDIR/provisioned"   # one line per successful Provision: <env_id>
DESTROYED="$WORKDIR/destroyed"       # one line per successful Destroy:   <env_id>
FAILED_PROV="$WORKDIR/failed_prov"
FAILED_DEST="$WORKDIR/failed_dest"
STOP_FLAG="$WORKDIR/stop"
: > "$PROVISIONED"; : > "$DESTROYED"; : > "$FAILED_PROV"; : > "$FAILED_DEST"

cleanup() { rm -rf "$WORKDIR" 2>/dev/null || true; }
trap cleanup EXIT
trap 'touch "$STOP_FLAG"' INT TERM

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
pass() { printf '\033[1;32mPASS: %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; FAILURES=$((FAILURES+1)); }

FAILURES=0

grpc() {
  grpcurl -plaintext -H "authorization: Bearer ${SOAK_SHARED_SECRET}" \
    -import-path "$SOAK_PROTO_ROOT" -proto orchestrator.proto \
    "$@"
}

metric() {
  curl -sf "${SOAK_ORCH_METRICS_URL}/metrics" \
    | awk -v m="$1" '$0 !~ /^#/ && index($0, m) == 1 { s += $NF } END { printf "%d", s+0 }'
}

k8s_ns_count() {
  command -v kubectl >/dev/null 2>&1 && [ -f "$SOAK_KUBECONFIG" ] || { echo "n/a"; return; }
  KUBECONFIG="$SOAK_KUBECONFIG" kubectl get ns -o name 2>/dev/null \
    | sed 's#namespace/##' | grep -c "^${SOAK_NS_PREFIX}" || true
}

k8s_api_latency_ms() {
  command -v kubectl >/dev/null 2>&1 && [ -f "$SOAK_KUBECONFIG" ] || { echo "n/a"; return; }
  local t0 t1
  t0=$(python3 -c 'import time;print(int(time.time()*1000))')
  KUBECONFIG="$SOAK_KUBECONFIG" kubectl get --raw='/readyz' >/dev/null 2>&1 || true
  t1=$(python3 -c 'import time;print(int(time.time()*1000))')
  echo $(( t1 - t0 ))
}

# ---- one churn worker -------------------------------------------------------
# Provision -> sleep lifetime -> Destroy, looping until STOP_FLAG appears.
#
# attempt_id MUST be a real UUID: env.environment.attempt_id is a uuid column,
# so Destroy's ownership check (attempt_id == env.environment.attempt_id) can
# never match a non-uuid synthetic id — Provision accepts the string but the
# row stores something else and every Destroy then returns PermissionDenied.
gen_uuid() {
  if command -v uuidgen >/dev/null 2>&1; then uuidgen | tr 'A-Z' 'a-z'; else
    python3 -c 'import uuid;print(uuid.uuid4())'
  fi
}

churn_worker() {
  local wid="$1" n=0
  while [ ! -f "$STOP_FLAG" ]; do
    n=$((n+1))
    local attempt_id
    attempt_id="$(gen_uuid)"
    local out env_id
    out="$(grpc -d "{\"attempt_id\":\"${attempt_id}\",\"blueprint_id\":\"${SOAK_BLUEPRINT}\",\"blueprint_version\":\"v1\",\"tier\":\"${TIER_ENUM}\",\"ttl_minutes\":10,\"idle_timeout_minutes\":10}" \
      "$SOAK_ORCH_ADDR" practiceengine.orchestrator.v1.EnvironmentOrchestrator/Provision 2>>"$WORKDIR/grpc.err" || true)"
    env_id="$(printf '%s' "$out" | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin); print(d.get("environmentId",""))
except Exception:
  print("")' 2>/dev/null)"

    if [ -z "$env_id" ]; then
      echo "${attempt_id}" >> "$FAILED_PROV"
      sleep 1
      continue
    fi
    echo "$env_id" >> "$PROVISIONED"

    # live for the lifetime, but wake early if we've been told to stop
    local slept=0
    while [ "$slept" -lt "$SOAK_ENV_LIFETIME_SEC" ] && [ ! -f "$STOP_FLAG" ]; do
      sleep 3; slept=$((slept+3))
    done

    # Destroy (idempotent server-side). Retry a couple of times — a failed
    # Destroy that is never retried is exactly the leak this soak hunts.
    # A clean teardown is either an empty response ({}) or already_destroyed.
    # PermissionDenied ("does not own this environment") means the row is
    # present but owned by a different attempt id — treat as NOT confirmed so
    # it shows up as a failed Destroy and the orphan/namespace gate is the
    # backstop that catches any real leak.
    local d ok=0
    for _ in 1 2 3; do
      d="$(grpc -d "{\"environment_id\":\"${env_id}\",\"reason\":\"admin\",\"attempt_id\":\"${attempt_id}\"}" \
        "$SOAK_ORCH_ADDR" practiceengine.orchestrator.v1.EnvironmentOrchestrator/Destroy 2>>"$WORKDIR/grpc.err" || true)"
      if printf '%s' "$d" | grep -qE 'alreadyDestroyed|already_destroyed|^\{\}$|^\s*\{\s*\}\s*$'; then ok=1; break; fi
      sleep 2
    done
    if [ "$ok" -eq 1 ]; then echo "$env_id" >> "$DESTROYED"; else echo "$env_id" >> "$FAILED_DEST"; fi
  done

  # drain: destroy anything this worker provisioned in its last cycle that
  # didn't get torn down before the stop flag
}

# ---- preflight ------------------------------------------------------------
log "preflight"
for bin in grpcurl curl awk python3; do
  command -v "$bin" >/dev/null 2>&1 || { echo "missing required tool: $bin" >&2; exit 2; }
done
grpc "$SOAK_ORCH_ADDR" list >/dev/null 2>&1 \
  || { echo "cannot reach orchestrator gRPC at ${SOAK_ORCH_ADDR}" >&2; exit 2; }
info "orchestrator reachable at ${SOAK_ORCH_ADDR}"
if ! curl -sf -m 5 "${SOAK_ORCH_METRICS_URL}/metrics" | grep -q '^orchestrator_reaper_orphans_found_total'; then
  echo "WARNING: ${SOAK_ORCH_METRICS_URL}/metrics is not serving orchestrator_reaper_orphans_found_total." >&2
  echo "         The counter-based orphan gate (criterion 1) will read 0/0 and cannot fail." >&2
  echo "         Point SOAK_ORCH_METRICS_URL at the orchestrator's real Prometheus port" >&2
  echo "         (ORCHESTRATOR_METRICS_PORT, default 9090) before the 0.4/0.5 run." >&2
fi
info "tier=${SOAK_TIER} blueprint=${SOAK_BLUEPRINT}"
info "concurrency = ${SOAK_PEAK} × ${SOAK_MULTIPLIER} = ${CONCURRENCY} envs alive at once"
info "env lifetime = ${SOAK_ENV_LIFETIME_SEC}s  =>  ~$(( CONCURRENCY * 3600 / SOAK_ENV_LIFETIME_SEC )) provision+destroy pairs/hour"
info "churn ${SOAK_DURATION_MIN}m, then hold ${SOAK_HOLD_MIN}m"

log "baseline"
BASE_ORPHANS="$(metric orchestrator_reaper_orphans_found_total)"
BASE_REAPER_DEST="$(metric orchestrator_reaper_destroyed_total)"
BASE_PROV_TOTAL="$(metric orchestrator_provision_total)"
BASE_NS="$(k8s_ns_count)"
info "reaper_orphans_found_total  = ${BASE_ORPHANS}"
info "reaper_destroyed_total      = ${BASE_REAPER_DEST}"
info "provision_total             = ${BASE_PROV_TOTAL}"
info "env-* namespaces            = ${BASE_NS}"
info "k8s /readyz latency         = $(k8s_api_latency_ms) ms"
if [ "$BASE_ORPHANS" -ne 0 ]; then
  echo "NOTE: baseline orphans_found_total is ${BASE_ORPHANS} (not 0). The gate below" >&2
  echo "      measures the DELTA over the run, but a non-zero baseline means an" >&2
  echo "      earlier leak is unaccounted for — investigate before trusting a PASS." >&2
fi

# ---- churn phase -------------------------------------------------------
log "churn phase: ${SOAK_DURATION_MIN}m at ${CONCURRENCY}-way concurrency"
PIDS=()
for w in $(seq 1 "$CONCURRENCY"); do
  churn_worker "$w" &
  PIDS+=("$!")
  # slight stagger so all workers don't Provision on the same tick
  sleep 0.1
done

CHURN_END=$(( $(date +%s) + SOAK_DURATION_MIN * 60 ))
while [ "$(date +%s)" -lt "$CHURN_END" ]; do
  sleep 30
  P=$(wc -l < "$PROVISIONED" | tr -d ' ')
  D=$(wc -l < "$DESTROYED" | tr -d ' ')
  FP=$(wc -l < "$FAILED_PROV" | tr -d ' ')
  FD=$(wc -l < "$FAILED_DEST" | tr -d ' ')
  O=$(metric orchestrator_reaper_orphans_found_total)
  NS=$(k8s_ns_count)
  API=$(k8s_api_latency_ms)
  printf '   t-%3dm  prov=%-6s dest=%-6s failP=%-4s failD=%-4s orphans=%-4s ns=%-5s api=%sms\n' \
    "$(( (CHURN_END - $(date +%s)) / 60 ))" "$P" "$D" "$FP" "$FD" "$((O - BASE_ORPHANS))" "$NS" "$API"
  if [ "$((O - BASE_ORPHANS))" -ne 0 ]; then
    info "!! orphans_found_total moved during churn — the teardown path is leaking"
  fi
done

log "stopping workers + draining outstanding Destroys"
touch "$STOP_FLAG"
for pid in "${PIDS[@]}"; do wait "$pid" 2>/dev/null || true; done

# Backstop drain: any env id in PROVISIONED not in DESTROYED — try once more.
log "reconciling provisioned vs destroyed"
sort -u "$PROVISIONED" > "$WORKDIR/prov.sorted"
sort -u "$DESTROYED"   > "$WORKDIR/dest.sorted"
comm -23 "$WORKDIR/prov.sorted" "$WORKDIR/dest.sorted" > "$WORKDIR/leaked.candidates" || true
LEAK_CAND=$(wc -l < "$WORKDIR/leaked.candidates" | tr -d ' ')
if [ "$LEAK_CAND" -gt 0 ]; then
  info "${LEAK_CAND} env(s) provisioned but not confirmed destroyed — issuing final Destroy each"
  while read -r env_id; do
    [ -z "$env_id" ] && continue
    grpc -d "{\"environment_id\":\"${env_id}\",\"reason\":\"admin\",\"attempt_id\":\"soak-drain\"}" \
      "$SOAK_ORCH_ADDR" practiceengine.orchestrator.v1.EnvironmentOrchestrator/Destroy >/dev/null 2>&1 \
      && echo "$env_id" >> "$DESTROYED" || echo "$env_id" >> "$FAILED_DEST"
  done < "$WORKDIR/leaked.candidates"
fi

# ---- hold phase -----------------------------------------------------------
log "hold phase: ${SOAK_HOLD_MIN}m watching orphans_found_total"
HOLD_END=$(( $(date +%s) + SOAK_HOLD_MIN * 60 ))
HOLD_MAX_DELTA=0
while [ "$(date +%s)" -lt "$HOLD_END" ]; do
  sleep 60
  O=$(metric orchestrator_reaper_orphans_found_total)
  DELTA=$((O - BASE_ORPHANS))
  [ "$DELTA" -gt "$HOLD_MAX_DELTA" ] && HOLD_MAX_DELTA=$DELTA
  printf '   +%3dm  orphans_delta=%-4s ns=%-5s api=%sms\n' \
    "$(( SOAK_HOLD_MIN - (HOLD_END - $(date +%s)) / 60 ))" "$DELTA" "$(k8s_ns_count)" "$(k8s_api_latency_ms)"
done

# ---- results ------------------------------------------------------------
log "results"
FINAL_ORPHANS="$(metric orchestrator_reaper_orphans_found_total)"
FINAL_REAPER_DEST="$(metric orchestrator_reaper_destroyed_total)"
FINAL_NS="$(k8s_ns_count)"
TOTAL_PROV=$(sort -u "$PROVISIONED" | grep -c . || true)
TOTAL_DEST=$(sort -u "$DESTROYED" | grep -c . || true)
TOTAL_FP=$(grep -c . "$FAILED_PROV" || true)
TOTAL_FD=$(sort -u "$FAILED_DEST" | grep -c . || true)
ORPHAN_DELTA=$(( FINAL_ORPHANS - BASE_ORPHANS ))
REAPER_DELTA=$(( FINAL_REAPER_DEST - BASE_REAPER_DEST ))

info "provisioned (unique)        = ${TOTAL_PROV}"
info "destroyed  (confirmed)      = ${TOTAL_DEST}"
info "failed Provision            = ${TOTAL_FP}"
info "failed Destroy              = ${TOTAL_FD}"
info "orphans_found_total delta   = ${ORPHAN_DELTA}   (max during hold: ${HOLD_MAX_DELTA})"
info "reaper_destroyed_total delta= ${REAPER_DELTA}   (TTL/idle backstop teardowns during the run)"
info "env-* namespaces  base->now = ${BASE_NS} -> ${FINAL_NS}"

log "gate"
# Criterion 1: zero orphan delta, during + hold.
if [ "$ORPHAN_DELTA" -eq 0 ] && [ "$HOLD_MAX_DELTA" -eq 0 ]; then
  pass "zero orphans during churn and the ${SOAK_HOLD_MIN}m hold (increase(reaper_orphans_found_total) == 0)"
else
  fail "orphans_found_total increased by ${ORPHAN_DELTA} (hold max ${HOLD_MAX_DELTA}) — namespace/environment leak"
fi

# Criterion 2: everything provisioned got destroyed.
if [ "$TOTAL_DEST" -ge "$TOTAL_PROV" ] && [ "$TOTAL_FD" -eq 0 ]; then
  pass "every provisioned environment was explicitly destroyed (${TOTAL_DEST} >= ${TOTAL_PROV}, 0 failed Destroy)"
else
  fail "teardown gap: provisioned ${TOTAL_PROV}, confirmed-destroyed ${TOTAL_DEST}, failed Destroy ${TOTAL_FD}"
fi

# Criterion 3: the reaper's TTL backstop should not be the thing doing the
# teardown work. Some reaper activity is fine (races, the final drain), but if
# it tore down a large fraction of total provisions the explicit path is
# failing silently.
if [ "$TOTAL_PROV" -gt 0 ]; then
  PCT=$(( REAPER_DELTA * 100 / TOTAL_PROV ))
  if [ "$PCT" -le 5 ]; then
    pass "reaper TTL backstop handled only ${PCT}% of teardowns (${REAPER_DELTA}/${TOTAL_PROV}) — explicit Destroy path is carrying the load"
  else
    fail "reaper TTL backstop handled ${PCT}% of teardowns (${REAPER_DELTA}/${TOTAL_PROV}) — explicit Destroy path is not keeping up"
  fi
fi

# Criterion 4: namespace count returned to ~baseline.
if [ "$FINAL_NS" != "n/a" ] && [ "$BASE_NS" != "n/a" ]; then
  if [ "$FINAL_NS" -le "$(( BASE_NS + 2 ))" ]; then
    pass "env-* namespace count returned to baseline (${BASE_NS} -> ${FINAL_NS})"
  else
    fail "env-* namespace count did not drain: ${BASE_NS} -> ${FINAL_NS}"
    KUBECONFIG="$SOAK_KUBECONFIG" kubectl get ns 2>/dev/null | grep "^${SOAK_NS_PREFIX}" || true
  fi
else
  info "namespace cross-check skipped (no kubectl/kubeconfig) — counter gate above still holds"
fi

if [ -s "$WORKDIR/grpc.err" ]; then
  log "grpc error sample (first 20 lines)"
  head -20 "$WORKDIR/grpc.err"
fi

echo
echo "Record this run in evaluation/phase1/results/soak-namespace-churn-$(date +%Y%m%d).md"
echo "(template: evaluation/phase1/soak/results-template.md). Run"
echo "evaluation/phase1/load/check-orphans.sh again ≥1h from now for an"
echo "independent second orphan reading."
echo

if [ "$FAILURES" -eq 0 ]; then
  pass "namespace-churn soak (${SOAK_TIER}, 3× peak) — ALL CRITERIA MET"
  exit 0
else
  fail "namespace-churn soak — ${FAILURES} criterion(s) failed"
  exit 1
fi
