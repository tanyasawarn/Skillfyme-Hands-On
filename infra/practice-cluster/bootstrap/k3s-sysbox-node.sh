#!/usr/bin/env bash
# k3s-sysbox-node.sh — provision a single ARM VM as the practice cluster:
# k3s + Sysbox, so T1 pods AND T2 (Sysbox) pods run on the one node.
#
# This is the ₹100/user host target (docs/t2-cost-optimization-100.md):
# one Ubuntu 24.04 ARM box (e.g. Hetzner CAX41, 16 vCPU / 32 GiB), no
# EKS, no separate T2 pool.
#
# Run as root on a FRESH Ubuntu 24.04 ARM64 server:
#   scp this script over, then:  bash k3s-sysbox-node.sh
# or:  curl -sfL <raw-url> | bash
#
# Idempotent: re-running skips steps already done.
#
# What it does:
#   1. sanity: arch, kernel >= 5.12, unprivileged userns enabled
#   2. install k3s (single node, no traefik, no local-storage churn)
#   3. install Sysbox from the official .deb, register sysbox-runc with
#      k3s's bundled containerd
#   4. apply the sysbox-runc RuntimeClass
#   5. verify: a Sysbox pod runs dockerd + hello-world
#
# After this, deploy the platform (orchestrator/practice-core/web/db) and
# run scripts/t2-lifecycle-check.sh.
set -euo pipefail

SYSBOX_VERSION="${SYSBOX_VERSION:-0.6.7}"
K3S_CHANNEL="${K3S_CHANNEL:-stable}"
ARCH="$(uname -m)"

c_b=$'\033[1;34m'; c_g=$'\033[1;32m'; c_r=$'\033[1;31m'; c_0=$'\033[0m'
step() { printf '\n%s== %s%s\n' "$c_b" "$*" "$c_0"; }
ok()   { printf '%sOK%s %s\n' "$c_g" "$c_0" "$*"; }
die()  { printf '%sFAIL%s %s\n' "$c_r" "$c_0" "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root"

# ---------------------------------------------------------------------------
step "1/5 sanity checks"
case "$ARCH" in
  aarch64|arm64) ok "arch $ARCH" ;;
  x86_64)        ok "arch $ARCH (x86_64 also fine for Sysbox)" ;;
  *) die "unsupported arch $ARCH" ;;
esac

KVER="$(uname -r | cut -d- -f1)"
KMAJ="${KVER%%.*}"; KMIN="$(echo "$KVER" | cut -d. -f2)"
if [ "$KMAJ" -lt 5 ] || { [ "$KMAJ" -eq 5 ] && [ "$KMIN" -lt 12 ]; }; then
  die "kernel $KVER < 5.12 — Sysbox needs >= 5.12 (Ubuntu 24.04 ships 6.8). Upgrade the base image."
fi
ok "kernel $KVER"

USERNS="$(sysctl -n kernel.unprivileged_userns_clone 2>/dev/null || echo 1)"
if [ "$USERNS" != "1" ]; then
  echo "kernel.unprivileged_userns_clone=1" > /etc/sysctl.d/99-sysbox-userns.conf
  sysctl -p /etc/sysctl.d/99-sysbox-userns.conf
fi
ok "unprivileged user namespaces enabled"

# Sysbox needs these for its shiftfs/idmapped-mount and inotify use.
cat > /etc/sysctl.d/99-sysbox.conf <<'EOF'
fs.inotify.max_queued_events = 1048576
fs.inotify.max_user_watches = 1048576
fs.inotify.max_user_instances = 1048576
kernel.keys.maxkeys = 20000
kernel.keys.maxbytes = 400000
EOF
sysctl -p /etc/sysctl.d/99-sysbox.conf >/dev/null
ok "sysctl tuning applied"

apt-get update -qq
apt-get install -y -qq curl ca-certificates jq iptables >/dev/null
ok "base packages"

# ---------------------------------------------------------------------------
# Oracle Cloud (and some other providers) ship Ubuntu with a restrictive
# INPUT chain that drops everything except SSH -- k3s's API server, the
# CNI, NodePorts, and pod-to-pod traffic all break silently. Detect that
# shape and open what the platform needs. Harmless on a host without it.
if iptables -S INPUT 2>/dev/null | grep -q -- '-j REJECT --reject-with icmp-host-prohibited' \
   || iptables -S INPUT 2>/dev/null | grep -qi 'DROP'; then
  step "1b/5 relax the provider's default iptables (Oracle Cloud shape detected)"
  # Trust the pod/service CIDRs and the loopback + established traffic;
  # open the platform's external ports. Insert BEFORE the catch-all
  # REJECT/DROP rules (-I ... 1 ... prepends).
  iptables -I INPUT 1 -i lo -j ACCEPT
  iptables -I INPUT 2 -m state --state ESTABLISHED,RELATED -j ACCEPT
  # k3s defaults: pods 10.42.0.0/16, services 10.43.0.0/16.
  iptables -I INPUT 3 -s 10.42.0.0/16 -j ACCEPT
  iptables -I INPUT 4 -s 10.43.0.0/16 -j ACCEPT
  iptables -I INPUT 5 -p udp --dport 8472 -j ACCEPT   # flannel VXLAN
  iptables -I INPUT 6 -p tcp --dport 6443 -j ACCEPT   # k3s API
  iptables -I INPUT 7 -p tcp --dport 10250 -j ACCEPT  # kubelet
  iptables -I INPUT 8 -p tcp --dport 80 -j ACCEPT
  iptables -I INPUT 9 -p tcp --dport 443 -j ACCEPT
  iptables -I INPUT 10 -p tcp --dport 50051 -j ACCEPT # orchestrator gRPC
  iptables -I INPUT 11 -p tcp --dport 30000:32767 -j ACCEPT # NodePort range
  # Persist across reboot.
  apt-get install -y -qq iptables-persistent netfilter-persistent >/dev/null 2>&1 || true
  netfilter-persistent save >/dev/null 2>&1 || iptables-save > /etc/iptables/rules.v4 2>/dev/null || true
  ok "iptables INPUT chain opened for k3s + platform ports and persisted"
fi

# ---------------------------------------------------------------------------
step "2/5 install k3s (single node)"
if command -v k3s >/dev/null 2>&1; then
  ok "k3s already installed ($(k3s --version | head -1))"
else
  # --disable traefik: we terminate TLS at the platform's own ingress.
  # --disable local-storage: no per-namespace PV churn (memory.md §5.2
  #   "keep namespaces lean"); workspace volumes are emptyDir.
  # --write-kubeconfig-mode 0644: so a non-root deploy user can read it.
  curl -sfL https://get.k3s.io | \
    INSTALL_K3S_CHANNEL="$K3S_CHANNEL" \
    INSTALL_K3S_EXEC="--disable traefik --disable local-storage --write-kubeconfig-mode 0644" \
    sh -
  for i in $(seq 1 60); do
    k3s kubectl get --raw='/readyz' >/dev/null 2>&1 && break
    sleep 2
  done
  ok "k3s installed and ready"
fi

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
KUBECTL="k3s kubectl"

# Label + taint the node so the orchestrator's tier logic and the
# platform/learner split work exactly as on a real multi-node cluster.
NODE="$($KUBECTL get nodes -o jsonpath='{.items[0].metadata.name}')"
$KUBECTL label  node "$NODE" practiceengine.dev/tier=t1 --overwrite >/dev/null
# NOTE: on a single-node cluster we do NOT taint workload=learner:NoSchedule
# — that would also block the platform pods (orchestrator, db) which have
# no toleration. On a real >1-node cluster, taint the learner pool and
# keep platform pods on a separate untainted node.
ok "node $NODE labelled practiceengine.dev/tier=t1"

# ---------------------------------------------------------------------------
step "3/5 install Sysbox + register sysbox-runc with k3s containerd"
if command -v sysbox-runc >/dev/null 2>&1 && [ -f /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl ] \
   && grep -q 'sysbox-runc' /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl 2>/dev/null; then
  ok "sysbox-runc already installed and wired into k3s containerd"
else
  # Sysbox needs Docker absent OR configured not to conflict; on a fresh
  # k3s box there's no Docker, which is the supported path.
  apt-get install -y -qq \
    "rsync" "fuse" "iptables" >/dev/null

  DEB="sysbox-ce_${SYSBOX_VERSION}-0.linux_${ARCH/aarch64/arm64}.deb"
  DEB="${DEB/x86_64/amd64}"
  URL="https://downloads.nestybox.com/sysbox/releases/v${SYSBOX_VERSION}/${DEB}"
  echo "  downloading $URL"
  curl -fsSL -o "/tmp/${DEB}" "$URL" || die "could not download Sysbox .deb — check SYSBOX_VERSION ($SYSBOX_VERSION) against https://github.com/nestybox/sysbox/releases"
  # The .deb's postinst tries to restart Docker; harmless if absent.
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "/tmp/${DEB}" || \
    dpkg -i "/tmp/${DEB}" || die "Sysbox .deb install failed"
  systemctl enable --now sysbox >/dev/null 2>&1 || true
  # Gate: sysbox-mgr + sysbox-fs must actually be up before k3s tries to
  # use the runtime, or containerd loads the config before the socket
  # exists and silently drops the runtime (seen on k3s >= v1.34).
  for i in $(seq 1 30); do
    systemctl is-active --quiet sysbox && systemctl is-active --quiet sysbox-mgr \
      && systemctl is-active --quiet sysbox-fs && break
    sleep 1
  done
  systemctl is-active --quiet sysbox || die "sysbox.service did not come up — journalctl -u sysbox-mgr -u sysbox-fs"
  command -v sysbox-runc >/dev/null 2>&1 || die "sysbox-runc binary not on PATH after install"
  ok "sysbox-ce ${SYSBOX_VERSION} installed and sysbox/sysbox-mgr/sysbox-fs active"

  # Wire sysbox-runc into k3s's bundled containerd.
  #
  # k3s picks up EXACTLY ONE containerd config template, newest schema
  # wins. Since k3s v1.27 that is config-v3.toml.tmpl (containerd 2.x,
  # CRI plugin "io.containerd.cri.v1.runtime"); older k3s used
  # config.toml.tmpl (containerd 1.x, "io.containerd.grpc.v1.cri").
  # The trial box (k3s v1.36 / containerd 2.x) ignored the v1 template,
  # which is why the runtime was "not configured". Write BOTH templates
  # with the section header matching each schema; k3s renders whichever
  # one matches its bundled containerd and ignores the other.
  CTRD_DIR=/var/lib/rancher/k3s/agent/etc/containerd
  mkdir -p "$CTRD_DIR"

  _seed_tmpl() { # $1 = template filename to seed from the live rendered config
    local f="$CTRD_DIR/$1"
    if [ ! -f "$f" ] && [ -f "$CTRD_DIR/config.toml" ]; then
      cp "$CTRD_DIR/config.toml" "$f"
    fi
    [ -f "$f" ] || printf '{{ template "base" . }}\n' > "$f"
    echo "$f"
  }

  # --- v3 schema (containerd 2.x / k3s >= v1.27) ---
  T3="$(_seed_tmpl config-v3.toml.tmpl)"
  if ! grep -q 'runtimes.sysbox-runc' "$T3" 2>/dev/null; then
    cat >> "$T3" <<'EOF'

# --- Sysbox runtime (added by k3s-sysbox-node.sh) ---
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.sysbox-runc]
  runtime_type = "io.containerd.runc.v2"
  pod_annotations = ["io.kubernetes.cri-o.userns-mode", "*.sysbox.*"]
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.sysbox-runc.options]
  BinaryName = "/usr/bin/sysbox-runc"
  SystemdCgroup = true
EOF
    ok "sysbox-runc added to config-v3.toml.tmpl (containerd 2.x schema)"
  fi

  # --- v1/v2 schema (containerd 1.x / k3s < v1.27) — harmless on newer ---
  T1="$(_seed_tmpl config.toml.tmpl)"
  if ! grep -q 'runtimes.sysbox-runc' "$T1" 2>/dev/null; then
    cat >> "$T1" <<'EOF'

# --- Sysbox runtime (added by k3s-sysbox-node.sh) ---
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.sysbox-runc]
  runtime_type = "io.containerd.runc.v2"
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.sysbox-runc.options]
  BinaryName = "/usr/bin/sysbox-runc"
  SystemdCgroup = true
EOF
    ok "sysbox-runc added to config.toml.tmpl (containerd 1.x schema)"
  fi

  systemctl restart k3s
  for i in $(seq 1 60); do
    $KUBECTL get --raw='/readyz' >/dev/null 2>&1 && break
    sleep 2
  done

  # ASSERT the runtime is actually live in containerd — fail here, loudly,
  # rather than 2 minutes later at a pod-sandbox error.
  RUNTIME_OK=0
  for i in $(seq 1 20); do
    if /usr/local/bin/k3s crictl info 2>/dev/null | grep -q '"sysbox-runc"' \
       || grep -rq 'runtimes.sysbox-runc' "$CTRD_DIR/config.toml" 2>/dev/null; then
      RUNTIME_OK=1; break
    fi
    sleep 2
  done
  [ "$RUNTIME_OK" = 1 ] || {
    echo "  --- rendered $CTRD_DIR/config.toml (runtimes section) ---"
    grep -A3 'containerd.runtimes' "$CTRD_DIR/config.toml" 2>/dev/null | head -40 || true
    die "sysbox-runc did not appear in the rendered containerd config after k3s restart — check k3s version vs. the two templates above"
  }
  ok "k3s restarted; sysbox-runc live in containerd"
fi

# ---------------------------------------------------------------------------
step "4/5 apply the sysbox-runc RuntimeClass"
$KUBECTL apply -f - <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: sysbox-runc
handler: sysbox-runc
EOF
ok "RuntimeClass sysbox-runc applied"

# ---------------------------------------------------------------------------
step "5/5 verify: a Sysbox pod runs dockerd + hello-world"
$KUBECTL delete pod sb-smoke --ignore-not-found --wait=true >/dev/null 2>&1 || true
cat <<'EOF' | $KUBECTL apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: sb-smoke
  namespace: default
spec:
  runtimeClassName: sysbox-runc
  restartPolicy: Never
  containers:
    - name: main
      image: nestybox/ubuntu-noble-systemd-docker
      command: ["/sbin/init"]
EOF

echo "  waiting for sb-smoke to be Running..."
for i in $(seq 1 60); do
  ph="$($KUBECTL get pod sb-smoke -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  [ "$ph" = "Running" ] && break
  [ "$ph" = "Failed" ] && { $KUBECTL describe pod sb-smoke | tail -30; die "sb-smoke pod Failed"; }
  sleep 2
done
[ "$($KUBECTL get pod sb-smoke -o jsonpath='{.status.phase}')" = "Running" ] || {
  $KUBECTL describe pod sb-smoke | tail -30
  die "sb-smoke never reached Running — Sysbox runtime not working"
}

echo "  starting dockerd inside the Sysbox pod + running hello-world..."
if $KUBECTL exec sb-smoke -- bash -c '
    systemctl start docker 2>/dev/null || (dockerd >/tmp/d.log 2>&1 &)
    for i in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
    docker run --rm hello-world' | grep -q "Hello from Docker"; then
  ok "Docker-in-Docker works inside a Sysbox pod"
else
  $KUBECTL exec sb-smoke -- bash -c 'cat /tmp/d.log 2>/dev/null; journalctl -u docker --no-pager 2>/dev/null | tail -20' || true
  die "DinD smoke test failed inside the Sysbox pod"
fi

echo "  checking systemd is PID 1..."
$KUBECTL exec sb-smoke -- bash -c 'ps -p 1 -o comm= | grep -q systemd' \
  && ok "systemd runs as PID 1" \
  || die "systemd is not PID 1 in the Sysbox pod"

$KUBECTL delete pod sb-smoke --ignore-not-found --wait=false >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
step "DONE"
cat <<EOF

k3s + Sysbox are up on this node.

  kubeconfig:      /etc/rancher/k3s/k3s.yaml
  node label:      practiceengine.dev/tier=t1
  RuntimeClass:    sysbox-runc  (matches ORCHESTRATOR_T2_RUNTIME_CLASS default)

Next:
  1. Copy /etc/rancher/k3s/k3s.yaml to your workstation, rewrite the
     server: address to this box's IP, use it as KUBECONFIG.
  2. Deploy the platform: orchestrator, practice-core, web, Postgres,
     Redis, NATS (see infra/practice-cluster/platform/ or docker-compose
     'app' profile adapted for k3s).
  3. Set on the orchestrator:
        ORCHESTRATOR_T2_ENABLED=true
        ORCHESTRATOR_T2_RUNTIME_CLASS=sysbox-runc   (this is the default)
  4. Run the end-to-end check:
        scripts/t2-lifecycle-check.sh
     It must pass create -> connect -> DinD/k3s/systemd/eBPF -> destroy
     -> zero-leftover before Phase 2 requirement A is marked done.
  5. go test ./orchestrator/internal/k8s/ -run TestProvisionT2_Lifecycle
     against this cluster -> it must take the POSITIVE branch and pass.
EOF
