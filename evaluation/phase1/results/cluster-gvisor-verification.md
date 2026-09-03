# Phase 1 — Step 1: regional gVisor cluster verification

**Status:** ⬜ NOT YET RUN — IaC authored and validated; awaiting `gcloud`
auth + project id, then `tofu apply` and `verify-gvisor.sh`.

This file is the evidence artifact for Phase 1 Step 1
("get a real multi-node regional Kubernetes cluster running for T1 with
gVisor"). It is filled in from a real run, not from a plan.

---

## Definition of done

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | Real regional Kubernetes cluster exists | ⬜ | §Cluster |
| 2 | Multiple worker nodes exist | ⬜ | §Nodes |
| 3 | All nodes Ready | ⬜ | §Nodes |
| 4 | kubectl connectivity works | ⬜ | §1 log |
| 5 | gVisor runtime available (`RuntimeClass gvisor`, handler `runsc`) | ⬜ | §gVisor |
| 6 | Minimal workload runs under gVisor (proven in-guest) | ⬜ | §gVisor |
| 7 | Test workload deletes cleanly | ⬜ | §gVisor |
| 8 | Infrastructure reproducible from IaC | ✅ (authored) | `infra/practice-cluster/gke/` — `tofu validate` + `fmt` clean |
| 9 | No unexpected cloud resources created | ⬜ | §Teardown |
| 10 | Exact commands + evidence documented | ⬜ (this file) | below |

---

## Environment

| | |
|---|---|
| Cloud / cluster type | GCP · GKE Standard · **regional** |
| Module | `infra/practice-cluster/gke/main.tf` (provider `hashicorp/google-beta ~> 6.0`) |
| Region | `asia-south1` |
| Release channel | `REGULAR` |
| Node pools | `platform` (3 × e2-standard-4 on-demand, untainted) · `practice-t1` (3 × e2-standard-4 **spot**, GKE Sandbox / gVisor, taint `workload=learner:NoSchedule`, label `practiceengine.dev/tier=t1`) |
| OpenTofu | v1.12.6 |
| Project id | _(fill in)_ |
| Run date (UTC) | _(fill in)_ |
| Operator | tanyasawarn.222@gmail.com |

---

## Commands executed

```
# (paste the actual command history: gcloud auth/config, tofu init/plan/apply,
#  get-credentials, verify-gvisor.sh, tofu destroy, orphan checks)
```

---

## Cluster

```
# kubectl cluster-info
# gcloud container clusters describe practice-cluster --region asia-south1 \
#     --format='yaml(name,location,locations,currentMasterVersion,status,releaseChannel)'
```

_Assert: `location` is a region (not a single zone); `locations` lists ≥3 zones; `status: RUNNING`._

---

## Nodes

```
# kubectl get nodes -o wide
# kubectl get nodes -L topology.kubernetes.io/zone,practiceengine.dev/tier,cloud.google.com/gke-spot
```

_Assert: ≥ 6 nodes, every one `Ready`, spread across ≥ 3 zones; ≥ 3 carry `practiceengine.dev/tier=t1`._

---

## Namespaces

```
# kubectl get namespaces
```

---

## gVisor verification (the real gate)

```
# kubectl get runtimeclass
# kubectl get runtimeclass gvisor -o yaml         -> handler: runsc
# kubectl get nodes -l practiceengine.dev/tier=t1 \
#     -o custom-columns=NAME:.metadata.name,SANDBOX:.metadata.labels.sandbox\.gke\.io/runtime

# kubectl apply -f infra/practice-cluster/gke/testdata/gvisor-smoke-pod.yaml
# kubectl wait --for=condition=Ready pod/gvisor-smoke --timeout=150s
# kubectl get pod gvisor-smoke -o jsonpath='{.spec.runtimeClassName} {.spec.nodeName}'
# kubectl exec gvisor-smoke -- uname -a           -> contains "gVisor"
# kubectl exec gvisor-smoke -- sh -c 'echo exec-ok-$(id -u)'   -> exec-ok-65532
# kubectl delete -f infra/practice-cluster/gke/testdata/gvisor-smoke-pod.yaml
# kubectl get pod gvisor-smoke                    -> NotFound
```

**Proof of genuine sandboxing:** under `runsc` the pod's `uname` /
`dmesg` report the kernel as **gVisor**, not the host Linux release. A
RuntimeClass that exists but is not actually wired to a sandbox cannot
produce that string. `verify-gvisor.sh` step 8 enforces it.

Full log: `evaluation/phase1/results/logs/gvisor-verify-<timestamp>.log`

---

## Teardown

```
# cd infra/practice-cluster/gke && tofu destroy
# gcloud container clusters list --region asia-south1          -> empty
# gcloud compute disks list --filter="zone~asia-south1"        -> only unrelated / empty
# gcloud compute forwarding-rules list --regions asia-south1   -> empty
# gcloud compute addresses list --filter="region:asia-south1"  -> empty
```

_Assert: cluster gone; no leftover disks / LBs / static IPs from this test
(the test creates no PVC, Service type=LoadBalancer, or address)._

---

## Result

_PASS / FAIL + one-line summary once the run is done._
