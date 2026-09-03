#!/usr/bin/env bash
# provider-acceptance-check.sh — decide GO / NO-GO for an ARM VPS/metal
# provider as the Practice Engine practice-cluster host.
#
# Runs EVERY mandatory capability check for the k3s + Sysbox architecture
# (contracts: memory.md §5.2, orchestrator/docs/t2-cost-optimization-100.md,
# infra/practice-cluster/bootstrap/k3s-sysbox-node.sh), then optionally
# runs the real bootstrap + a nested-k3d-in-Sysbox test.
#
#   USAGE — on a FRESH trial box, as root:
#     scp infra/practice-cluster/provider-acceptance-check.sh \
#         infra/practice-cluster/bootstrap/k3s-sysbox-node.sh  root@<TRIAL_IP>:/root/
#     ssh root@<TRIAL_IP>
#     bash /root/provider-acceptance-check.sh 2>&1 | tee /root/acceptance-<provider>.log
#
#   PHASES:
#     1  static capability probes (no installs)   — always run
#     2  short burst / IO / egress probes         — always run
#     3  run k3s-sysbox-node.sh (the real thing)  — set RUN_BOOTSTRAP=1 (default 1)
#     4  nested k3d cluster inside the Sysbox pod — needs phase 3 pass
#
#   ENV:
#     RUN_BOOTSTRAP=0     skip phases 3+4 (capability-only quick pass)
#     EGRESS_TEST_GB=5    size of the image-pull egress probe
#     SKIP_EGRESS=1       skip the egress pull probe (slow / metered links)
#
# Exit 0 = FULLY SATISFIES (every mandatory check PASS).
# Exit 1 = NO-GO (>=1 mandatory FAIL).
# Exit 2 = INCONCLUSIVE (an optional/burst check needs a human eyeball).
set -uo pipefail

EGRESS_TEST_GB="${EGRESS_TEST_GB:-5}"
RUN_BOOTSTRAP="${RUN_BOOTSTRAP:-1}"
SKIP_EGRESS="${SKIP_EGRESS:-0}"

c_g=$'\033[1;32m'; c_r=$'\033[1;31m'; c_y=$'\033[1;33m'; c_b=$'\033[1;34m'; c_0=$'\033[0m'
FAILS=0; WARNS=0; PASSES=0
declare -a ROWS

row() { # row <check> <PASS|FAIL|WARN|INFO> <detail>
  local st="$2"
  case "$st" in
    PASS) PASSES=$((PASSES+1)); printf '%sPASS%s  %-34s %s\n' "$c_g" "$c_0" "$1" "${3:-}";;
    FAIL) FAILS=$((FAILS+1));   printf '%sFAIL%s  %-34s %s\n' "$c_r" "$c_0" "$1" "${3:-}";;
    WARN) WARNS=$((WARNS+1));   printf '%sWARN%s  %-34s %s\n' "$c_y" "$c_0" "$1" "${3:-}";;
    INFO)                       printf '%sINFO%s  %-34s %s\n' "$c_b" "$c_0" "$1" "${3:-}";;
  esac
  ROWS+=("$1|$st|${3:-}")
}
hdr() { printf '\n%s========== %s ==========%s\n' "$c_b" "$*" "$c_0"; }

[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }

# =====================================================================
hdr "PHASE 0 — host identity"
# =====================================================================
ARCH="$(uname -m)"
KVER="$(uname -r)"
OSREL="$(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-unknown}")"
VIRT="$(systemd-detect-virt 2>/dev/null || echo unknown)"
NCPU="$(nproc)"
MEMGB="$(awk '/MemTotal/{printf "%.1f", $2/1024/1024}' /proc/meminfo)"
DISKGB="$(df -BG --output=size / | tail -1 | tr -dc '0-9')"
row "arch"            INFO "$ARCH"
row "kernel"          INFO "$KVER"
row "os"              INFO "$OSREL"
row "virt type"       INFO "$VIRT"
row "vCPU"            INFO "$NCPU"
row "RAM (GiB)"       INFO "$MEMGB"
row "root disk (GiB)" INFO "$DISKGB"

# =====================================================================
hdr "PHASE 1 — mandatory capability probes"
# =====================================================================

# --- ARM64 -----------------------------------------------------------
case "$ARCH" in
  aarch64|arm64) row "ARM64 / aarch64" PASS "$ARCH" ;;
  x86_64)        row "ARM64 / aarch64" WARN "x86_64 — Sysbox works but this is not the target arch" ;;
  *)             row "ARM64 / aarch64" FAIL "unsupported arch $ARCH" ;;
esac

# --- Ubuntu 24.04 --------------------------------------------------
if grep -qi '24\.04' /etc/os-release 2>/dev/null && grep -qi ubuntu /etc/os-release; then
  row "Ubuntu 24.04" PASS "$OSREL"
else
  row "Ubuntu 24.04" WARN "$OSREL — bootstrap script targets 24.04 (kernel 6.8); other distros may work if kernel >=5.12"
fi

# --- KVM/QEMU or bare metal (NOT container virt) --------------------
case "$VIRT" in
  kvm|qemu|amazon|microsoft|xen|vmware|oracle)
    row "KVM/QEMU or bare metal" PASS "systemd-detect-virt=$VIRT" ;;
  none)
    row "KVM/QEMU or bare metal" PASS "bare metal (systemd-detect-virt=none)" ;;
  lxc|openvz|lxc-libvirt|systemd-nspawn|docker|podman|container-other|proot|pouch)
    row "KVM/QEMU or bare metal" FAIL "container-type virt ($VIRT) — Sysbox + k3s cannot run properly. NO-GO." ;;
  *)
    row "KVM/QEMU or bare metal" WARN "unrecognised virt '$VIRT' — inspect manually (must not be an OS container)" ;;
esac

# --- full root -----------------------------------------------------
if [ "$(id -u)" = 0 ] && touch /root/.acc_root_test 2>/dev/null && rm -f /root/.acc_root_test; then
  row "full root access" PASS "uid 0, writable /root"
else
  row "full root access" FAIL "not real root"
fi
# capability set — a container-VPS often drops these
if command -v capsh >/dev/null 2>&1; then
  CAPS="$(capsh --print 2>/dev/null | awk -F= '/Current:/{print $2}')"
else
  CAPS="$(grep CapEff /proc/self/status | awk '{print $2}')"
fi
if grep -qiE 'cap_sys_admin|0000003fffffffff|000001ffffffffff|CapEff.*ffffff' <<<"$CAPS" || \
   ( command -v capsh >/dev/null 2>&1 && capsh --print 2>/dev/null | grep -q 'cap_sys_admin' ); then
  row "CAP_SYS_ADMIN present" PASS
else
  row "CAP_SYS_ADMIN present" WARN "could not confirm full capability set ($CAPS) — verify Sysbox mounts work in phase 3"
fi

# --- kernel >= 5.12 ----------------------------------------------
KMAJ="${KVER%%.*}"; KMIN="$(echo "$KVER" | cut -d. -f2 | tr -dc '0-9')"
if [ "${KMAJ:-0}" -gt 5 ] || { [ "${KMAJ:-0}" -eq 5 ] && [ "${KMIN:-0}" -ge 12 ]; }; then
  row "kernel >= 5.12" PASS "$KVER"
else
  row "kernel >= 5.12" FAIL "$KVER < 5.12 — Sysbox idmapped-mount/shiftfs needs >=5.12"
fi

# --- unprivileged user namespaces --------------------------------
UNS="$(sysctl -n kernel.unprivileged_userns_clone 2>/dev/null || echo missing)"
MAXUNS="$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 0)"
if [ "$UNS" = "missing" ]; then
  # not all kernels expose the knob; the functional test below is authoritative
  :
elif [ "$UNS" != "1" ]; then
  if sysctl -w kernel.unprivileged_userns_clone=1 >/dev/null 2>&1; then
    row "userns sysctl settable" PASS "was $UNS, set to 1 (persist via /etc/sysctl.d)"
  else
    row "userns sysctl settable" FAIL "kernel.unprivileged_userns_clone=$UNS and cannot be changed — locked kernel"
  fi
else
  row "userns sysctl settable" PASS "kernel.unprivileged_userns_clone=1"
fi
if [ "${MAXUNS:-0}" -lt 1000 ]; then
  row "max_user_namespaces" WARN "only $MAXUNS — raise via sysctl user.max_user_namespaces"
else
  row "max_user_namespaces" PASS "$MAXUNS"
fi
# functional: can an unprivileged userns actually be created?
if command -v unshare >/dev/null 2>&1; then
  if unshare -Ur --map-root-user sh -c 'id -u' 2>/dev/null | grep -qx 0; then
    row "unprivileged userns works" PASS "unshare -Ur -> uid 0 inside"
  else
    row "unprivileged userns works" FAIL "unshare -Ur failed — userns disabled/blocked. Sysbox CANNOT work. NO-GO."
  fi
else
  row "unprivileged userns works" WARN "unshare not installed — apt-get install -y util-linux and retry"
fi

# --- cgroup v2 --------------------------------------------------
CGT="$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)"
if [ "$CGT" = "cgroup2fs" ]; then
  row "cgroup v2 (unified)" PASS "$CGT"
elif [ "$CGT" = "tmpfs" ]; then
  row "cgroup v2 (unified)" WARN "hybrid/v1 layout — k3s wires SystemdCgroup for sysbox-runc; v2 strongly preferred (Ubuntu 24.04 default). Set systemd.unified_cgroup_hierarchy=1."
else
  row "cgroup v2 (unified)" FAIL "cannot determine cgroup layout ($CGT)"
fi
if command -v systemctl >/dev/null 2>&1 && systemctl show -p Delegate --value user.slice 2>/dev/null | grep -qi yes; then
  row "cgroup delegation" PASS
else
  row "cgroup delegation" WARN "systemd cgroup delegation not confirmed — usually fine on 24.04"
fi

# --- overlayfs -------------------------------------------------
if modprobe overlay 2>/dev/null || grep -qw overlay /proc/filesystems; then
  row "overlayfs" PASS "$(grep -qw overlay /proc/filesystems && echo builtin || echo module-loaded)"
  # can we actually mount one? (DinD-in-Sysbox uses overlay2)
  T=$(mktemp -d); mkdir -p "$T"/{l,u,w,m}
  if mount -t overlay overlay -o "lowerdir=$T/l,upperdir=$T/u,workdir=$T/w" "$T/m" 2>/dev/null; then
    umount "$T/m"; row "overlayfs mount" PASS
  else
    row "overlayfs mount" WARN "overlay present but a test mount failed — nested overlay may still work inside userns"
  fi
  rm -rf "$T"
else
  row "overlayfs" FAIL "no overlay filesystem — containerd/DinD storage driver unavailable"
fi

# --- /dev/fuse ----------------------------------------------
if [ -c /dev/fuse ]; then
  row "/dev/fuse" PASS
elif modprobe fuse 2>/dev/null && [ -c /dev/fuse ]; then
  row "/dev/fuse" PASS "fuse module loaded on demand"
else
  row "/dev/fuse" FAIL "no /dev/fuse — Sysbox's fuse-backed procfs/sysfs virt unavailable"
fi

# --- iptables / nftables control ---------------------------
IPT_OK=0
if command -v iptables >/dev/null 2>&1; then
  if iptables -N ACC_TEST 2>/dev/null && iptables -A ACC_TEST -j RETURN 2>/dev/null && iptables -X ACC_TEST 2>/dev/null; then
    IPT_OK=1
  fi
fi
NFT_OK=0
if command -v nft >/dev/null 2>&1; then
  if nft add table inet acc_test 2>/dev/null && nft delete table inet acc_test 2>/dev/null; then
    NFT_OK=1
  fi
fi
if [ "$IPT_OK" = 1 ] || [ "$NFT_OK" = 1 ]; then
  row "iptables/nftables control" PASS "iptables:$([ $IPT_OK = 1 ] && echo ok || echo no) nft:$([ $NFT_OK = 1 ] && echo ok || echo no)"
else
  row "iptables/nftables control" FAIL "cannot create firewall rules as root — provider-locked netfilter. k3s/flannel/NetworkPolicy will not work. NO-GO."
fi

# --- VXLAN 8472 (flannel default backend) -----------------
if ip link add acc_vx type vxlan id 42 dstport 8472 dev lo 2>/dev/null; then
  ip link del acc_vx 2>/dev/null
  row "VXLAN 8472 (flannel)" PASS "vxlan device creatable"
else
  # try loading the module first
  if modprobe vxlan 2>/dev/null && ip link add acc_vx type vxlan id 42 dstport 8472 dev lo 2>/dev/null; then
    ip link del acc_vx 2>/dev/null
    row "VXLAN 8472 (flannel)" PASS "vxlan module loaded on demand"
  else
    row "VXLAN 8472 (flannel)" FAIL "cannot create a VXLAN device — flannel pod network broken. Use host-gw only if single-node; NO-GO for multi-node scaling."
  fi
fi
# UDP 8472 not blocked to self
if command -v nc >/dev/null 2>&1; then
  (timeout 2 nc -u -l 8472 &) 2>/dev/null; sleep 0.3
  echo x | timeout 2 nc -u -w1 127.0.0.1 8472 2>/dev/null && row "UDP 8472 loopback" PASS || row "UDP 8472 loopback" WARN "loopback UDP 8472 probe inconclusive"
  pkill -f 'nc -u -l 8472' 2>/dev/null || true
fi

# --- eBPF / bpftrace -------------------------------------
BPF_LOCK="$(cat /sys/kernel/security/lockdown 2>/dev/null || echo 'none')"
if grep -q '\[confidentiality\]' <<<"$BPF_LOCK"; then
  row "kernel lockdown" FAIL "lockdown=confidentiality — blocks eBPF/BPF program load. NO-GO for T2 eBPF content."
elif grep -q '\[integrity\]' <<<"$BPF_LOCK"; then
  row "kernel lockdown" WARN "lockdown=integrity — some BPF/XDP restricted; kprobes/tracepoints usually ok"
else
  row "kernel lockdown" PASS "lockdown=${BPF_LOCK//$'\n'/ }"
fi
if command -v bpftrace >/dev/null 2>&1; then
  if bpftrace -e 'BEGIN { exit(); }' >/dev/null 2>&1; then
    row "bpftrace runs" PASS
  else
    row "bpftrace runs" FAIL "bpftrace present but cannot attach — BPF blocked"
  fi
else
  # try a minimal BPF syscall via python/perf as a proxy
  if [ -r /proc/sys/kernel/unprivileged_bpf_disabled ]; then
    UBPF="$(cat /proc/sys/kernel/unprivileged_bpf_disabled)"
    row "bpftrace runs" WARN "bpftrace not installed (apt-get install -y bpftrace to test). unprivileged_bpf_disabled=$UBPF (root BPF still ok unless lockdown)"
  else
    row "bpftrace runs" WARN "bpftrace not installed; could not probe BPF sysctl"
  fi
fi
# BTF present — needed for CO-RE eBPF (most modern bpftrace scripts)
if [ -r /sys/kernel/btf/vmlinux ]; then
  row "kernel BTF (/sys/kernel/btf/vmlinux)" PASS
else
  row "kernel BTF (/sys/kernel/btf/vmlinux)" WARN "no BTF — CO-RE eBPF scripts need it; provider kernel built without CONFIG_DEBUG_INFO_BTF"
fi

# --- stable public IPv4 --------------------------------
PUB4="$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)"
EXT4="$(timeout 5 curl -4 -s https://api.ipify.org 2>/dev/null || timeout 5 curl -4 -s https://ifconfig.me 2>/dev/null || echo '')"
if [ -n "$PUB4" ]; then
  case "$PUB4" in
    10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*|100.6[4-9].*|100.[7-9][0-9].*|100.1[01][0-9].*|100.12[0-7].*)
      if [ -n "$EXT4" ]; then
        row "public IPv4" WARN "NIC has private $PUB4 but egress IP is $EXT4 — provider does 1:1 NAT. Inbound needs a floating/public IP mapping; confirm it's static."
      else
        row "public IPv4" FAIL "only a private address ($PUB4) and no external IPv4 detected"
      fi
      ;;
    *)
      row "public IPv4" PASS "$PUB4${EXT4:+ (egress $EXT4)}"
      ;;
  esac
else
  row "public IPv4" FAIL "no global-scope IPv4 on any interface"
fi

# --- storage size -------------------------------------
# check the largest mounted fs we can actually use (root, or a data mount)
BIGGEST=$(df -BG --output=avail,target 2>/dev/null | tail -n +2 | sort -rn | head -1)
BIGGEST_GB=$(echo "$BIGGEST" | tr -dc '0-9' | head -c4)
ROOT_GB="$DISKGB"
if [ "${ROOT_GB:-0}" -ge 160 ]; then
  row "storage >= 80GB (160+ pref)" PASS "root ${ROOT_GB}GB"
elif [ "${ROOT_GB:-0}" -ge 80 ]; then
  row "storage >= 80GB (160+ pref)" WARN "root ${ROOT_GB}GB — meets 80GB min, below 160GB preferred (base images + pre-pull top 20 + Postgres + recordings buffer)"
else
  row "storage >= 80GB (160+ pref)" FAIL "root only ${ROOT_GB}GB — attach/resize a volume to >=80GB before production"
fi

# =====================================================================
hdr "PHASE 2 — burst / IO / egress probes"
# =====================================================================

# --- CPU steal under burst -----------------------------
apt-get install -y -qq sysstat stress-ng fio util-linux >/dev/null 2>&1 || true
if command -v mpstat >/dev/null 2>&1 && command -v stress-ng >/dev/null 2>&1; then
  echo "  running a 30s all-core stress burst, sampling %steal..."
  stress-ng --cpu "$NCPU" --timeout 32s >/dev/null 2>&1 &
  SPID=$!
  sleep 2
  STEAL="$(mpstat 1 25 2>/dev/null | awk '/Average:/ && $2 ~ /all/ {print $NF}')"
  wait $SPID 2>/dev/null || true
  STEAL="${STEAL:-unknown}"
  if [ "$STEAL" = unknown ]; then
    row "CPU steal < 5% under burst" WARN "mpstat gave no reading — re-run: mpstat 1 30 during a stress-ng burst"
  else
    awk -v s="$STEAL" 'BEGIN{exit !(s+0 < 5)}' \
      && row "CPU steal < 5% under burst" PASS "avg %steal = ${STEAL}" \
      || { awk -v s="$STEAL" 'BEGIN{exit !(s+0 < 10)}' \
           && row "CPU steal < 5% under burst" WARN "avg %steal = ${STEAL} (<10%, marginal — misses p95 time-to-ready under load)" \
           || row "CPU steal < 5% under burst" FAIL "avg %steal = ${STEAL} — oversubscribed host, will miss provision SLO. NO-GO for production node."; }
  fi
else
  row "CPU steal < 5% under burst" WARN "stress-ng/mpstat unavailable — install sysstat stress-ng and re-run"
fi

# --- disk I/O -----------------------------------------
if command -v fio >/dev/null 2>&1; then
  echo "  fio 4k random-write, 15s, direct..."
  FIO_JSON="$(fio --name=acc --filename=/root/.acc_fio --rw=randwrite --bs=4k --iodepth=32 \
      --direct=1 --runtime=15 --time_based --size=1G --output-format=json 2>/dev/null)"
  # Parse robustly — fio's JSON key spacing varies by version
  # ("iops" : 1234  vs  "iops":1234.5), and --direct=1 can be refused on
  # some overlay/loop mounts (then retry buffered before calling it a fail).
  _fio_num() { echo "$FIO_JSON" | tr -d ' ' | grep -o "\"$1\":[0-9.]*" | head -1 | grep -o '[0-9.]*'; }
  IOPS="$(_fio_num iops)"
  BWMB="$(_fio_num bw)"
  if [ -z "$IOPS" ] || [ "${IOPS%.*}" = 0 ]; then
    echo "  (direct I/O gave no result — retrying buffered)"
    FIO_JSON="$(fio --name=acc --filename=/root/.acc_fio --rw=randwrite --bs=4k --iodepth=32 \
        --direct=0 --fsync=16 --runtime=15 --time_based --size=1G --output-format=json 2>/dev/null)"
    IOPS="$(_fio_num iops)"
    BWMB="$(_fio_num bw)"
  fi
  rm -f /root/.acc_fio
  BWMB=$(( ${BWMB%.*} / 1024 ))
  IOPS_INT="${IOPS%.*}"
  if [ "${IOPS_INT:-0}" -ge 5000 ]; then
    row "disk I/O (>=5k rand-write IOPS)" PASS "${IOPS_INT} IOPS, ~${BWMB} MB/s"
  elif [ "${IOPS_INT:-0}" -ge 2000 ]; then
    row "disk I/O (>=5k rand-write IOPS)" WARN "${IOPS_INT} IOPS — image pulls + containerd + Postgres will be slow-ish under cohort load"
  else
    row "disk I/O (>=5k rand-write IOPS)" FAIL "${IOPS_INT:-0} IOPS — network/HDD-backed storage, provisioning will stall. NO-GO."
  fi
else
  row "disk I/O (>=5k rand-write IOPS)" WARN "fio unavailable — install and re-run"
fi

# --- egress allowance / throttle ---------------------
if [ "$SKIP_EGRESS" = 1 ]; then
  row "egress: pull ${EGRESS_TEST_GB}GB unthrottled" WARN "SKIP_EGRESS=1 — verify the provider's included monthly egress is >= 1 TB from their pricing page"
else
  echo "  pulling ~${EGRESS_TEST_GB}GB to check for throttling / metering..."
  T0=$(date +%s); BYTES=0
  for i in $(seq 1 "$EGRESS_TEST_GB"); do
    B=$(timeout 60 curl -s -o /dev/null -w '%{size_download}' \
        "https://speed.hetzner.de/1GB.bin" 2>/dev/null || echo 0)
    BYTES=$((BYTES + B))
    [ "$B" -lt 900000000 ] && break
  done
  T1=$(date +%s); DUR=$((T1 - T0)); [ "$DUR" -lt 1 ] && DUR=1
  MBPS=$(( BYTES / DUR / 1000000 ))
  GBPULLED=$(( BYTES / 1000000000 ))
  if [ "$MBPS" -ge 50 ] && [ "$GBPULLED" -ge $((EGRESS_TEST_GB - 1)) ]; then
    row "egress: pull ${EGRESS_TEST_GB}GB unthrottled" PASS "${GBPULLED}GB @ ~${MBPS} MB/s — no throttle. CONFIRM monthly cap >=1TB on pricing page."
  elif [ "$MBPS" -ge 10 ]; then
    row "egress: pull ${EGRESS_TEST_GB}GB unthrottled" WARN "~${MBPS} MB/s — slow; cohort-start image pulls will lag. Check for a burst cap."
  else
    row "egress: pull ${EGRESS_TEST_GB}GB unthrottled" FAIL "~${MBPS} MB/s over ${GBPULLED}GB — hard throttle or tiny quota. NO-GO."
  fi
fi

# =====================================================================
hdr "PHASE 3 — the real bootstrap (k3s-sysbox-node.sh)"
# =====================================================================
if [ "$RUN_BOOTSTRAP" != 1 ]; then
  row "k3s-sysbox-node.sh" WARN "RUN_BOOTSTRAP=0 — skipped. Re-run with RUN_BOOTSTRAP=1 for a real GO decision."
elif [ ! -f /root/k3s-sysbox-node.sh ] && [ ! -f "$(dirname "$0")/bootstrap/k3s-sysbox-node.sh" ]; then
  row "k3s-sysbox-node.sh" FAIL "script not found — scp infra/practice-cluster/bootstrap/k3s-sysbox-node.sh to /root/ and re-run"
else
  BS=/root/k3s-sysbox-node.sh
  [ -f "$BS" ] || BS="$(dirname "$0")/bootstrap/k3s-sysbox-node.sh"
  echo "  running $BS  (installs k3s + Sysbox, ~3–6 min)..."
  if bash "$BS"; then
    row "k3s-sysbox-node.sh completes" PASS "bootstrap exited 0"
  else
    row "k3s-sysbox-node.sh completes" FAIL "bootstrap failed — see output above (arch/kernel/userns/sysbox .deb/DinD smoke test)"
  fi

  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
  if k3s kubectl get nodes 2>/dev/null | grep -q ' Ready '; then
    row "k3s node Ready" PASS
  else
    row "k3s node Ready" FAIL "no Ready node after bootstrap"
  fi
  if k3s kubectl get runtimeclass sysbox-runc >/dev/null 2>&1; then
    row "RuntimeClass sysbox-runc" PASS
  else
    row "RuntimeClass sysbox-runc" FAIL "sysbox-runc RuntimeClass missing"
  fi

  # =================================================================
  hdr "PHASE 4 — nested k3d cluster inside a Sysbox pod"
  # =================================================================
  # This is the T2 requirement: real nested multi-node k3s in a Sysbox
  # pod (memory.md §5.1 / t2-cost-optimization-100.md §2 capability table).
  #
  # Size the probe pod to what the NODE can actually give (allocatable
  # minus what k3s+system already use), so a small trial box measures the
  # capability instead of failing on FailedScheduling. A spec-sized T2 env
  # is 4 vCPU / 8 GiB (t2-cost-optimization-100.md §5.1); a 1-server +
  # 1-agent k3d comes up in ~1.5 vCPU / 2.5 GiB.
  ALLOC_CPU="$(k3s kubectl get node -o jsonpath='{.items[0].status.allocatable.cpu}' 2>/dev/null | tr -d 'm')"
  ALLOC_MEM_KI="$(k3s kubectl get node -o jsonpath='{.items[0].status.allocatable.memory}' 2>/dev/null | tr -d 'Ki')"
  # crude: leave ~1 core and ~1.2Gi for k3s+system, cap request at the T2 spec
  [ "${ALLOC_CPU:-0}" -gt 1000 ] 2>/dev/null && REQ_CPU_M=$(( ALLOC_CPU > 5000 ? 4000 : ALLOC_CPU - 1000 )) || REQ_CPU_M=1000
  [ "${ALLOC_MEM_KI:-0}" -gt 2000000 ] 2>/dev/null && REQ_MEM_MI=$(( ALLOC_MEM_KI/1024 > 9400 ? 8192 : (ALLOC_MEM_KI/1024) - 1200 )) || REQ_MEM_MI=2048
  if [ "$REQ_MEM_MI" -lt 2048 ] || [ "$REQ_CPU_M" -lt 1000 ]; then
    row "Sysbox pod Running" WARN "node allocatable too small (${REQ_CPU_M}m / ${REQ_MEM_MI}Mi free) to host a nested-k3d probe — re-run PHASE 4 on the production-sized box"
    k3s kubectl delete pod acc-nested --ignore-not-found --wait=false >/dev/null 2>&1 || true
    OK=skip
  fi
  if [ "${OK:-}" != skip ]; then
  echo "  probe pod request: ${REQ_CPU_M}m CPU / ${REQ_MEM_MI}Mi mem (node-adaptive; T2 spec is 4000m/8192Mi)"
  cat <<EOF | k3s kubectl apply -f - >/dev/null 2>&1
apiVersion: v1
kind: Pod
metadata:
  name: acc-nested
  namespace: default
spec:
  runtimeClassName: sysbox-runc
  restartPolicy: Never
  containers:
    - name: main
      image: nestybox/ubuntu-noble-systemd-docker
      command: ["/sbin/init"]
      resources:
        requests: {cpu: "${REQ_CPU_M}m", memory: "${REQ_MEM_MI}Mi"}
        limits:   {cpu: "${REQ_CPU_M}m", memory: "${REQ_MEM_MI}Mi"}
EOF
  echo "  waiting for acc-nested to be Running..."
  OK=0
  for i in $(seq 1 90); do
    ph="$(k3s kubectl get pod acc-nested -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    [ "$ph" = "Running" ] && { OK=1; break; }
    [ "$ph" = "Failed" ] && break
    sleep 2
  done
  if [ "$OK" != 1 ]; then
    k3s kubectl describe pod acc-nested 2>/dev/null | tail -25
    row "Sysbox pod Running" FAIL "acc-nested never reached Running"
  else
    row "Sysbox pod Running" PASS

    echo "  starting dockerd + creating a 1-server 1-agent k3d cluster inside the pod..."
    NESTED="$(k3s kubectl exec acc-nested -- bash -c '
      set -e
      systemctl start docker 2>/dev/null || (dockerd >/tmp/d.log 2>&1 &)
      for i in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
      docker info >/dev/null 2>&1 || { echo "DIND_FAIL"; exit 1; }
      curl -sfL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash >/dev/null 2>&1
      k3d cluster create acc --servers 1 --agents 1 --wait --timeout 180s >/tmp/k3d.log 2>&1 || { echo "K3D_FAIL"; tail -20 /tmp/k3d.log; exit 1; }
      export KUBECONFIG="$(k3d kubeconfig write acc)"
      kubectl wait --for=condition=Ready nodes --all --timeout=120s >/dev/null 2>&1 || { echo "NODES_NOT_READY"; exit 1; }
      N=$(kubectl get nodes --no-headers | wc -l)
      echo "NESTED_OK nodes=$N"
      ps -p 1 -o comm= | grep -q systemd && echo "SYSTEMD_PID1_OK"
    ' 2>&1)"
    echo "$NESTED" | sed 's/^/    /'
    if grep -q 'NESTED_OK nodes=2' <<<"$NESTED"; then
      row "nested k3d (1 server + 1 agent) Ready" PASS "2 nodes Ready inside the Sysbox pod"
    elif grep -q 'NESTED_OK' <<<"$NESTED"; then
      row "nested k3d (1 server + 1 agent) Ready" WARN "nested cluster came up but node count != 2"
    elif grep -q 'DIND_FAIL' <<<"$NESTED"; then
      row "nested k3d (1 server + 1 agent) Ready" FAIL "Docker-in-Docker did not start inside the Sysbox pod"
    else
      row "nested k3d (1 server + 1 agent) Ready" FAIL "nested k3d cluster failed to reach Ready (see log above)"
    fi
    grep -q 'SYSTEMD_PID1_OK' <<<"$NESTED" \
      && row "systemd PID 1 in Sysbox pod" PASS \
      || row "systemd PID 1 in Sysbox pod" WARN "could not confirm systemd as PID 1"
  fi
  k3s kubectl delete pod acc-nested --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi   # end: OK != skip
fi     # end: RUN_BOOTSTRAP

# =====================================================================
hdr "VERDICT"
# =====================================================================
printf '  %sPASS %d%s   %sWARN %d%s   %sFAIL %d%s\n\n' \
  "$c_g" "$PASSES" "$c_0" "$c_y" "$WARNS" "$c_0" "$c_r" "$FAILS" "$c_0"

echo "  --- machine-readable matrix (check|status|detail) ---"
for r in "${ROWS[@]}"; do echo "  $r"; done
echo "  ----------------------------------------------------"

if [ "$FAILS" -gt 0 ]; then
  printf '\n%sNO-GO%s — %d mandatory check(s) failed. This provider does NOT fully satisfy the k3s + Sysbox requirements.\n' "$c_r" "$c_0" "$FAILS"
  exit 1
elif [ "$WARNS" -gt 0 ]; then
  printf '\n%sCONDITIONAL%s — 0 hard failures but %d warning(s). Resolve each (kernel knob, disk size, egress cap, latency) before calling this provider FULLY SATISFIES.\n' "$c_y" "$c_0" "$WARNS"
  exit 2
else
  printf '\n%sGO / FULLY SATISFIES%s — every mandatory check passed. Safe to select for production.\n' "$c_g" "$c_0"
  exit 0
fi
