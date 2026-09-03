# infra/practice-cluster/ — the practice cluster (T1 + Sysbox-T2)

The regional cluster T1 and T2 learner environments run on. Phase 1
shipped on local docker-compose k3s only; this is the real deployment.

**T2 runtime decision (₹100/user):** T2 runs under **Sysbox**
(`sysbox-runc`) on the **same node pool as T1** — real DinD / systemd /
nested multi-node k3s at ~₹0 marginal cost. See
`orchestrator/docs/t2-cost-optimization-100.md`. The Kata metal path is
the documented scale-up option.

## Layout

| Dir | What | Status |
|---|---|---|
| `gke/` | OpenTofu: regional **GKE** Standard cluster + `practice-t1` pool with **GKE Sandbox (gVisor)** + untainted `platform` pool. The gVisor-capable regional path — GKE gives the `gvisor` RuntimeClass (handler `runsc`) as a one-flag node-pool feature, matching `internal/k8s/provision.go` `runtimeClassForT1`. | `tofu validate` + `tofu fmt` clean; **apply `[B]`** (needs `gcloud` auth + billing-enabled project) |
| `bootstrap/k3s-sysbox-node.sh` | **Single ARM VM + k3s + Sysbox** — one script, the ₹100/user host target. Installs k3s, Sysbox, the RuntimeClass; runs a DinD/systemd smoke test. Single node, Sysbox (T2) only — no gVisor. | authored; `bash -n` clean; **run on a real Ubuntu 24.04 ARM server** |
| `sysbox/` | `sysbox-runc` RuntimeClass + `sysbox-deploy-k8s` DaemonSet — the **T2** runtime (for a multi-node cluster instead of route A). Not gVisor. | authored; `kubectl apply --dry-run` clean |
| `cluster/` | OpenTofu: practice **EKS** cluster (VPC, single NAT, ARM Graviton spot T1 node group, OIDC/IRSA). AWS-native alternative to `gke/`. Note: gVisor is **not** installed by anything in this repo on the EKS path (`sysbox/` is Sysbox/T2 only) — it needs a custom AMI or a `runsc`-install DaemonSet, not yet written. | `tofu validate` + `tofu fmt` clean; **apply `[B]`** (needs AWS creds) |
| `t2-nodepool-kata/` | OpenTofu: dedicated Kata/Firecracker **bare-metal** T2 pool. **NOT the default** — the scale-up path for >~150 concurrent T2 envs. | `tofu validate` + `tofu fmt` clean; **apply `[B]`** |

## Which host?

Decision rule from `t2-cost-optimization-100.md` §3:

| Cohort (monthly active) | Host | All-in ₹/user/mo |
|---|---|---|
| pilot / ~20–30, ≤5 concurrent | **Oracle Cloud Always Free** ARM, Mumbai (`bootstrap/`) | **₹0** ✅ |
| < ~200 | **Single paid ARM VM + k3s** (Hetzner/Oracle, `bootstrap/`) | ₹30–95 ✅ |
| ~300+ | AWS EKS (`cluster/`) + `sysbox/` DaemonSet | ₹50–95 ✅ |
| needs real gVisor T1 on a regional multi-node cluster | **GKE** regional + GKE Sandbox (`gke/`) | control plane ~$73/mo + nodes |
| > ~150 concurrent T2, or guest-kernel content | add `t2-nodepool-kata/`, set `ORCHESTRATOR_T2_RUNTIME_CLASS=kata` | — |

## Provision runbook (single ARM VM path)

See `PROVISION-RUNBOOK.md` in this directory — the step-by-step you
follow once the server exists.
