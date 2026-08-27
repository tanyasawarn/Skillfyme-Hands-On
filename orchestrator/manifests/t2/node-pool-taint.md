# T2 node pool: taint/label and capacity plan (doc §5.1, PLAN.md Phase 2)

Same category of artifact as `manifests/t1/node-pool-taint.md`: a
**cloud-provider node-pool configuration**, not a Kubernetes object you
`kubectl apply` — set when the node pool/instance group itself is
created. Cannot be demonstrated on this project's single-node local k3s
cluster (same reason T1's equivalent doc gives: a single node can't host
a second, differently-tainted pool). Documented here as real
infrastructure-as-code intent, verified by internal consistency against
`internal/k8s/provision.go`'s T2 branch (`applyResourceQuota`,
`applyLimitRange`, `applyT2PodShape`) and against doc §5.1's own cost/
latency/isolation table, not by a live deployment.

## Taint and label

```
Node group: practice-t2 (dedicated instance type with KVM/nested-virt exposed)
Taint:      workload=learner-t2:NoSchedule
Label:      practiceengine.dev/tier2=true
```

`internal/k8s/provision.go`'s `applyT2PodShape` already sets the matching
`nodeSelector: {practiceengine.dev/tier2: "true"}` and toleration
(`workload=learner-t2:NoSchedule`) on every T2 workspace pod — this file
is the node-pool side of that pairing, same "one line each side" contract
T1's taint doc describes. Keep this taint/label pair **distinct** from
T1's (`workload=learner:NoSchedule` / no tier label) so a T1 pod can
never accidentally land on a T2 (KVM-backed, more expensive) node, and a
T2 pod can never land on a T1 (no KVM, Kata won't start) node.

## Threat model: T1's `privileged: true` / PSS `privileged` trade-off

T2 trades away two controls T1 relies on, and this is a deliberate,
scoped trade -- not an oversight:

- **PodSecurity level**: T1 namespaces enforce PSS `restricted` (no
  privileged containers, no host namespaces, mandatory non-root,
  seccomp). T2 namespaces enforce PSS `privileged` (`internal/k8s/
  provision.go`'s `pssLevelFor`), the least restrictive built-in level,
  because `applyT2PodShape` sets `Privileged: true` on the shell
  container -- required for DinD/systemd/eBPF (doc §5.1), and PSS
  `restricted` would reject that container outright at admission time
  before scheduling is even considered (the bug this pass found and
  fixed in `createNamespace`).
- **What this actually trades away**: on a T1 namespace, even a fully
  compromised workspace container is still constrained by seccomp,
  dropped capabilities, and non-root UID -- a real, meaningful barrier
  against container escape. A T2 namespace's `privileged: true` container
  has none of those container-level constraints.
- **Why this is acceptable specifically for T2**: the compensating
  control isn't PSS at all, it's **Kata's hardware virtualisation** --
  each T2 pod's containers run inside their own microVM with their own
  kernel (doc §5.1's isolation column: "Hardware-virtualised; own
  kernel"), so a container-escape from `privileged: true` lands inside
  that microVM, not on the shared host's kernel. T1's model relies on
  container-level restrictions BECAUSE its containers share the host
  kernel (gVisor intercepts syscalls, but there's still one kernel); T2's
  model relies on VM-level isolation INSTEAD OF container-level
  restrictions, because the isolation boundary moved up a layer. Running
  `privileged: true` on a T1 (gVisor, shared-kernel) node would be a real
  vulnerability; running it on a genuine Kata node is the documented,
  intended T2 trade.
- **What makes the isolation boundary between T1 and T2 sound, not just
  documented**: the T2 pod shape (`RuntimeClassName: kata`, the
  `practiceengine.dev/tier2` nodeSelector/toleration pair, `Privileged:
  true`) is applied by exactly one code path --
  `internal/k8s/provision.go`'s `Provision()`, gated on `req.Tier ==
  TierT2IsolatedMicroVM`, which is itself set exclusively by
  `internal/orchestrator/server.go`'s `resolveTier()` at the RPC
  boundary (see `server_test.go`'s
  `TestResolveTier_T2NeverSilentlyDowngradesToT1` for the regression
  guard on that gate). A learner's workspace pod has no path to request
  a different pod spec for itself: `ServiceAccount.
  AutomountServiceAccountToken: false` (`applyServiceAccount`) means the
  workspace pod has no K8s API credentials at all, so even a fully
  compromised T1 workload cannot submit a new pod spec, modify its own
  `nodeSelector`/tolerations, or otherwise request placement onto a T2
  (privileged-capable) node. The T1/T2 boundary is enforced by the
  orchestrator's own RPC-level tier decision plus the learner's total
  lack of cluster API access, not by convention.

## Why a second pool, not a bigger T1 pool

Doc §5.1's isolation column: T1 is "gVisor + NetworkPolicy + namespace +
quota" (shared kernel, syscall-interception boundary); T2 is "Hardware-
virtualised; own kernel." These are different *instance types*, not just
different quotas on the same hardware — Kata's microVMs need KVM (or an
equivalent hypervisor) exposed to the node, which most T1-shaped compute
(cheap ARM64 spot instances per doc §5.2) does not have and does not need
to have. Mixing the two into one pool means either paying KVM-capable
prices for every T1 pod (defeats T1's whole cost rationale) or running T2
pods on non-KVM nodes (Kata fails to start). A dedicated pool is the only
shape consistent with doc §5.1's own per-tier cost figures.

## Capacity model

Derived from doc §5.1's T2 row (`$0.10–0.35/hr`, `8–20s warm / 60–90s
cold` start latency) and this pass's `applyResourceQuota`/
`applyLimitRange` T2 values (8 CPU / 16Gi request per environment,
scaled up from T1's 2 CPU / 4Gi for the reasons documented at that call
site: nested k3s control plane + worker pods, DinD image/layer storage).

| Parameter | Value | Basis |
|---|---|---|
| Per-environment request | 8 vCPU, 16Gi mem | `applyResourceQuota`'s T2 `ResourceQuota.Hard` |
| Per-environment ceiling | 8 vCPU, 16Gi mem (LimitRange max) | `applyLimitRange`'s T2 `Max` |
| Instance shape | KVM-capable, ≥32 vCPU / 64Gi per node | headroom for ~3-4 concurrent T2 envs/node before request-level bin-packing pressure, leaving room for node-level overhead (kubelet, kata-agent per-VM overhead) |
| Environments per node (target) | 3 | `floor(32 vCPU / 8 vCPU request) - 1` node reserved for system/kata overhead, not `floor()` exactly — Kata's per-microVM overhead (its own kernel + kata-agent) is non-trivial versus T1's per-pod overhead, so this deliberately under-packs relative to a naive division |
| Pool sizing driver | Concurrent T2 attempts (Production Sim, L4+) | T2 is a minority of traffic by design (doc §5.1: "expect 70-80% of all attempts to run on T1", §5.2) — size this pool against Production Sim's expected concurrency, not total learner count |
| Autoscaling | Cluster-autoscaler on this node group only, min=0 | T2's cost band ($0.10-0.35/hr, 6x T1's) makes idle capacity expensive relative to T1's warm-pool economics; min=0 with the doc's own 8-20s warm / 60-90s cold start budget is the right trade for a pool that may see bursty, not steady, demand |
| Cost control | Same `budget` table / evaluator chain as T1 (doc §10.4), no T2-specific mechanism needed | `internal/costmeter` already emits `usage_meter` rows keyed by environment, tier-agnostic; T2's higher $/hr flows through the existing 50/80/100/120% budget evaluator without a new code path |

## Workload placement rule (enforced in code, not just policy)

`internal/orchestrator/server.go`'s `Provision()` RPC handler is the
single enforcement point: a `TIER_T2_ISOLATED_MICROVM` request is
rejected outright (`FailedPrecondition`) unless
`ORCHESTRATOR_T2_ENABLED=true`, and even when enabled, the warm pool is
never consulted for T2 requests (`Claim` is only called when
`tier == TierT1SharedContainer`) — every T2 environment cold-provisions
through `k8s.Provisioner.Provision` with `Tier: TierT2IsolatedMicroVM`,
which is the only code path that sets the T2 `nodeSelector`/toleration
pair. There is no route by which a T1 request can end up on this pool or
a T2 request on the T1 pool; the routing decision is made once, at
`Provision()`, from the caller's own `req.Tier`, not inferred later from
resource shape.

## Isolation boundary (platform-namespace pods)

Same rule as T1's doc: session broker, egress proxy, reaper, and every
other platform-namespace pod must never carry a toleration for
`workload=learner-t2:NoSchedule` — those pods stay on general-purpose
(non-KVM, cheaper) nodes regardless of which learner-facing pool exists,
since T2's node pool is where the platform's most privileged learner
workloads (DinD, systemd, eBPF) run and must not share a node with
control-plane services.

## Real-deployment steps (not run here)

1. Create a dedicated node group `practice-t2` on KVM-capable instance
   types (bare-metal or nitro-class on AWS, nested-virt-enabled on
   GCP/Azure — see `runtimeclass-kata.yaml`'s own real-deployment notes
   for the Kata install step that must accompany this).
2. Taint every node `workload=learner-t2:NoSchedule`, label
   `practiceengine.dev/tier2=true`.
3. Configure cluster-autoscaler for this node group only, `min=0`,
   `max` sized against expected Production Sim concurrency (start
   conservative — doc's own R1/R9 risk-gate philosophy — and raise once
   real attempt-volume data exists).
4. Confirm PLAN.md's Phase 2 sequencing gate (Phase 1 reaper/teardown
   running orphan-free for a sustained period) against this
   deployment's own history before setting
   `ORCHESTRATOR_T2_ENABLED=true`.
