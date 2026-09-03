# infra/practice-cluster/gke/ — regional practice cluster, gVisor path

The real regional Kubernetes cluster T1 learner environments run on, with
**gVisor** as a managed node-pool feature (GKE Sandbox). This is the
Phase 1 M1.1 deliverable ("gVisor RuntimeClass, node pool, …") that
Phase 1 shipped without — Phase 1 ran on local docker-compose k3s only.

## Why GKE here (the sibling `../cluster/` is EKS)

The orchestrator sets `spec.runtimeClassName = "gvisor"` on every T1 pod
when `ORCHESTRATOR_GVISOR_ENABLED=true`
(`orchestrator/internal/k8s/provision.go` → `runtimeClassForT1`).
Something must install the `runsc` runtime handler on the learner nodes
and create that RuntimeClass.

- **GKE Sandbox** does this with one flag on the node pool
  (`sandbox_config.sandbox_type = "gvisor"`). GKE wires containerd, ships
  `runsc`, and creates a `RuntimeClass` named `gvisor` with handler
  `runsc` — exactly what the orchestrator already emits. Nothing to
  hand-roll.
- On **EKS** the same result needs a custom AMI or a privileged DaemonSet
  that rewrites every node's containerd config. This repo does not have
  that. `../cluster/main.tf` remains the AWS-native option; its old
  comment claiming gVisor is installed by `../sysbox/` was inaccurate
  (`../sysbox/` installs **Sysbox**, the T2 runtime — see the corrected
  comment there and `../README.md`).

Both are the same architectural role (memory.md §5.2: "Practice Cluster
(regional, **EKS/GKE**)"), not a parallel design — the same way
`../sysbox/` and `../t2-nodepool-kata/` are two routes to the T2 runtime.

## What this builds

Regional GKE Standard cluster (`asia-south1`), release channel `REGULAR`,
Dataplane V2 (eBPF NetworkPolicy enforcement), two node pools:

| Pool | Nodes | Type | Sandbox | Taint / label |
|---|---|---|---|---|
| `platform` | 1/zone × 3 = 3 | `e2-standard-4` on-demand | none (system pods can't run under GKE Sandbox) | — |
| `practice-t1` | 1/zone × 3 = 3 | `e2-standard-4` **spot** | **gVisor** | `workload=learner:NoSchedule`, `practiceengine.dev/tier=t1` |

Taint/label match `internal/k8s/provision.go` `createWorkspacePod` and the
`../sysbox/` + `orchestrator/manifests/t1/` node selectors exactly.

## Cost

| Item | ~USD/hr |
|---|---|
| Regional control plane | 0.10 |
| `platform` pool (3 × e2-standard-4 on-demand) | ~0.40 |
| `practice-t1` pool (3 × e2-standard-4 spot) | ~0.16 |
| **Total** | **~$0.66/hr + disk/egress** |

Created and destroyed the same day for the Phase 1 proof → a few USD.
The verification creates **no** PVC, LoadBalancer, or static IP, so
`tofu destroy` leaves nothing behind.

## Use

```bash
# prereqs (once, interactive):
gcloud auth login
gcloud auth application-default login
gcloud config set project <PROJECT_ID>
gcloud services enable container.googleapis.com compute.googleapis.com

cd infra/practice-cluster/gke
cp terraform.tfvars.example terraform.tfvars     # set project_id
tofu init
tofu validate && tofu fmt -check
tofu plan -out tfplan                            # review — no resources yet

tofu apply tfplan                                # ~8–12 min

# connect kubectl (or: terraform output -raw get_credentials_command)
gcloud container clusters get-credentials practice-cluster \
  --region asia-south1 --project <PROJECT_ID>

# the hard gate — every [CHECK] must pass:
./verify-gvisor.sh | tee ../../../evaluation/phase1/results/logs/gvisor-verify-$(date +%Y%m%dT%H%M%SZ).log
```

## Destroy (always, after the proof)

```bash
cd infra/practice-cluster/gke
tofu destroy

# confirm nothing left in the region:
gcloud container clusters list --region asia-south1
gcloud compute disks list --filter="zone~asia-south1"
gcloud compute forwarding-rules list --regions asia-south1
gcloud compute addresses list --filter="region:asia-south1"
```

## Integrating with the orchestrator (next step — not done here)

1. `gcloud container clusters get-credentials …` → kubeconfig.
2. Point the orchestrator at it: `KUBECONFIG=<that file>` (local process)
   or an in-cluster ServiceAccount (`orchestrator/manifests/platform/`).
3. Set `ORCHESTRATOR_GVISOR_ENABLED=true`. `runtimeClassForT1(true)` then
   stamps `runtimeClassName: gvisor` on T1 workspace pods, which the
   `practice-t1` pool honours.
4. Apply the T1 platform manifests: `orchestrator/manifests/t1/`
   (`daemonset-image-prepull.yaml`, `egress-proxy.yaml`), and let the
   orchestrator's namespace-per-environment template create the
   ResourceQuota / LimitRange / default-deny NetworkPolicy / PSS
   `restricted` per environment.
5. Verify the **real workspace image** complies with GKE Sandbox pod
   constraints (no host namespaces/paths/privileged) before load.
