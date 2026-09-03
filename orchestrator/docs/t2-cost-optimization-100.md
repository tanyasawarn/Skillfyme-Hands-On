# T2 at ₹100/user/month — the Sysbox decision

**Constraint:** total cost per user, **including everything** (compute,
AI, storage), must stay under **₹100/user/month** (≈ **$1.20** at ₹83/$).

**Decision:** T2 runs under the **Sysbox** container runtime
(`sysbox-runc`) on the **same shared node pool as T1** — not on a
dedicated Kata/Firecracker bare-metal pool. This document is the
reasoning, the cost model, and the Sysbox-vs-Kata comparison.

This supersedes `t2-cost-optimization.md` (which targeted ₹300/user and
still assumed a Kata metal pool). The Kata path is preserved as the
scale-up option in `infra/practice-cluster/t2-nodepool-kata/`.

---

## 1. Why Kata-on-metal cannot fit ₹100/user

`memory.md` §5.1 gives T2 a cost band of `$0.10–0.35/env-hr`. That figure
assumes a **large, well-packed, shared Kata pool at scale** (§10.5:
10,000 learners, ~120 concurrent T2, metal nodes always busy). At a
launch-cohort scale it does not hold:

| Cost component | Reality at <200 users |
|---|---|
| EKS control plane | **$73/mo fixed**, before any learner |
| NAT gateway + EIP | **$37/mo fixed** |
| `c7g.metal` (smallest ARM metal) | **$2.90/hr on-demand, ~$1.00/hr spot** — no smaller size; you rent all 64 vCPU |
| Bin-packing at low volume | a 50-user cohort often has **1 learner on a 64-vCPU node** → effective **$1–2.90/env-hr**, ~10× the reference |

**Fixed cost alone** (EKS + NAT + EBS ≈ $118/mo ≈ ₹9,800/mo), amortised:

| Cohort | Fixed ₹/user/mo |
|---|---|
| 50 | ₹196 |
| 100 | ₹98 |
| 200 | ₹49 |

Below ~200 users, a dedicated AWS EKS + metal setup is **2× over budget
on fixed infrastructure cost alone** — before Sysbox, before T2, before
compute or AI. Kata-on-metal is a scale-economics feature; it is not
viable at launch scale.

---

## 2. The Sysbox architecture

**One shared cluster. No separate T2 pool. No microVM.**

A "T2" environment is a **Sysbox-isolated pod** on the same nodes that
run T1 — a bigger pod that requests more CPU/RAM for ~45 min, then
releases it. Sysbox provides the isolation and the capabilities:

| Capability | Sysbox delivery |
|---|---|
| Docker-in-Docker | ✅ real, **unprivileged** — `dockerd` runs inside the pod |
| systemd as PID 1 | ✅ native (Sysbox was built for this) |
| Nested multi-node k3s | ✅ k3d/kind inside the pod, multiple nodes |
| eBPF | ✅ most programs (kprobes, tracepoints, `bpftrace`); LSM-BPF / some XDP need `PrivilegedWorkload` (still no VM) |
| Kernel modules / custom kernel | ❌ — shared host kernel. Your 5 T2-gated faults (Istio, ArgoCD, IAM) don't need this. |

Isolation: **Linux user-namespace remapping** — the pod's `uid 0` is a
non-root host uid. Stronger than a `privileged` container (which the
original T2 design already sanctioned — `memory.md` §1727 "no privileged
containers **outside T2**"); weaker than a VM (a host-kernel 0-day is a
cross-tenant risk).

### Code

`orchestrator/internal/k8s/provision.go`:
- `ProvisionerConfig.T2RuntimeClass` — `ORCHESTRATOR_T2_RUNTIME_CLASS`,
  **default `"sysbox-runc"`**. Set to `"kata"` + the metal pool for the
  scale-up path.
- `applyT2PodShape` (Sysbox shape): sets that RuntimeClass, **no**
  `tier2` nodeSelector/toleration, shell container runs `RunAsUser: 0`
  (root-in-userns), **not privileged** unless
  `ProvisionRequest.PrivilegedWorkload` (eBPF blueprints).
- T2 namespace stays PSS `privileged` (`pssLevelFor`) — required so
  admission permits `RunAsUser: 0`.
- `ttl.EnvironmentDefaultT2 = 45m` still applies (caps a walked-away env).

### Infra

- `infra/practice-cluster/cluster/` — the practice EKS cluster (VPC,
  single NAT, ARM Graviton **spot** T1 node group). *(For the cheapest
  option, run k3s on one ARM VM instead — `infra/practice-cluster/bootstrap/k3s-sysbox-node.sh`.)*
- `infra/practice-cluster/sysbox/` — `sysbox-runc` RuntimeClass +
  `sysbox-deploy-k8s` DaemonSet (multi-node), node-selected to learner
  nodes only.
- `infra/practice-cluster/bootstrap/k3s-sysbox-node.sh` — single-VM k3s +
  Sysbox, one script, with a built-in DinD/systemd smoke test.

---

## 3. Cost model — Sysbox, all-in, per user per month

Assumptions (grounded — see `t2-cost-optimization.md` §"Assumptions"):
6 attempts/user/mo, 80% T1-class / 20% T2-class, T2 env 4 vCPU / 8 GiB
(optimized, not the 8/16 ceiling), 45-min max TTL / ~40-min typical,
65% packing, AI $0.01/attempt (static hints, no mentor in Phase 2).

### Fixed cost

| Host option | $/mo fixed | ₹/mo fixed |
|---|---|---|
| **Oracle Cloud Always Free** (Ampere ARM, 4 OCPU / 24 GB, Mumbai) | **$0** | **₹0** — free forever; ~3–5 concurrent envs, good for a pilot / requirement-A verification |
| **Single paid ARM VM + k3s** (Hetzner CAX41-class, 16 vCPU / 32 GiB, 2 TB egress incl.) | ~$40–53 | ~₹3,500–4,400 |
| **AWS EKS** (single-AZ, one NAT, from `cluster/main.tf`) | ~$118–123 | ~₹9,800–10,200 |

### Variable cost (per user, their 6 attempts)

| Host option | typical ₹/user/mo | worst-case ₹/user/mo |
|---|---|---|
| **Single ARM VM** | ~₹0 marginal until the box saturates (~4–5 concurrent envs), then step-cost a 2nd VM | ~₹5 |
| **AWS EKS** (`m7g` spot ~$0.021/vCPU-hr) | ~₹33 | ~₹60 |

### Total — does it fit ₹100 all-in?

| Cohort | Single VM + k3s | AWS EKS |
|---|---|---|
| 30 users | ₹152 ❌ (fixed dominates) | ₹390 ❌ |
| **50 users** | **₹93 ✅** | ₹264 ❌ |
| **100 users** | **₹52 ✅** | ₹162 ❌ |
| 200 users | ₹34 ✅ | ₹111 ❌ (barely) |
| 300 users | ₹28 ✅ | ₹94 ✅ |
| 500 users | ₹30 ✅ | ₹53 ✅ |

**Conclusion: at <~200 users, "single ARM VM + k3s + Sysbox" is the only
architecture that fits ₹100/user all-in.** Max realistic cost is
**~₹90–95/user/mo at a 50-user cohort**, dropping fast with growth. AWS
EKS + Sysbox only fits from ~300 users; Kata metal never does at this
stage.

### Provider comparison for the paid ARM-VM step (decide at pilot exit)

Sysbox needs a real Linux VM you have root on — this rules out all
serverless / container PaaS (Fly, Railway, Render, Cloud Run, App Runner,
Lambda). The comparison is bare/virtual Linux servers, cheapest first:

| Provider | Shape | ~₹/mo | India latency | Notes |
|---|---|---|---|---|
| **Oracle Cloud Always Free** | 4 OCPU / 24 GB ARM, Mumbai | **₹0** | ~5–30 ms | **Pilot host.** Capped at 4 OCPU; capacity-retry pain. |
| **Contabo** | 6 vCPU / 16 GB (shared), Singapore | ~₹1,000 | ~60–90 ms | Cheapest paid. Noisy-neighbour risk, weak support. |
| **Netcup** | 8 vCPU / 16 GB ARM, DE/AT | ~₹1,000 | ~130 ms | Hetzner-tier price, EU only. |
| **E2E Networks** | 4 vCPU / 8 GB ARM, Mumbai/Delhi/Chennai | ~₹1,500–3,000 | **<10 ms** | Indian provider + data residency; less battle-tested tooling. |
| **OVHcloud** | 4 vCPU / 8 GB, **Mumbai DC** | ~₹1,600 | ~10–30 ms | Only mainstream cheap host with an India DC. |
| **Hetzner** | CAX41: 16 vCPU / 32 GB ARM, 20 TB egress | **~₹2,700** | ~120–150 ms | **Best ₹/vCPU anywhere.** Egress effectively free. EU only. 130 ms is fine for a WebSocket terminal (reconnect buffer + telemetry tap absorb jitter, `memory.md` §5.4). |
| Vultr / Linode | 4 vCPU / 8 GB, Mumbai | ~₹3,000–3,500 | ~10–30 ms | Reliable + India DC, 2–3× Hetzner per core. |
| AWS Lightsail | 4 vCPU / 8 GB, Mumbai | ~₹3,300 | ~5 ms | Flat-rate, AWS-native (handy if Phase 3 T3 vending is AWS). |

**Recommendation for the long run:** Oracle Free now → **Hetzner** for
production (lowest cost, protects the margin) → switch to **OVHcloud
Mumbai** or **E2E Networks** only if a real pilot shows learners
complaining about typing latency. Migration between any of these is: run
`bootstrap/k3s-sysbox-node.sh` on the new box, re-deploy, repoint DNS —
about a day, not a rebuild. The bootstrap script and all manifests are
provider-agnostic.

---

## 4. Sysbox vs Kata — performance & capability

| Dimension | **Sysbox (shared T1 pool)** | **Kata/Firecracker (dedicated metal)** |
|---|---|---|
| Isolation boundary | Linux user namespaces + procfs/sysfs virt + syscall trap. Shared host kernel. | Hardware VM. Own guest kernel per pod. |
| Isolation strength | Strong for a container (~gVisor-class). Host-kernel 0-day = cross-tenant risk. | Strongest available. Kernel exploit stays in the guest. |
| DinD | ✅ real, **unprivileged** | ✅ real, in the VM |
| systemd as PID 1 | ✅ native | ✅ native |
| Nested multi-node k3s | ✅ via k3d/kind | ✅ |
| eBPF | ⚠️ most; LSM-BPF/some XDP need `PrivilegedWorkload` | ✅ full |
| Kernel modules / custom kernel | ❌ | ✅ (guest) |
| `sysctl` tuning | ⚠️ namespaced only | ✅ any (guest) |
| Cold start | **8–15 s** (regular pod) | 60–90 s cold / 8–20 s warm |
| CPU overhead | ~2–5% | ~8–15% |
| Memory overhead / env | ~30 MiB | ~120–150 MiB |
| Density (64-vCPU node) | ~10–16 | ~3–6 |
| Node type | any (shares T1 nodes) | **bare-metal only** (`*.metal`, KVM) |
| Marginal infra cost of the capability | **~₹0** | $73 EKS + $37 NAT + metal $/hr, all fixed |
| $/env-hr at small cohort | ~$0.03–0.06 (= T1) | ~$1–2.90 (poor packing) |
| I/O / network perf | near-native | virtio overhead |
| Ops surface for you | one runtime, one pool, one autoscaler | second pool, kata-deploy lifecycle, guest-image upkeep, metal capacity planning |

**Verdict for Phase 2 content:** the 5 T2-gated faults are Istio mesh
config, VirtualService weights, ArgoCD drift, IAM role, ECR pull —
**every one is application/control-plane layer**. None need a guest
kernel, kernel modules, or LSM-BPF. Sysbox covers 100% of what your
Phase 2 content exercises, with faster cold starts and ~₹0 marginal
cost. It is the correct engineering choice at this budget and scale, not
a downgrade.

Switch to Kata metal (`../t2-nodepool-kata/`) when concurrent T2 volume
exceeds ~100–150 (metal packs well) **and** you author content that
genuinely needs a guest kernel.

---

## 5. Cost guardrails (apply these regardless of host)

1. **Right-size the T2 request.** `DefaultT2Resources` (8/16) is the
   LimitRange *max*, not a default. Set `resources: {cpu: "4", memory:
   "8Gi"}` on sim blueprints that don't need the ceiling → ~2× density.
2. **Aggressive idle-kill.** Set `idle_timeout_minutes: 7` on T2 sim
   blueprints (`IdleTimeoutDefault` is 15).
3. **45-min T2 TTL** — already the code default (`EnvironmentDefaultT2`).
4. **No T2 warm pool.** `server.go` never claims warm for T2; keep it
   that way — a warm-pool floor is pure idle burn at any tier.
5. **Autoscale the T1 pool to `min=0` off-peak** (`cluster/main.tf`
   `t1_min_size = 0`); a small always-on floor only during known cohort
   hours.
6. **CI guard on `min_tier: T2`** — an activity may declare it only if
   its blueprint declares a real T2 capability (`docker.privileged`,
   `k8s.k3s`, `k8s.multi_node`, `systemd`, `ebpf`). Keeps T2 at ~20% of
   attempts (`memory.md` §10.2 "tier down — largest single lever").
7. **Per-cohort `budget` row** sized from §3, plus a cloud-native budget
   alarm as the out-of-band tripwire (`memory.md` §10.4).
