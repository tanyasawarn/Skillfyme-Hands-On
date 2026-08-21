# T1 node pool taint/label (doc §5.2)

Doc: `Node group: practice-t1 (spot, ARM64 where possible, 16–64 vCPU
nodes)`, `Taints: workload=learner:NoSchedule`.

This is a **cloud-provider node-pool configuration**, not a Kubernetes
object you `kubectl apply` — it's set when the node pool/instance group
is created (e.g. an EKS/GKE managed node group's taint config, or a
`kubectl taint nodes` call against pre-existing nodes). A single-node
local k3s cluster has one node running the control plane and all
workloads; tainting it `workload=learner:NoSchedule` would also block the
Orchestrator/session-broker/reaper pods from scheduling unless they carry
a matching toleration, which defeats the purpose of the taint on a
single-node dev cluster. Documented here rather than applied locally —
this is a real production step, deferred because it requires >1 node to
demonstrate correctly.

**What Provisioner (internal/k8s/provision.go) does instead for local
dev:** schedules the workspace pod without a node selector/toleration, so
it lands wherever the k3s scheduler puts it (the only node). The pod spec
already carries the taints a real practice-t1 node pool would require
tolerations for as a comment marker — see `TODO(node-pool)` in
`createWorkspacePod` — so wiring the toleration in for a real multi-node
deployment is a one-line addition once a tainted node pool exists.

**Real-deployment steps (not run here):**
1. Create a dedicated node group named `practice-t1` (managed node group
   API of the target cloud), spot instances, ARM64 where the base images
   support it (doc §5.2 image strategy).
2. Taint every node in that pool: `workload=learner:NoSchedule`.
3. Add matching `tolerations` + `nodeSelector`/`nodeAffinity` to the
   workspace pod spec in `internal/k8s/provision.go`.
4. Keep platform-namespace pods (session broker, egress proxy, reaper)
   on a separate, untainted node pool — never co-schedule them onto
   `practice-t1` nodes, since that pool is where untrusted learner code
   runs (doc §9.1 T2: "control-plane services on a separate cluster/VPC").
