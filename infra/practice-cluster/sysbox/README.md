# infra/practice-cluster/sysbox/ — the T2 runtime (₹100/user path)

**This is how T2 runs.** Sysbox (`sysbox-runc`) on the same node pool as
T1 — real Docker-in-Docker, systemd-as-PID-1, nested multi-node k3s, and
most eBPF, via Linux user-namespace isolation. No dedicated bare-metal
pool, no microVM, ~₹0 marginal cost for the T2 capability.

See `orchestrator/docs/t2-cost-optimization-100.md` for the cost model
and the Sysbox-vs-Kata comparison. The Kata metal path is kept as the
documented scale-up option in `../t2-nodepool-kata/`.

## Contents

| File | What |
|---|---|
| `runtimeclass-sysbox.yaml` | The `sysbox-runc` RuntimeClass the orchestrator's `applyT2PodShape` references (matches `ORCHESTRATOR_T2_RUNTIME_CLASS`, default `sysbox-runc`). |
| `sysbox-install.yaml` | The upstream **sysbox-deploy-k8s** DaemonSet + its RBAC + a `sysbox-runc` RuntimeClass. Installs the Sysbox binaries and registers the containerd/CRI-O runtime on every node it lands on. Node-selected so it only touches learner nodes. |
| `../bootstrap/k3s-sysbox-node.sh` | For the **single-VM k3s** deployment (the ₹100/user host target): installs k3s, then Sysbox natively via the `.deb`, then applies the RuntimeClass. Use this instead of the DaemonSet when there is exactly one node you control directly. |

## Two install routes

### Route A — single ARM VM + k3s (current production target)

```bash
# on the VM, as root, after provisioning an Ubuntu 24.04 ARM box:
curl -sfL https://raw.githubusercontent.com/<repo>/main/infra/practice-cluster/bootstrap/k3s-sysbox-node.sh | bash
# or copy the script over and run it
```

That script does everything: k3s, Sysbox `.deb`, containerd wiring,
`kubectl apply -f runtimeclass-sysbox.yaml`. Verification steps are at
the end of the script and in `scripts/t2-lifecycle-check.sh`.

### Route B — multi-node cluster (EKS, or k3s with agents)

```bash
kubectl apply -f infra/practice-cluster/sysbox/sysbox-install.yaml
# wait for the DaemonSet to go Ready on every learner node:
kubectl -n kube-system rollout status ds/sysbox-deploy-k8s
kubectl get runtimeclass sysbox-runc
```

The DaemonSet's `nodeSelector` is `practiceengine.dev/tier: t1` (the
label the `cluster/` module's node group sets) — Sysbox installs only on
learner nodes, never on a platform/control-plane pool.

## Requirements

- **Kernel ≥ 5.12** with `CONFIG_USER_NS=y` and unprivileged user
  namespaces enabled (`sysctl kernel.unprivileged_userns_clone=1` on
  older distros; Ubuntu 24.04 has it on by default).
- containerd ≥ 1.6 (k3s bundles a compatible one) or CRI-O.
- The learner namespace's PodSecurity level must be `privileged`
  (`pssLevelFor(TierT2)` already returns this) — Sysbox pods run their
  shell container as `RunAsUser: 0` (remapped by Sysbox to an
  unprivileged host uid), which PSS `restricted`/`baseline` forbid.

## Verify

After either route:

```bash
kubectl get runtimeclass sysbox-runc                       # exists
# quick smoke: a Sysbox pod that runs dockerd
kubectl run sb-smoke --rm -it --restart=Never \
  --overrides='{"spec":{"runtimeClassName":"sysbox-runc"}}' \
  --image=nestybox/ubuntu-noble-systemd-docker -- \
  bash -c 'systemctl start docker && docker run --rm hello-world'
```

Then the real check: `scripts/t2-lifecycle-check.sh` against the running
orchestrator.
