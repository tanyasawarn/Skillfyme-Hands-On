# T2 (Isolated microVM) — Setup & Operations Guide

> **SCALE-UP REFERENCE, not the current setup.** As of the ₹100/user
> decision, T2 runs under **Sysbox** on the shared T1 pool, not on a
> dedicated Kata bare-metal pool. The current setup path is:
> - `orchestrator/docs/t2-cost-optimization-100.md` — the decision + cost model
> - `infra/practice-cluster/PROVISION-RUNBOOK.md` — the step-by-step
> - `infra/practice-cluster/sysbox/` + `bootstrap/k3s-sysbox-node.sh`
>
> This document describes the **Kata microVM** path, kept as the
> reference for when concurrent T2 volume makes a dedicated metal pool
> economical (~150+ concurrent T2 envs) or content needs a guest kernel.
> The capacity/cost §4 and the runbook §5 below are Kata-specific; the
> tier concepts, security model, and sequencing gate still apply to both.

**Audience:** the operator standing up the T2 tier for a real learner cohort.
**Status of the code:** T2 is fully implemented in the orchestrator. The
default runtime is `sysbox-runc` (`ORCHESTRATOR_T2_RUNTIME_CLASS`); set it
to `kata` plus the pool below for this path.

**Companion artifacts (Phase 2 requirement A):**
- `orchestrator/docs/t2-cost-optimization.md` — the ₹300/user cost model, the
  Firecracker/spot-metal/right-sizing levers, and the revised numbers.
- `infra/practice-cluster/t2-nodepool/` — the OpenTofu module that builds the
  pool (ARM Graviton bare-metal SPOT, taint/label pair, `min=0` autoscaler,
  kata-deploy with the Firecracker shim, spot-interruption drain). `tofu
  validate` + `tofu fmt` clean; apply needs the practice EKS cluster.
- `scripts/t2-lifecycle-check.sh` — the end-to-end harness: real Provision →
  Connect → verify DinD/k3s/systemd/eBPF → Destroy → zero-leftover. Fails
  loudly on any stub/skip/leftover.
- `orchestrator/internal/k8s/provision_t2_live_test.go` —
  `TestProvisionT2_Lifecycle`, which auto-detects whether a Kata node exists
  and either asserts the honest "no kata node" failure or the full positive
  lifecycle (pod on tier2 node, PSS privileged, quota = `DefaultT2Resources`,
  clean Destroy).

**Sources this guide is derived from (not invented):**
- `PLAN.md` Phase 2 — "T2 microVM tier: Firecracker/Kata driver, DinD, k3s
  multi-node, systemd/eBPF support" + the zero-orphan sequencing gate.
- `memory.md` §5.1 (tier table), §5.2 (T1 node-pool shape, the pattern T2
  mirrors), §5.5 (provisioning pipeline — `T2: request microVM from node pool`),
  §10.5 (capacity model: "~120 concurrent T2", "20% T2 (1.0 env-hr)"),
  §1815 (cost: "T2 k8s sim (60 min) ~ $0.22").
- The actual code: `internal/k8s/provision.go` (`applyT2PodShape`,
  `resourceQuotaFor`, `limitRangeMaxFor`, `pssLevelFor`, `DefaultT2Resources`),
  `internal/orchestrator/server.go` (`resolveTier`, `t2Enabled`),
  `internal/config/config.go` (`T2Enabled`),
  `manifests/t2/runtimeclass-kata.yaml`, `manifests/t2/node-pool-taint.md`.

---

## 0. What a learner actually gets on T2

A learner who launches a **Production Sim** activity whose blueprint declares
`min_tier: T2_ISOLATED_MICROVM` (or any capability T1 cannot provide) is routed
to T2 by `resolveTier()` at the `Provision` RPC. Their environment is:

| Property | Value | Set by |
|---|---|---|
| Namespace | `env-<env_id>` (same convention as T1) | `createNamespace` |
| Pod runtime | **Kata Containers** microVM (own kernel) via `RuntimeClassName: kata` | `applyT2PodShape` |
| Node placement | `nodeSelector: practiceengine.dev/tier2=true` + toleration `workload=learner-t2:NoSchedule` | `applyT2PodShape` |
| Privilege | `securityContext.privileged: true` on the shell container | `applyT2PodShape` |
| PodSecurity level | `privileged` (T1 is `restricted`) | `pssLevelFor` |
| ResourceQuota (per env) | **8 vCPU / 16 GiB / 20 pods / 4 PVC / 4 svc** | `resourceQuotaFor` |
| LimitRange max (per container) | **8 vCPU / 16 GiB** | `limitRangeMaxFor` |
| Default shell container limit | **8 vCPU / 16 GiB** (`DefaultT2Resources`) | `applyT2PodShape` |
| ServiceAccount token | **not mounted** (`automountServiceAccountToken: false`) — same as T1 | `applyServiceAccount` |
| NetworkPolicy | default-deny ingress+egress, egress-proxy allow only — same as T1 | `applyNetworkPolicy` |
| Connection | K8s `exec` API proxied by the Session Broker over WebSocket (never SSH) | Session Broker |

Inside that microVM the learner can run **Docker-in-Docker, a nested multi-node
k3s cluster, systemd, and eBPF programs** — the four capabilities Phase 2
promises and T1 structurally cannot give (T1 is gVisor, shared kernel, no
privileged containers). The compensating security control for `privileged:
true` is **Kata's hardware virtualisation**: an escape from the privileged
container lands inside the per-pod microVM, not on the shared host kernel
(`manifests/t2/node-pool-taint.md` "Threat model" section).

---

## 1. Prerequisites — the sequencing gate (do not skip)

`PLAN.md` Phase 2 dependency note and `config.go`'s `T2Enabled` comment both
state the same hard rule:

> Dev A should not start T2 until Phase 1's reaper/teardown has been running
> with **zero orphans for a sustained period**.

**Concretely, before you set `ORCHESTRATOR_T2_ENABLED=true`:**

1. Run `evaluation/phase1/load/check-orphans.sh 3600` against the T1
   deployment **immediately after** a load run, and **again ≥ 1 h later**.
   Both must print `PASS` (`increase(orchestrator_reaper_orphans_found_total[1h]) == 0`).
2. Confirm `orchestrator_reaper_orphans_found_total` has been flat at its
   starting value for the sustained window (the script does this) **and** that
   `kubectl get ns | grep '^env-'` returns nothing after a cohort's activity
   has drained.
3. Record both runs in `evaluation/phase1/results/`.

If the T1 reaper is still occasionally leaking namespaces, T2 will leak
**microVMs** — which cost 5–10× more per hour — so this gate is not a
formality.

---

## 2. Infrastructure plan — the T2 node pool

T2 needs a **second, dedicated node pool** on the practice cluster. It cannot
share the T1 pool: T1 nodes are cheap spot/ARM64 instances with no hardware
virtualisation exposed; Kata's microVMs need KVM (`memory.md` §5.2
"Alternatives considered: Kata Containers … chosen for T2", §5.1 isolation
column "Hardware-virtualised; own kernel").

### 2.1 Node pool shape

| Parameter | Value | Basis |
|---|---|---|
| Pool name | `practice-t2` | `manifests/t2/node-pool-taint.md` |
| Instance requirement | **Bare-metal or nested-virt-capable**, KVM exposed | Kata requirement |
| — AWS | `*.metal` (e.g. `m6i.metal`, `c6i.metal`) or `i3.metal` | KVM only on metal / a few nitro types |
| — GCP | any N2/C2 with **nested virtualization license** enabled on the image | GCP nested-virt |
| — Azure | Dv3/Ev3+ with nested virtualization | Azure nested-virt |
| Node size (target) | **≥ 32 vCPU / 64 GiB** | `node-pool-taint.md` capacity model — headroom for ~3 concurrent 8-vCPU envs + kata-agent/kubelet overhead |
| Taint | `workload=learner-t2:NoSchedule` | matches `applyT2PodShape`'s toleration |
| Label | `practiceengine.dev/tier2=true` | matches `applyT2PodShape`'s `nodeSelector` |
| Autoscaling | cluster-autoscaler **on this pool only**, `min=0`, `max` sized to peak T2 concurrency (see §4) | `node-pool-taint.md` — T2's $/hr makes idle capacity expensive; min=0 is correct for bursty demand |
| Envs per node (planning target) | **3** | `floor(32 / 8) − 1` reserved for system + per-microVM overhead; deliberately under-packs vs. naive division |

### 2.2 Kata installation on the pool

`kubectl apply -f manifests/t2/runtimeclass-kata.yaml` creates the
`RuntimeClass` object — **inert on its own**. The node side is:

1. Deploy **kata-deploy** (the upstream DaemonSet) targeted at the
   `practice-t2` pool only:
   ```
   kubectl apply -k "github.com/kata-containers/kata-containers/tools/packaging/kata-deploy/kata-deploy/overlays/k3s?ref=<pin-a-release>"
   ```
   Add a nodeSelector to the DaemonSet so it only lands on
   `practiceengine.dev/tier2=true` nodes — you do **not** want the kata shim
   installed on T1 spot nodes.
2. kata-deploy installs the `kata` containerd runtime handler and (on k3s)
   patches `/var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl`.
3. Verify: `kubectl get runtimeclass kata` and, on a T2 node,
   `containerd config dump | grep -A3 'runtimes.kata'`.

### 2.3 Platform-namespace isolation

Session Broker, egress proxy, reaper, and every other
`practiceengine-platform` pod **must not** carry a toleration for
`workload=learner-t2:NoSchedule` — they stay on general-purpose nodes, never
co-scheduled with the most-privileged learner workloads
(`node-pool-taint.md` "Isolation boundary").

### 2.4 The `infra/` gap (build this)

Today there is **no `infra/` Terraform for the T2 node pool** — `infra/` has
`aws-org/`, `account-baseline/`, `budget/`, etc. for Phase 3 T3, but nothing
for the practice cluster's node pools. **Add `infra/practice-cluster/t2-nodepool/`**
containing:
- the `practice-t2` node group / instance group definition for your cloud,
  with the taint, label, instance type, and `min=0` autoscaling from §2.1;
- the kata-deploy manifest (or a Helm values file for it) pinned to a release;
- an output for the pool's max size so the capacity model in §4 and the
  Terraform stay in sync (same "numbers match implementation" rule
  `node-pool-taint.md` already applies to the quota values).

---

## 3. Turning T2 on

1. Land the node pool (§2) and confirm `kubectl get nodes -l practiceengine.dev/tier2=true`
   shows Ready nodes with the `kata` runtime.
2. Confirm the sequencing gate (§1).
3. Set on the orchestrator deployment:
   ```
   ORCHESTRATOR_T2_ENABLED=true
   ```
   (`config.go` → `T2Enabled` → `NewServer(..., cfg.T2Enabled)` →
   `resolveTier`). No redeploy of anything else is needed.
4. Smoke-test with the runbook in §5 **before** any learner traffic.

`resolveTier` fails **closed**: with `T2Enabled=false`, a
`TIER_T2_ISOLATED_MICROVM` request returns `FailedPrecondition` and never
silently downgrades to T1 (`server_test.go`
`TestResolveTier_T2NeverSilentlyDowngradesToT1`).

---

## 4. Capacity plan

All numbers below trace to `memory.md` §10.5 (capacity model) and §1815
(cost), and to `provision.go`'s actual T2 quota values. **They must stay
matched to the code** — if `DefaultT2Resources` or `resourceQuotaFor` change,
update this section (there is a test, `TestApplyResourceQuota_T2HasHigherCeilingsThanT1`,
that pins the *relationship*; this doc pins the *absolute* numbers).

### 4.1 Per-environment footprint

| Resource | Request/quota | Source |
|---|---|---|
| CPU | 8 vCPU | `resourceQuotaFor(TierT2)` → `DefaultT2Resources.CPU` |
| Memory | 16 GiB | `resourceQuotaFor(TierT2)` → `DefaultT2Resources.Memory` |
| Pods | 20 | `resourceQuotaFor` — fits a nested k3s control plane + workers |
| PVCs | 4 | `resourceQuotaFor` |
| Services | 4 | `resourceQuotaFor` |

### 4.2 Node math

- Node: 32 vCPU / 64 GiB (§2.1 minimum).
- Usable after system reserve (kubelet, kata-agent per-VM overhead, OS): ~26 vCPU / ~52 GiB.
- **Envs per node = 3** (`floor(26 / 8) = 3`, and 3 × 16 GiB = 48 GiB < 52 GiB). Matches `node-pool-taint.md`.

### 4.3 Pool sizing against a cohort

`memory.md` §10.5 model, scaled to **your** cohort size `N` (monthly active learners):

```
attempts/month          = N × 6
T2 share                 = 20%                → 1.2N T2 attempts/month
T2 env-hours             = 1.2N × 1.0 hr      → 1.2N env-hr/month
avg concurrency          = 1.2N / (30 × 24)   → 0.00167N
peak concurrency (~8×)   = 0.0133N
envs per node            = 3
peak nodes               = ceil(0.0133N / 3) = ceil(0.00444N)
autoscaler max           = peak nodes + 1 (headroom)
```

**Worked examples:**

| Cohort `N` | Peak concurrent T2 envs | Peak T2 nodes | Autoscaler `max` | Steady-state nodes (off-peak) |
|---|---|---|---|---|
| 200 | ~3 | 1 | 2 | 0 (min=0) |
| 1,000 | ~13 | 5 | 6 | 0–1 |
| 10,000 | ~133 (matches `memory.md` "~120 T2") | 45 | 48 | ~2 |

For a **live class** (you know the schedule), pre-warm: if 30 learners will hit
a T2 sim in the same 15-minute window, that is 30 concurrent → 10 nodes → set
the autoscaler `min=10` for that window and back to `min=0` after
(`memory.md` §5.5 "pre-warm 200 environments 15 minutes before").

### 4.4 Cost model

| Line | Value | Source |
|---|---|---|
| T2 env cost band | **$0.10–0.35 / env-hr** | `memory.md` §5.1 tier table |
| Reference: T2 k8s sim (60 min) | **~$0.22** | `memory.md` §1815 |
| Per cohort/month | `1.2N env-hr × $0.22` | derived |
| — N = 200 | ~$53 / month | |
| — N = 1,000 | ~$264 / month | |
| — N = 10,000 | ~$2,640 / month | |
| Idle pool burn | **~$0** off-peak | autoscaler `min=0` |
| Warm-pool burn (if enabled for T2) | pool_floor × node_hourly × (1 − utilisation) | `memory.md` §5.5 trade-off formula; **T2 warm pool is currently not populated** — `server.go` only calls `warmPool.Claim` for `TierT1SharedContainer`. Leave it cold unless p95 time-to-ready on T2 is missing SLO. |

**Budget enforcement is already tier-agnostic.** `internal/costmeter` emits
`usage_meter` rows keyed by environment with a `tier` tag; T2's higher $/hr
flows through the existing 50/80/100/120% evaluator chain (`memory.md` §10.4)
with no new code. Set a per-cohort `budget` row sized from §4.4 and the
120%-trip (revoke + snapshot + force-destroy + page) protects you.

### 4.5 SLOs to publish for T2

From `memory.md` §5.5 / §11.1:

| SLI | SLO |
|---|---|
| Time-to-ready p50 / p95 | ≤ 3 s / ≤ 20 s (warm) — but T2 warm pool is cold today, so realistically **60–90 s cold** (`memory.md` §5.1) until a warm pool is populated |
| Provision success rate | ≥ 99.5% |
| Zero orphan microVMs | `increase(orchestrator_reaper_orphans_found_total[1h]) == 0`, T2 namespaces included |

---

## 5. Runbook — one full T2 environment lifecycle (create → connect → destroy)

Run this **after** §2 and §3, **before** learner traffic. It is the concrete
version of Phase 2 requirement A ("Run full lifecycle of one T2 env … confirm
no leftover resources").

### 5.1 Preconditions

```bash
export KUBECONFIG=<practice cluster kubeconfig>
kubectl get nodes -l practiceengine.dev/tier2=true          # ≥1 Ready
kubectl get runtimeclass kata                                # exists
# orchestrator running with ORCHESTRATOR_T2_ENABLED=true, reachable at $ORCH_GRPC
```

### 5.2 Create

Drive it through the real RPC path (grpcurl against the orchestrator, with the
shared secret / mTLS your deployment uses). `attempt_id` must be a real UUID
that exists in `attempt` (the ownership check in `server.go` requires it).

```bash
grpcurl -H "authorization: Bearer $ORCHESTRATOR_SHARED_SECRET" \
  -d '{"attempt_id":"<uuid>","blueprint_version_id":"<t2 sim bp>","tier":"TIER_T2_ISOLATED_MICROVM","resources":{}}' \
  $ORCH_GRPC orchestrator.Orchestrator/Provision
# expect: {"environmentId":"...","status":"READY"} — NOT FailedPrecondition
```

Then verify the environment is real:

```bash
ENV=env-<environmentId>
kubectl -n $ENV get pod workspace -o jsonpath='{.spec.runtimeClassName}'   # kata
kubectl -n $ENV get pod workspace -o wide                                   # Running, on a tier2 node
kubectl -n $ENV get resourcequota env-quota -o jsonpath='{.spec.hard}'      # cpu:8 memory:16Gi pods:20 ...
kubectl get ns $ENV -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'  # privileged
```

### 5.3 Connect + verify the four capabilities

Open a shell via the Session Broker (the same `Connect` RPC the web UI uses),
or for the smoke test `kubectl -n $ENV exec -it workspace -- bash`. Inside:

```bash
# 1. Docker-in-Docker
dockerd &                       # or: systemctl start docker  (see systemd check)
docker run --rm hello-world     # must print "Hello from Docker!"

# 2. Multi-node k3s (nested)
curl -sfL https://get.k3s.io | sh -            # server
# add a second node in a nested container / second pod, or k3d:
k3d cluster create t2check --agents 2
kubectl --context k3d-t2check get nodes        # 1 server + 2 agents, all Ready

# 3. systemd
systemctl is-system-running                    # running (or degraded, not offline)
systemd-run --unit=t2check /bin/true && systemctl status t2check

# 4. eBPF
apt-get install -y bpftrace 2>/dev/null || true
bpftrace -e 'tracepoint:syscalls:sys_enter_execve { printf("%s\n", comm); }' -c 'ls' \
  | head -3                                    # prints comm names → eBPF verifier + attach worked
```

Record the output of all four in the closeout. If any fails, T2 is **not**
"working in real" per Phase 2's rule — investigate before proceeding.

### 5.4 Destroy + zero-leftover check

```bash
grpcurl -H "authorization: Bearer $ORCHESTRATOR_SHARED_SECRET" \
  -d '{"attempt_id":"<uuid>","environment_id":"<environmentId>"}' \
  $ORCH_GRPC orchestrator.Orchestrator/Destroy

# namespace gone
kubectl get ns $ENV                                  # NotFound
# no dangling PV (T2 allows 4 PVCs — make sure none leaked as Released PVs)
kubectl get pv | grep $ENV                           # empty
# node scaled back down (give the autoscaler its cooldown, then:)
kubectl get nodes -l practiceengine.dev/tier2=true   # back toward min=0 if no other T2 envs
# reaper counter did not register an orphan
curl -s $ORCH_METRICS/metrics | grep reaper_orphans_found_total   # unchanged
```

Then run the sustained check:

```bash
evaluation/phase1/load/check-orphans.sh 3600
# ...and again ≥1h later. Both PASS.
```

### 5.5 Automated version of this runbook

`internal/k8s/provision_t2_live_test.go` already exists and currently asserts
the **failure** mode (`RuntimeClass "kata" not found`) because this dev cluster
has no Kata node. **Once your `practice-t2` pool is live**, that test's own
header says to flip its expectation. The follow-up work (tracked in §7) is to
turn it into a positive end-to-end test: Provision(T2) → assert pod Running
with `runtimeClassName: kata` on a tier2 node → assert the quota/PSS values →
Destroy → assert namespace gone. Run it in CI against a Kata-capable runner.

---

## 6. Security posture recap (feeds requirement D)

| Control | T1 | T2 | Where |
|---|---|---|---|
| Kernel isolation | gVisor (shared kernel, syscall intercept) | **Kata microVM (own kernel)** | `applyT2PodShape` RuntimeClass |
| Privileged containers | forbidden (PSS `restricted`) | **allowed** (PSS `privileged`) — compensated by Kata | `pssLevelFor` |
| SA token in pod | not mounted | not mounted | `applyServiceAccount` |
| In-cluster egress | default-deny, egress-proxy only | default-deny, egress-proxy only | `applyNetworkPolicy` |
| K8s API reachable from learner pod | no | no | no SA token + NetworkPolicy |
| Tier decision | at `Provision` RPC, from `req.Tier` | same, gated on `T2Enabled`, fails closed | `resolveTier` |
| Learner self-placement onto T2 node | impossible (no API creds to submit a pod spec) | impossible | `node-pool-taint.md` threat model |

The **NetworkPolicy that scopes gRPC ingress to the orchestrator pod**
(`manifests/t1/orchestrator-netpol.yaml`) and **running the orchestrator as a
K8s pod** are requirement **D**, covered in the D setup doc — they are not T2's
job but T2's rollout is a good time to do them since you are already deploying
cluster infra.

---

## 7. Work-item status (Phase 2 requirement A)

| # | Item | Status |
|---|---|---|
| 1 | **`infra/practice-cluster/t2-nodepool/`** — OpenTofu for `practice-t2` (ARM Graviton `*.metal` SPOT, taint/label, `min=0` autoscaler tags, spot-interruption SQS+EventBridge, `kata` RuntimeClass, kata-deploy Firecracker shim, image pre-pull) | **Authored.** `tofu fmt` + `tofu validate` clean. Has two `check` blocks (env-ceiling matches `DefaultT2Resources`; `min_size==0`). **Apply `[B]`** — needs the practice EKS cluster + credentials. |
| 2 | **Positive `provision_t2_live_test.go`** — `TestProvisionT2_Lifecycle` | **Done.** Auto-detects a Kata node; asserts the honest "no kata node" failure today, and the full positive lifecycle (Kata pod on tier2 node → PSS privileged → quota = `DefaultT2Resources` → clean Destroy → no PV leak) automatically once the pool is up. Passing now (negative branch). |
| 3 | **`scripts/t2-lifecycle-check.sh`** — end-to-end harness (real RPCs + `kubectl exec`) covering create → connect → DinD/k3s/systemd/eBPF → destroy → zero-leftover | **Done.** `bash -n` clean. Runs `[B]` — needs the pool + a running orchestrator with `ORCHESTRATOR_T2_ENABLED=true`. This is the artifact that verifies requirement A's "Verify inside T2" bullets. |
| 4 | **T2-specific TTL** — `ttl.EnvironmentDefaultT2 = 45m`, `resolveEnvTTL()` | **Done.** Built, vetted, 7 tests. Caps a walked-away microVM at ~half the cost of the 90-min T1 default. |
| 5 | **T2 warm pool** — `server.go` never claims warm for T2 | **Deliberately not built** (cost — see `t2-cost-optimization.md` §3.3). Revisit only if cold-start p95 misses the §4.5 SLO. |
| 6 | **Three T2-gated fault sim activities** (`f.istio.*`, `f.gitops.argocd-*`) | **Deferred to requirement F/B** — handlers are real; the sim activities can only be authored/tested once the pool is up. |
| 7 | **Capacity-model drift guard** | **Partial.** `provision_t2_test.go` pins the T1<T2 *relationship*; `provision_t2_live_test.go` pins the *absolute* quota == `DefaultT2Resources`; the tofu `check` block pins the infra side. A CI job asserting §4.1's absolute numbers against `DefaultT2Resources` is still worth adding.

### What "requirement A — done" requires (not yet satisfiable here)

Everything above is authored, built, and tested to the limit of an
environment with no KVM-capable node. To mark A **done** per the Phase 2
rule ("not tested in real environment → NOT complete"):

1. Apply `infra/practice-cluster/t2-nodepool/` against the practice EKS
   cluster; confirm a Ready `practiceengine.dev/tier2=true` node and
   `kubectl get runtimeclass kata`.
2. Clear the zero-orphan sequencing gate (§1).
3. Set `ORCHESTRATOR_T2_ENABLED=true`.
4. Run `scripts/t2-lifecycle-check.sh` → it must exit 0 with all four
   capability checks green.
5. `go test ./internal/k8s/ -run TestProvisionT2_Lifecycle` against that
   cluster → it must take the positive branch and pass.
6. Record both outputs in `PHASE2_CLOSEOUT.md`.
