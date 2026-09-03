# infra/practice-cluster/t2-nodepool-kata/ — the T2 SCALE-UP path (not the default)

> **This is NOT how T2 runs today.** As of the ₹100/user cost decision,
> T2 runs under **Sysbox** on the shared T1 node pool —
> `infra/practice-cluster/sysbox/` + `ORCHESTRATOR_T2_RUNTIME_CLASS=sysbox-runc`
> (the orchestrator default). See `orchestrator/docs/t2-cost-optimization-100.md`.
>
> This module — a dedicated **Kata / Firecracker microVM** node pool on
> ARM Graviton **bare-metal** — is kept, authored and validated, as the
> path to switch to **once concurrent T2 volume makes a metal pool
> economical** (roughly: hundreds of concurrent T2 environments, so the
> `*.metal` nodes stay well-packed and the `$0.10–0.35/env-hr` figure in
> `memory.md` §5.1 becomes real instead of the `~$1–2.90/env-hr` a
> lightly-loaded metal node actually costs).

## When to switch from Sysbox to this

Switch when **all** of these hold:

1. Concurrent T2 environments routinely exceed ~100–150 (so a
   64-vCPU `c7g.metal` runs 6+ envs and packs well).
2. You have a security or content reason that needs a **guest kernel** —
   kernel-module labs, LSM-BPF, custom schedulers — that Sysbox's
   shared-kernel user-namespace isolation can't give (your current 5
   T2-gated faults do **not** need this; they're Istio / ArgoCD / IAM).
3. The fixed cost of the metal pool (EKS is already paid; the pool
   itself autoscales from 0) is amortised across enough T2 attempts that
   per-attempt cost drops below the Sysbox-on-shared-nodes cost.

Until then, Sysbox is both cheaper and faster-starting.

## What `main.tf` builds

| Resource | Purpose |
|---|---|
| `aws_eks_node_group.t2` — `practice-t2`, ARM Graviton `*.metal` **SPOT** | KVM-capable nodes for Kata/Firecracker |
| taint `workload=learner-t2:NoSchedule` + label `practiceengine.dev/tier2=true` | pin T2 pods here, keep T1 off |
| cluster-autoscaler discovery tags, `min=0` | scale to nothing when no T2 env is scheduled |
| SQS queue + EventBridge rule for EC2 spot interruption | 2-min-notice drain → orchestrator snapshot-and-destroy |
| `kata` RuntimeClass (Firecracker shim) + kata-deploy DaemonSet | the microVM runtime, node-selected to this pool only |
| T2 image pre-pull DaemonSet | cold-start latency |

`tofu fmt` + `tofu validate` clean. Two `check` blocks: env-ceiling
matches `DefaultT2Resources`; `min_size == 0`.

## To activate this path

1. `tofu apply` this module against the practice EKS cluster
   (`infra/practice-cluster/cluster/`), passing its outputs
   (`cluster_name`, `private_subnet_ids`).
2. Confirm `kubectl get runtimeclass kata` and a Ready
   `practiceengine.dev/tier2=true` node (force one by scaling the
   nodegroup `minSize=1` briefly).
3. Deploy the cluster-wide **AWS Node Termination Handler**
   (queue-processor mode) pointed at this module's
   `spot_interruption_queue_url` output.
4. Set `ORCHESTRATOR_T2_RUNTIME_CLASS=kata` on the orchestrator and
   restart it. `applyT2PodShape` then emits `runtimeClassName: kata`.
   **Note:** the current `applyT2PodShape` (Sysbox-shaped) does **not**
   add the `learner-t2` toleration / `tier2` nodeSelector any more —
   re-add that branch, gated on `T2RuntimeClass == "kata"`, before using
   this pool, or Kata pods won't land on the metal nodes.
5. Run `scripts/t2-lifecycle-check.sh` and
   `go test ./internal/k8s/ -run TestProvisionT2_Lifecycle` against the
   cluster.

## `prod.tfvars` sketch

```hcl
region       = "ap-south-1"
cluster_name = "practice-cluster"
subnet_ids   = ["subnet-xxxx"]              # single private AZ subnet
node_max_size = 4                           # from t2-setup-and-operations.md §4.3
workspace_base_image = "<registry>/practiceengine/linux-tools:v1"
kata_deploy_ref      = "3.10.1"
# node_min_size stays 0
```
