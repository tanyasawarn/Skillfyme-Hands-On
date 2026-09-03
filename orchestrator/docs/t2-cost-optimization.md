# T2 Cost Optimization — the ₹300/user ceiling (SUPERSEDED)

> **Superseded by `t2-cost-optimization-100.md`.** The budget was
> tightened to ₹100/user all-in, and the decision changed: T2 no longer
> runs on a dedicated Kata bare-metal pool — it runs under **Sysbox** on
> the shared T1 pool. This document is kept for the ₹300-era reasoning
> and the Firecracker/spot-metal cost levers (still valid *if* you ever
> return to a Kata pool at scale). For the current architecture, cost
> model, and Sysbox-vs-Kata comparison, read `t2-cost-optimization-100.md`.

**Constraint (historical):** total cost per user under **₹300/user/month**
(≈ **$3.60**/user/month at ₹83/$). This document works out whether T2 fits,
and the specific changes that keep it there.

**Grounding:** `memory.md` §10.1–10.5 (cost model, cost-control stack,
capacity model), §5.1 (tier cost bands), §5.5–5.6 (warm pools, idle/TTL),
`internal/ttl/ttl.go`, `internal/k8s/provision.go` (`DefaultT2Resources`),
`internal/idledetect`, `internal/costmeter`.

---

## 1. Does T2 even fit? — the arithmetic

`memory.md`'s cost model is **per user per month**:

```
cost/user/month = Σ over the user's attempts (env_compute + storage + egress
                  + control-plane share + validation + AI tokens + snapshots)
```

Per `memory.md` §10.5 the reference mix is **6 attempts/user/month**, of which
**20% run on T2** → **1.2 T2 attempts/user/month**. Per §1815 a T2 sim
(60 min) costs **~$0.22**.

| Line | Value |
|---|---|
| T2 attempts / user / month | 1.2 |
| Cost per T2 attempt (reference, 60 min) | $0.22 |
| **T2 spend / user / month** | **$0.26 ≈ ₹22** |
| T1 spend / user / month (4.8 attempts × ~$0.04) | $0.19 ≈ ₹16 |
| AI / validation / storage (rough, §1815) | ~$0.30 ≈ ₹25 |
| **Total variable cost / user / month** | **~₹63** |

**T2's incremental cost is ~₹22/user/month — it fits ₹300 with large
headroom.** The ₹300 ceiling is *not* threatened by T2 usage. It is
threatened by **three failure modes** that turn $0.22 into $2+:

| Failure mode | Effect | Fix |
|---|---|---|
| **F1 — idle node burn.** A dedicated `*.metal` node left running off-peak at ~$1–5/hr. | Fixed cost independent of users. 1 idle metal node = **₹60,000–300,000/month** — wipes the budget for a 1,000-user cohort. | Autoscaler `min=0`, scale-down delay ≤ 5 min, `min=0` verified in the runbook (§4). |
| **F2 — long-lived T2 envs.** Default `EnvironmentDefault = 90 min` TTL. At $0.22/hr that's **$0.33/attempt**, and a learner who walks away burns the full 90 min. | 50% cost overrun per attempt, silently. | **T2-specific 45-min TTL** + aggressive idle-kill (§3). |
| **F3 — misrouting.** Content authors put `min_tier: T2_ISOLATED_MICROVM` on activities that only need T1. Every such attempt costs **5×**. | If T2 share drifts from 20% → 40%, T2 spend doubles to ₹44/user. | CI guard on `min_tier` (§5) + tier-down review in content CI. |

Everything below is about closing F1–F3, not about shaving the $0.22.

---

## 2. Compute: the cheapest way to run a Kata/Firecracker microVM

`memory.md` §5.1 gives T2 a band of **$0.10–0.35/env-hr**. To land at the
**bottom** of that band:

### 2.1 Use Firecracker, not full QEMU-Kata

`memory.md` §5.1/§5.3 name the tier "**Firecracker/Kata microVM**" and §2223
"**Kata/Firecracker (T2)**" — Firecracker is explicitly in scope. Firecracker
(via `kata-containers` with the `firecracker` hypervisor, or `firecracker-containerd`)
has:
- **~125 ms boot**, ~5 MiB memory overhead per microVM (vs. QEMU-Kata's
  ~150 MiB and multi-second boot);
- much higher **density per node** → lower $/env-hr.

Cost impact: Firecracker's low per-VM overhead is what lets you pack
**4–5 envs on a 32-vCPU node** instead of 3, dropping $/env-hr ~20%.

**Trade-off (be honest):** Firecracker's device model is minimal — no GPU, no
arbitrary PCI, limited to virtio. For T2's workloads (DinD, nested k3s,
systemd, eBPF) that is fine. If a future T2 activity needs something
Firecracker can't model, that activity uses the QEMU-Kata runtime class
instead (`RuntimeClassName: kata-qemu`) — but nothing in the current 3
T2-gated faults needs it.

### 2.2 Spot metal + graceful drain

`memory.md` §10.2 "Spot / ARM: 40–70% on compute" and §2235. AWS `*.metal`
spot is **60–70% cheaper** than on-demand. T2 pods already snapshot-and-destroy
on idle; extend the same path to **spot preemption notice** (2-min warning) —
`memory.md` §10.2 "graceful drain (snapshot on preemption notice)". A
preempted T2 attempt is snapshotted and the learner resumes on a fresh node.

Cost impact: `*.metal` spot at ~$0.35/vCPU-hr on-demand → ~$0.12 spot.
A 4-env node at ~$0.12 → **~$0.03/env-vCPU-hr**, i.e. an 8-vCPU env ≈
**$0.24/hr on-demand, ~$0.09/hr spot**. Spot lands at the bottom of the band.

### 2.3 ARM where the workload allows

`memory.md` §5.2: T1 runs "ARM64 where possible". For T2, Firecracker runs on
Graviton metal (`c7g.metal`, `m7g.metal`). DinD/k3s/systemd all have ARM
images. eBPF is arch-neutral. Graviton metal spot is the cheapest KVM-capable
compute AWS sells. Default the T2 pool to ARM; keep a small x86 `*.metal` pool
only if a specific activity pins an amd64-only image.

### 2.4 Right-size the per-env request — 8/16 is a ceiling, not a default

`DefaultT2Resources = {CPU: "8", Memory: "16Gi"}` is the LimitRange **max**.
Most T2 sims (single nested k3s, one DinD build) run comfortably in **4 vCPU /
8 GiB**. Set the blueprint's `resources` explicitly per activity:

| T2 activity shape | Suggested request | Envs / 32-vCPU node |
|---|---|---|
| Single nested k3s + 1–2 workloads (most sims) | **4 vCPU / 8 GiB** | 6 |
| Multi-node nested k3s / heavy DinD | 8 vCPU / 16 GiB | 3 |

Cost impact: halving the request **doubles density** → ~halves $/env-hr for
those activities. This is the single biggest lever after `min=0`.

**Change needed:** author `resources: {cpu: "4", memory: "8Gi"}` in the sim
activity YAMLs that don't need the full ceiling, and thread
`ProvisionRequest.Resources` through (already supported —
`applyT2PodShape` uses `req.Resources` over the default).

---

## 3. Lifetime: cap what a single T2 attempt can cost

### 3.1 T2-specific TTL (change to code)

`ttl.EnvironmentDefault = 90 min` is right for a T1 guided lab. For a T2 sim
it is 2× too long. Add a tier-aware default:

```go
// internal/ttl/ttl.go
const (
    EnvironmentDefault   = 90 * time.Minute // T1 guided labs
    EnvironmentDefaultT2 = 45 * time.Minute // T2 sims — at $0.22/env-hr, cap the tail
)
```

Wire it in `server.go`'s `Provision`: when `tier == TierT2IsolatedMicroVM`
and the request carries no explicit `TtlMinutes`, use `EnvironmentDefaultT2`.
A T2 sim is scoped to ~60 min of work; 45 min forces tight authoring and a
15-min buffer via the "long-running-op suppression" already in idledetect.

Cost impact: worst-case attempt cost $0.33 → **$0.165**. Halves the tail.

### 3.2 Aggressive idle-kill for T2 (config)

`IdleTimeoutDefault = 15 min` — for T2 set the per-activity
`environment.idle_timeout_minutes` to **7 min** in the sim blueprints.
`memory.md` §10.2: "Idle kill … aggressive defaults … 30–50% of env-hours".
At T2 prices, a 7-min idle timeout vs. 15-min saves ~$0.03/attempt on the
median walk-away.

### 3.3 No T2 warm pool (keep it cold)

`server.go` already only claims warm for `TierT1SharedContainer`. **Keep it
that way.** `memory.md` §5.5: warm pools "cost money for idle capacity". At
T2's band, a warm-pool floor of even 2 envs = 2 × $0.22 × 24 × 30 ≈
**$317/month** of pure idle burn — more than the entire T2 variable cost of a
1,000-user cohort. Accept the 60–90 s cold start (`memory.md` §5.1) for T2;
show it honestly in the UI (§1715 "always show … cost-relevant state").

**Exception:** a scheduled live class. Pre-warm *just for that window* by
raising the autoscaler `min` 15 min before (`memory.md` §5.5), then back to
`min=0`. This is a scheduled scale action, not a standing warm pool.

---

## 4. Fixed infra: the autoscaler config that makes or breaks the budget

```hcl
# infra/practice-cluster/t2-nodepool/  (to be built)
node_group "practice-t2" {
  instance_types = ["c7g.metal", "m7g.metal"]   # ARM Graviton metal
  capacity_type  = "SPOT"                         # 60-70% off; graceful drain on 2-min notice
  scaling { min_size = 0, max_size = <from capacity model>, desired = 0 }
  taints  = [{ key = "workload", value = "learner-t2", effect = "NO_SCHEDULE" }]
  labels  = { "practiceengine.dev/tier2" = "true" }
}

# cluster-autoscaler, THIS node group only:
scale_down_enabled             = true
scale_down_unneeded_time       = "3m"    # metal is expensive — reclaim fast
scale_down_delay_after_add     = "3m"
scale_down_utilization_threshold = 0.5
expander                       = "least-waste"
```

**The rule:** off-peak, `kubectl get nodes -l practiceengine.dev/tier2=true`
returns **nothing**. If it ever shows a node with zero T2 pods for >5 min, the
autoscaler is misconfigured and every hour costs ₹100–400. The runbook's
teardown check (`t2-setup-and-operations.md` §5.4) verifies this.

---

## 5. Routing: keep T2 at ~20% of attempts (CI guard)

`memory.md` §10.2: "Tier down — Route to the cheapest tier that satisfies
capabilities … **largest single lever — 10–100×**". Concretely:

1. **CI check** on `content/`: an activity may set
   `min_tier: T2_ISOLATED_MICROVM` **only if** its blueprint declares one of
   the genuinely-T2 capabilities (`docker.privileged`, `k8s.k3s`,
   `k8s.multi_node`, `kernel.tune`, `systemd`, `ebpf`). Any other activity
   claiming T2 fails CI with "tier over-specified — this needs T1".
   Add to `scripts/lint-content.ts` / `evaluation/content-ci`.
2. **Dashboard**: `usage_meter` already tags `tier`. Add a panel:
   `sum(rate(attempts[7d])) by (tier)`. Alert if T2 share > 25%.
3. **Author guidance**: document that T2 is for *simulations that need real
   kernel features*, not "sims in general". Most incident sims (config
   errors, bad selectors, probe tuning) are **T1** — see the 20 T1 faults vs.
   3 T2 faults split that already exists in `content/faults/`.

---

## 6. Storage & egress (small, but free wins)

| Lever | Action | Source |
|---|---|---|
| Snapshots | zstd compress, S3 IA, delete intermediates at 7 days | `memory.md` §10.2 storage lifecycle |
| Egress | single-AZ T2 environments; registry pull-through cache on the pool | `memory.md` §10.2, §2235 |
| Image pull | pre-pull the T2 base image + kata rootfs via DaemonSet on the pool | `memory.md` §5.2 |
| Control-plane share | T2 namespaces stay lean (no per-ns CRDs — nested k3s runs *inside* the pod, not as cluster CRDs) | `memory.md` §5.2 "keep namespaces lean" |

---

## 7. Revised cost model with all levers applied

| Line | Reference (§1815) | Optimized |
|---|---|---|
| T2 node compute | on-demand metal, 3 envs/node | **ARM spot metal, 4–6 envs/node** |
| $/env-hr | $0.22 | **$0.08–0.12** |
| Attempt length cap (TTL) | 90 min | **45 min** |
| Worst-case $/attempt | $0.33 | **$0.09** |
| Typical $/attempt (median ~35 min active) | $0.13 | **$0.05–0.07** |
| T2 attempts/user/month | 1.2 | 1.2 |
| **T2 $/user/month** | $0.26 (₹22) | **$0.07 (₹6)** |
| Idle node burn | risk: ₹60k+/mo if misconfigured | **~₹0** (min=0, 3-min scale-down) |

**Optimized total variable cost/user/month ≈ ₹40–50**, of which T2 is ~₹6.
Comfortably inside ₹300, leaving ~₹250 for subscription margin + AI mentor
spend in later phases.

---

## 8. Checklist — what to actually change

**Code (small):**
- [ ] `internal/ttl/ttl.go`: add `EnvironmentDefaultT2 = 45 * time.Minute`.
- [ ] `internal/orchestrator/server.go`: in `Provision`, default T2 TTL to
      `EnvironmentDefaultT2` when `req.TtlMinutes == 0` and tier is T2.
- [ ] (optional) metric `orchestrator_env_hours_total{tier}` if not already
      emitted, so the T2-share alert in §5.2 has a series.

**Infra (you provision):**
- [ ] `infra/practice-cluster/t2-nodepool/` — ARM Graviton `*.metal`,
      `capacity_type = SPOT`, `min_size = 0`, autoscaler `scale_down_unneeded_time = 3m`.
- [ ] kata-deploy with the **Firecracker** hypervisor (`configuration-fc`),
      not the default QEMU config.
- [ ] Spot-preemption handler: on the 2-min notice, call `Destroy` with a
      snapshot reason (reuses the idle snapshot-and-destroy path).
- [ ] Pre-pull DaemonSet for the T2 base image + Firecracker rootfs, pool-scoped.

**Content (Step F):**
- [ ] Set explicit `resources: {cpu: "4", memory: "8Gi"}` on T2 sim
      activities that don't need the 8/16 ceiling.
- [ ] Set `idle_timeout_minutes: 7` on T2 sim blueprints.
- [ ] CI guard: `min_tier: T2` requires a real T2 capability in the blueprint.

**Ops:**
- [ ] Per-cohort `budget` row sized from §7 (≈ ₹6/user × cohort × 1.5 safety).
- [ ] AWS Budgets alarm on the practice cluster account as the out-of-band
      tripwire (`memory.md` §10.4).
- [ ] Off-peak node-count check in the runbook: `tier2=true` nodes → 0.
