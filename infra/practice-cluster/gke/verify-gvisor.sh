#!/usr/bin/env bash
# verify-gvisor.sh — prove the regional practice cluster is real and that
# the practice-t1 pool actually sandboxes workloads with gVisor (runsc).
#
# This is the hard gate for Phase 1 Step 1. Exit 0 is NOT enough — every
# [CHECK] below must pass. Run AFTER `tofu apply` + `get-credentials`.
#
#   ./verify-gvisor.sh | tee ../../../evaluation/phase1/results/logs/gvisor-verify-$(date +%Y%m%dT%H%M%SZ).log
#
# Env:
#   KUBECONFIG must point at the new cluster (get_credentials_command output).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POD_MANIFEST="$SCRIPT_DIR/testdata/gvisor-smoke-pod.yaml"
POD=gvisor-smoke

c_g=$'\033[1;32m'; c_r=$'\033[1;31m'; c_b=$'\033[1;34m'; c_0=$'\033[0m'
step() { printf '\n%s== %s%s\n' "$c_b" "$*" "$c_0"; }
ok()   { printf '%s[CHECK PASS]%s %s\n' "$c_g" "$c_0" "$*"; }
die()  { printf '%s[CHECK FAIL]%s %s\n' "$c_r" "$c_0" "$*" >&2; cleanup; exit 1; }

cleanup() {
  kubectl delete -f "$POD_MANIFEST" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "verify-gvisor.sh  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "context: $(kubectl config current-context 2>/dev/null || echo '(none)')"

# ---------------------------------------------------------------------------
step "1. cluster reachable + kubectl connectivity"
kubectl version -o yaml | sed -n '1,40p'
kubectl cluster-info
SRV_VER="$(kubectl version -o json | python3 -c 'import sys,json;print(json.load(sys.stdin)["serverVersion"]["gitVersion"])')"
ok "kubectl reached the API server (server $SRV_VER)"

# ---------------------------------------------------------------------------
step "2. regional cluster + multiple Ready worker nodes"
kubectl get nodes -o wide
NODE_ZONES="$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.labels.topology\.kubernetes\.io/zone}{"\n"}{end}' | sort -u)"
ZONE_COUNT="$(echo "$NODE_ZONES" | grep -c . || true)"
NODE_COUNT="$(kubectl get nodes --no-headers | wc -l | tr -d ' ')"
READY_COUNT="$(kubectl get nodes --no-headers | awk '$2 ~ /(^|,)Ready($|,)/ {c++} END{print c+0}')"
echo "nodes=$NODE_COUNT ready=$READY_COUNT zones=$ZONE_COUNT"
echo "$NODE_ZONES"
[ "$NODE_COUNT" -ge 2 ]  || die "expected >=2 worker nodes, got $NODE_COUNT"
[ "$READY_COUNT" -eq "$NODE_COUNT" ] || die "not all nodes Ready ($READY_COUNT/$NODE_COUNT)"
[ "$ZONE_COUNT" -ge 2 ]  || die "expected nodes across >=2 zones (regional), got $ZONE_COUNT"
ok "$NODE_COUNT worker nodes, all Ready, across $ZONE_COUNT zones (regional)"

# ---------------------------------------------------------------------------
step "3. namespaces"
kubectl get namespaces
ok "namespace listing works"

# ---------------------------------------------------------------------------
step "4. gVisor RuntimeClass present"
kubectl get runtimeclass
kubectl get runtimeclass gvisor -o yaml
HANDLER="$(kubectl get runtimeclass gvisor -o jsonpath='{.handler}')"
[ "$HANDLER" = "runsc" ] || die "RuntimeClass 'gvisor' handler is '$HANDLER', expected 'runsc'"
ok "RuntimeClass 'gvisor' exists with handler 'runsc' (matches runtimeClassForT1(true))"

# ---------------------------------------------------------------------------
step "5. at least one node advertises the practice-t1 sandbox pool"
T1_NODES="$(kubectl get nodes -l practiceengine.dev/tier=t1 --no-headers | wc -l | tr -d ' ')"
[ "$T1_NODES" -ge 1 ] || die "no nodes labelled practiceengine.dev/tier=t1"
kubectl get nodes -l practiceengine.dev/tier=t1 -o custom-columns=NAME:.metadata.name,SANDBOX:.metadata.labels.sandbox\\.gke\\.io/runtime,TAINTS:.spec.taints
ok "$T1_NODES node(s) in the practice-t1 (gVisor) pool"

# ---------------------------------------------------------------------------
step "6. schedule the minimal gVisor workload"
kubectl delete -f "$POD_MANIFEST" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl apply -f "$POD_MANIFEST"
kubectl wait --for=condition=Ready "pod/$POD" --timeout=150s || {
  kubectl describe "pod/$POD" | tail -40
  die "$POD never became Ready"
}
PHASE="$(kubectl get pod "$POD" -o jsonpath='{.status.phase}')"
[ "$PHASE" = "Running" ] || die "$POD phase is $PHASE, expected Running"
ok "$POD scheduled and Running"

# ---------------------------------------------------------------------------
step "7. the pod's RuntimeClass really is gvisor, and it landed on a t1 node"
RC="$(kubectl get pod "$POD" -o jsonpath='{.spec.runtimeClassName}')"
[ "$RC" = "gvisor" ] || die "pod .spec.runtimeClassName is '$RC', expected 'gvisor'"
POD_NODE="$(kubectl get pod "$POD" -o jsonpath='{.spec.nodeName}')"
kubectl get node "$POD_NODE" -o jsonpath='{.metadata.labels.practiceengine\.dev/tier}{"\n"}' | grep -qx t1 \
  || die "pod landed on $POD_NODE which is not a tier=t1 node"
ok "pod uses runtimeClassName=gvisor and runs on t1 node $POD_NODE"

# ---------------------------------------------------------------------------
step "8. PROOF it is actually sandboxed (not just scheduled): in-guest kernel is gVisor"
# Under runsc the guest kernel identifies as 'gVisor'. Under runc it would
# be the host's real Linux release. This is the check that a mis-wired
# RuntimeClass (handler present, sandbox not really active) cannot fake.
UNAME_ALL="$(kubectl exec "$POD" -- uname -a || true)"
UNAME_VER="$(kubectl exec "$POD" -- uname -v || true)"
echo "uname -a : $UNAME_ALL"
echo "uname -v : $UNAME_VER"
if echo "$UNAME_ALL $UNAME_VER" | grep -qi gvisor; then
  ok "in-pod 'uname' reports gVisor — workload is genuinely gVisor-sandboxed"
else
  # Fallback proof: dmesg under gVisor prints 'Starting gVisor...'
  DMESG="$(kubectl exec "$POD" -- dmesg 2>/dev/null | head -5 || true)"
  echo "dmesg head: $DMESG"
  echo "$DMESG" | grep -qi gvisor \
    && ok "in-pod 'dmesg' reports gVisor — workload is genuinely gVisor-sandboxed" \
    || die "no gVisor signature in 'uname' or 'dmesg' — pod may NOT be sandboxed"
fi

# ---------------------------------------------------------------------------
step "9. kubectl exec works"
OUT="$(kubectl exec "$POD" -- sh -c 'echo exec-ok-$(id -u)')"
echo "$OUT"
[ "$OUT" = "exec-ok-65532" ] || die "unexpected exec output: $OUT"
ok "kubectl exec works (ran as uid 65532)"

# ---------------------------------------------------------------------------
step "10. delete the workload cleanly"
kubectl delete -f "$POD_MANIFEST" --wait=true --timeout=60s
kubectl get pod "$POD" 2>&1 | grep -q 'NotFound' \
  && ok "workload deleted, no residue" \
  || die "$POD still present after delete"

trap - EXIT
step "ALL CHECKS PASSED"
echo "Regional gVisor-capable practice cluster verified end-to-end."
