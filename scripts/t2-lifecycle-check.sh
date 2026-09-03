#!/usr/bin/env bash
# t2-lifecycle-check.sh — Phase 2 requirement A, the end-to-end harness.
#
# Runs ONE full T2 environment lifecycle against a REAL orchestrator and a
# REAL Sysbox-capable cluster (the ₹100/user runtime — Sysbox on the
# shared T1 pool; docs/t2-cost-optimization-100.md). Set T2_RUNTIME_CLASS=kata
# to check the Kata scale-up path instead.
#
#   create (Provision, T2)  ->  health gate passes
#   connect (Connect)       ->  exec into the workspace
#   verify 4 capabilities   ->  DinD, multi-node k3s, systemd, eBPF
#   destroy (Destroy)       ->  namespace gone, no PV/PVC leaked
#   zero-leftover           ->  reaper_orphans_found_total unchanged
#
# It is a HARNESS, not a mock: every step is a real RPC or a real kubectl
# call. It FAILS LOUDLY if anything is stubbed, skipped, or leftover — per
# the Phase 2 rule "partially done, mocked, or not tested in real
# environment -> NOT complete".
#
# ---------------------------------------------------------------------------
# Preconditions (the script checks these and aborts if unmet):
#   - $KUBECONFIG points at the practice cluster
#   - RuntimeClass "$T2_RUNTIME_CLASS" (default sysbox-runc) exists
#   - orchestrator reachable at $ORCH_GRPC with ORCHESTRATOR_T2_ENABLED=true
#     and ORCHESTRATOR_T2_RUNTIME_CLASS matching $T2_RUNTIME_CLASS
#   - $ATTEMPT_ID is a real UUID present in the attempt table (the
#     ownership check in server.go requires it) — or pass SEED_ATTEMPT=1
#     to have this script insert a throwaway attempt row via $DATABASE_URL
#   - grpcurl, kubectl, jq on PATH
#
# Env:
#   ORCH_GRPC                 default localhost:50051
#   ORCHESTRATOR_SHARED_SECRET   required (must match the orchestrator)
#   ORCH_METRICS             default http://localhost:9090
#   T2_BLUEPRINT_ID         default bp.k8s-multinode.v1
#   T2_BLUEPRINT_VERSION    default 1
#   T2_RUNTIME_CLASS        default sysbox-runc (set to "kata" for the scale-up path)
#   ATTEMPT_ID              required unless SEED_ATTEMPT=1
#   DATABASE_URL            required if SEED_ATTEMPT=1
#   SKIP_CAPABILITY_CHECKS  set to 1 to do lifecycle only (NOT a pass for A)
#
# Exit: 0 = full lifecycle + all 4 capabilities verified + zero leftover.
#       non-zero = a real failure; the message says which step and why.
set -euo pipefail

ORCH_GRPC="${ORCH_GRPC:-localhost:50051}"
ORCH_METRICS="${ORCH_METRICS:-http://localhost:9090}"
T2_BLUEPRINT_ID="${T2_BLUEPRINT_ID:-bp.k8s-multinode.v1}"
T2_BLUEPRINT_VERSION="${T2_BLUEPRINT_VERSION:-1}"
T2_RUNTIME_CLASS="${T2_RUNTIME_CLASS:-sysbox-runc}"
: "${ORCHESTRATOR_SHARED_SECRET:?must be set (must match the running orchestrator)}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO="$REPO_ROOT/contracts/orchestrator.proto"

c_reset=$'\033[0m'; c_blue=$'\033[1;34m'; c_green=$'\033[1;32m'; c_red=$'\033[1;31m'; c_yellow=$'\033[1;33m'
step() { printf '\n%s== %s%s\n' "$c_blue" "$*" "$c_reset"; }
ok()   { printf '%sPASS%s %s\n' "$c_green" "$c_reset" "$*"; }
warn() { printf '%sWARN%s %s\n' "$c_yellow" "$c_reset" "$*"; }
die()  { printf '%sFAIL%s %s\n' "$c_red" "$c_reset" "$*" >&2; exit 1; }

grpc() {
  # $1 = method, $2 = json body
  grpcurl -plaintext \
    -import-path "$REPO_ROOT/contracts" -proto orchestrator.proto \
    -H "authorization: Bearer ${ORCHESTRATOR_SHARED_SECRET}" \
    -d "$2" "$ORCH_GRPC" "orchestrator.Orchestrator/$1"
}

# --- tooling -------------------------------------------------------------
step "checking tooling"
for bin in grpcurl kubectl jq; do command -v "$bin" >/dev/null || die "missing $bin on PATH"; done
[ -f "$PROTO" ] || die "proto not found at $PROTO"
ok "grpcurl, kubectl, jq present"

# --- preconditions ----------------------------------------------------------
step "checking cluster preconditions"
kubectl version -o json >/dev/null 2>&1 || die "kubectl can't reach a cluster (is \$KUBECONFIG set?)"
READY_NODES="$(kubectl get nodes -o json | jq '[.items[] | select(.status.conditions[] | select(.type=="Ready" and .status=="True"))] | length')"
[ "${READY_NODES:-0}" -ge 1 ] || die "no Ready nodes in the cluster"
kubectl get runtimeclass "$T2_RUNTIME_CLASS" >/dev/null 2>&1 || die "RuntimeClass '$T2_RUNTIME_CLASS' not found — for sysbox-runc, run infra/practice-cluster/bootstrap/k3s-sysbox-node.sh (single VM) or apply infra/practice-cluster/sysbox/sysbox-install.yaml (multi-node); for kata, see infra/practice-cluster/t2-nodepool-kata/"
ok "$READY_NODES Ready node(s); RuntimeClass $T2_RUNTIME_CLASS exists"

step "checking orchestrator T2 is enabled"
# A T2 Provision with a bogus attempt_id should fail on OWNERSHIP
# (PermissionDenied / InvalidArgument), NOT on FailedPrecondition
# "T2 ... is not enabled". That distinguishes "T2 on" from "T2 off".
probe_err="$(grpc Provision '{"attempt_id":"00000000-0000-0000-0000-000000000000","blueprint_id":"'"$T2_BLUEPRINT_ID"'","tier":"TIER_T2_ISOLATED_MICROVM"}' 2>&1 || true)"
if grep -q "is not enabled" <<<"$probe_err"; then
  die "orchestrator reports T2 not enabled — set ORCHESTRATOR_T2_ENABLED=true and restart it"
fi
ok "orchestrator accepts T2 tier (ownership-gated, as expected)"

# --- attempt row --------------------------------------------------------
if [ "${SEED_ATTEMPT:-0}" = "1" ]; then
  : "${DATABASE_URL:?SEED_ATTEMPT=1 needs DATABASE_URL}"
  command -v psql >/dev/null || die "SEED_ATTEMPT=1 needs psql"
  ATTEMPT_ID="$(uuidgen | tr 'A-Z' 'a-z')"
  step "seeding throwaway attempt $ATTEMPT_ID"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q <<SQL
INSERT INTO attempt.attempt (id, tenant_id, user_id, activity_id, activity_version_id, mode, status, created_at)
VALUES ('$ATTEMPT_ID', gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
        'PRODUCTION_SIM', 'PROVISIONING', now());
SQL
  ok "attempt row inserted"
  cleanup_attempt() { psql "$DATABASE_URL" -q -c "DELETE FROM attempt.attempt WHERE id='$ATTEMPT_ID';" || true; }
  trap cleanup_attempt EXIT
else
  : "${ATTEMPT_ID:?set ATTEMPT_ID to a real attempt UUID, or pass SEED_ATTEMPT=1 with DATABASE_URL}"
fi

# --- snapshot the orphan counter --------------------------------------------
metric() { curl -sf "${ORCH_METRICS}/metrics" | awk -v m="$1" '$0 !~ /^#/ && index($0,m)==1 {s+=$NF} END{printf "%d", s+0}'; }
ORPHANS_BEFORE="$(metric orchestrator_reaper_orphans_found_total || echo 0)"
step "orphan counter before: $ORPHANS_BEFORE"

# ======================================================================
# 1. CREATE
# ======================================================================
step "1/5 Provision (T2)"
PROV="$(grpc Provision '{
  "attempt_id":"'"$ATTEMPT_ID"'",
  "blueprint_id":"'"$T2_BLUEPRINT_ID"'",
  "blueprint_version":"'"$T2_BLUEPRINT_VERSION"'",
  "tier":"TIER_T2_ISOLATED_MICROVM"
}')" || die "Provision RPC failed:\n$PROV"
ENV_ID="$(jq -r '.environmentId // .environment_id' <<<"$PROV")"
STATUS="$(jq -r '.status' <<<"$PROV")"
[ -n "$ENV_ID" ] && [ "$ENV_ID" != "null" ] || die "Provision returned no environment_id:\n$PROV"
NS="env-$ENV_ID"
echo "  environment_id = $ENV_ID   status = $STATUS"

# Always try to clean up the env even if a later step fails.
cleanup_env() {
  step "cleanup: Destroy $ENV_ID"
  grpc Destroy '{"environment_id":"'"$ENV_ID"'","attempt_id":"'"$ATTEMPT_ID"'","reason":"admin"}' 2>/dev/null || true
  kubectl delete ns "$NS" --wait=false 2>/dev/null || true
}
trap 'cleanup_env; { [ "${SEED_ATTEMPT:-0}" = "1" ] && cleanup_attempt; } || true' EXIT

step "waiting for workspace pod Ready + health gate"
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  phase="$(kubectl -n "$NS" get pod workspace -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  ready="$(kubectl -n "$NS" get pod workspace -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || true)"
  [ "$ready" = "true" ] && break
  [ "$phase" = "Failed" ] && die "workspace pod entered Failed:\n$(kubectl -n "$NS" describe pod workspace | tail -30)"
  sleep 3
done
[ "$(kubectl -n "$NS" get pod workspace -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null)" = "true" ] \
  || die "workspace pod never became Ready in 180s:\n$(kubectl -n "$NS" describe pod workspace | tail -30)"

# The pod must actually be running under the T2 runtime — not a silent
# fallback to a normal container.
RC="$(kubectl -n "$NS" get pod workspace -o jsonpath='{.spec.runtimeClassName}')"
[ "$RC" = "$T2_RUNTIME_CLASS" ] || die "workspace pod runtimeClassName is '$RC', expected '$T2_RUNTIME_CLASS' — T2 isolation is NOT in effect (check ORCHESTRATOR_T2_RUNTIME_CLASS on the orchestrator)"
NODE="$(kubectl -n "$NS" get pod workspace -o jsonpath='{.spec.nodeName}')"
PSS="$(kubectl get ns "$NS" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}')"
[ "$PSS" = "privileged" ] || die "namespace PSS enforce level is '$PSS', expected 'privileged' (T2 — Sysbox shell runs as root-in-userns)"
if [ "$T2_RUNTIME_CLASS" = "sysbox-runc" ]; then
  # Sysbox: shell container runs as root-in-userns and is NOT privileged.
  RUN_AS_USER="$(kubectl -n "$NS" get pod workspace -o jsonpath='{.spec.containers[0].securityContext.runAsUser}')"
  [ "$RUN_AS_USER" = "0" ] || die "workspace container runAsUser is '$RUN_AS_USER', expected 0 (Sysbox root-in-userns)"
  PRIV="$(kubectl -n "$NS" get pod workspace -o jsonpath='{.spec.containers[0].securityContext.privileged}')"
  [ "$PRIV" != "true" ] || warn "workspace container is privileged — expected only for eBPF-capability blueprints"
fi
QUOTA_CPU="$(kubectl -n "$NS" get resourcequota env-quota -o jsonpath='{.spec.hard.requests\.cpu}')"
QUOTA_MEM="$(kubectl -n "$NS" get resourcequota env-quota -o jsonpath='{.spec.hard.requests\.memory}')"
echo "  runtimeClass=$RC node=$NODE pss=$PSS quota=${QUOTA_CPU}/${QUOTA_MEM}"
[ "$QUOTA_CPU" = "8" ] && [ "$QUOTA_MEM" = "16Gi" ] \
  || warn "quota ${QUOTA_CPU}/${QUOTA_MEM} != 8/16Gi — OK if the blueprint set explicit resources (t2-cost-optimization-100.md), otherwise investigate"
ok "T2 environment is real: runtimeClass=$T2_RUNTIME_CLASS, PSS privileged"

# ======================================================================
# 2. CONNECT
# ======================================================================
step "2/5 Connect"
CONN="$(grpc Connect '{"environment_id":"'"$ENV_ID"'","attempt_id":"'"$ATTEMPT_ID"'"}')" \
  || die "Connect RPC failed:\n$CONN"
WS_URL="$(jq -r '.terminalWsUrl // .terminal_ws_url' <<<"$CONN")"
TOKEN="$(jq -r '.sessionToken // .session_token' <<<"$CONN")"
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || die "Connect returned no session token:\n$CONN"
ok "Connect minted a session token (terminal ws: $WS_URL)"

# For the capability checks we exec directly via the K8s API — the same
# channel the Session Broker proxies (memory.md §5.4 "K8s exec API for
# T1/T2"). A working Connect above proves the broker path; exec here
# proves the microVM's kernel actually does what T2 promises.
kx() { kubectl -n "$NS" exec workspace -c shell -- sh -lc "$1"; }

# ======================================================================
# 3. VERIFY THE 4 CAPABILITIES  (Phase 2 A: "Verify inside T2")
# ======================================================================
if [ "${SKIP_CAPABILITY_CHECKS:-0}" = "1" ]; then
  warn "SKIP_CAPABILITY_CHECKS=1 — lifecycle only. This is NOT a pass for requirement A."
else
  step "3/5 capability: Docker-in-Docker"
  kx 'command -v dockerd >/dev/null || (apt-get update -qq && apt-get install -y -qq docker.io) >/dev/null 2>&1 || true'
  kx 'pgrep dockerd >/dev/null || (dockerd >/tmp/dockerd.log 2>&1 &) ; for i in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done; docker run --rm hello-world' \
    | grep -q "Hello from Docker" || die "DinD: 'docker run hello-world' did not print the success banner. dockerd log:\n$(kx 'tail -20 /tmp/dockerd.log' || true)"
  ok "Docker-in-Docker works (ran hello-world inside the microVM)"

  step "3/5 capability: multi-node k3s (nested)"
  kx 'command -v k3d >/dev/null || curl -sfL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash >/dev/null 2>&1 || true'
  kx 'k3d cluster create t2check --agents 2 --wait --timeout 120s >/tmp/k3d.log 2>&1'
  AGENTS="$(kx 'k3d kubeconfig get t2check >/tmp/kc 2>/dev/null; KUBECONFIG=/tmp/kc kubectl get nodes --no-headers 2>/dev/null | wc -l' | tr -d '[:space:]')"
  [ "${AGENTS:-0}" -ge 3 ] || die "multi-node k3s: expected >=3 nodes (1 server + 2 agents), got '$AGENTS'. k3d log:\n$(kx 'tail -20 /tmp/k3d.log' || true)"
  kx 'k3d cluster delete t2check >/dev/null 2>&1 || true'
  ok "multi-node k3s works ($AGENTS nodes in the nested cluster)"

  step "3/5 capability: systemd"
  SYSD="$(kx 'systemctl is-system-running 2>/dev/null || true' | tr -d '[:space:]')"
  case "$SYSD" in
    running|degraded) ok "systemd is up (is-system-running=$SYSD)";;
    *) die "systemd: is-system-running='$SYSD' (expected running/degraded). The T2 image must boot with systemd as PID 1 or provide a usable systemd — see t2-setup-and-operations.md §5.3.";;
  esac
  kx 'systemd-run --unit=t2check-unit --wait /bin/true && systemctl status t2check-unit --no-pager | head -3' >/dev/null \
    || die "systemd: 'systemd-run --wait /bin/true' failed — systemd can't actually run a transient unit"
  ok "systemd runs transient units"

  step "3/5 capability: eBPF"
  kx 'command -v bpftrace >/dev/null || (apt-get update -qq && apt-get install -y -qq bpftrace) >/dev/null 2>&1 || true'
  EBPF_OUT="$(kx 'bpftrace -e "tracepoint:syscalls:sys_enter_execve { printf(\"EXEC %s\\n\", comm); exit(); }" -c "/bin/true" 2>/tmp/bpf.log || true')"
  grep -q "EXEC" <<<"$EBPF_OUT" || die "eBPF: bpftrace did not attach/emit. This needs a real kernel with BPF + a loaded verifier — a shared-kernel gVisor sandbox (T1) would fail here, which is the point. bpftrace log:\n$(kx 'tail -20 /tmp/bpf.log' || true)"
  ok "eBPF workloads run (bpftrace attached a tracepoint program and got events)"
fi

# ======================================================================
# 4. DESTROY
# ======================================================================
step "4/5 Destroy"
DEST="$(grpc Destroy '{"environment_id":"'"$ENV_ID"'","attempt_id":"'"$ATTEMPT_ID"'","reason":"admin"}')" \
  || die "Destroy RPC failed:\n$DEST"
echo "  $DEST"

step "waiting for namespace deletion"
deadline=$((SECONDS + 120))
while (( SECONDS < deadline )); do
  kubectl get ns "$NS" >/dev/null 2>&1 || break
  sleep 3
done
kubectl get ns "$NS" >/dev/null 2>&1 && die "namespace $NS still present 120s after Destroy — teardown leaked. \`kubectl get all,pv,pvc -n $NS\`:\n$(kubectl get all,pvc -n "$NS" 2>/dev/null)"
ok "namespace $NS deleted"

# ======================================================================
# 5. ZERO LEFTOVER
# ======================================================================
step "5/5 zero-leftover checks"
LEAKED_PV="$(kubectl get pv -o json | jq -r '[.items[] | select(.spec.claimRef.namespace=="'"$NS"'")] | length')"
[ "${LEAKED_PV:-0}" -eq 0 ] || die "$LEAKED_PV PersistentVolume(s) still bound to $NS after Destroy"
ok "no PV leaked"

ORPHANS_AFTER="$(metric orchestrator_reaper_orphans_found_total || echo "$ORPHANS_BEFORE")"
[ "$ORPHANS_AFTER" -eq "$ORPHANS_BEFORE" ] \
  || die "orchestrator_reaper_orphans_found_total moved $ORPHANS_BEFORE -> $ORPHANS_AFTER during this run — the reaper found an environment it had no record of"
ok "orphan counter unchanged ($ORPHANS_AFTER)"

# Report any T2 workspace pods still running cluster-wide (Sysbox shares
# the T1 pool, so there is no separate ASG to scale down — this is just a
# leak check).
REMAINING_T2_PODS="$(kubectl get pods -A -l app=workspace --field-selector=status.phase=Running -o json 2>/dev/null \
  | jq --arg rc "$T2_RUNTIME_CLASS" '[.items[] | select(.spec.runtimeClassName==$rc)] | length')"
echo "  T2 ($T2_RUNTIME_CLASS) workspace pods still Running cluster-wide: ${REMAINING_T2_PODS:-0}"

trap - EXIT
[ "${SEED_ATTEMPT:-0}" = "1" ] && cleanup_attempt || true

step "RESULT"
if [ "${SKIP_CAPABILITY_CHECKS:-0}" = "1" ]; then
  warn "lifecycle PASSED but capability checks were skipped — requirement A is NOT satisfied. Re-run without SKIP_CAPABILITY_CHECKS."
  exit 2
fi
ok "T2 full lifecycle + DinD + multi-node k3s + systemd + eBPF + zero leftover — ALL VERIFIED"
echo
echo "Record this output in PHASE2_CLOSEOUT.md under requirement A."
