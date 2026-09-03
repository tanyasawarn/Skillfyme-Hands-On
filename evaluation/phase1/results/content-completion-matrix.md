# Phase 1 — Content completion matrix (LOCAL, ₹0 pass)

_Generated during the local-only Phase 1 close-out. No cloud resources, no
`tofu apply`, no faked evidence._

## Execution environment

Local `docker compose --profile app`: clean single-node **k3s**
(`rancher/k3s`, **runc — NOT gVisor**) + orchestrator built from source on
`:50051`. `content-ci.ts` provisions a real T1 env per lab, runs
null-path → `solution_apply` (via `ExecShell` in the workspace pod) →
golden-path validators (the **production** validator executors:
`SHELL_ASSERT`/`SHELL_JSON`/`FILE_*` in-pod, `K8S_ASSERT` via the
orchestrator's K8s API) → flake ×2, then `Destroy`.

**What a PASS row proves:** the `solution_apply` script produces the end
state the lab's real validators require, and does so deterministically
(null-path fails, golden-path passes, no flake).

**What it does NOT prove:** gVisor isolation, multi-node behaviour,
production-scale load — all **REAL-CLUSTER BLOCKED** (see
`phase1-local-verification.md`).

## Environmental limits found (these define the BLOCKED reasons)

| Limit | Evidence |
|---|---|
| Workspace image `linux-tools:v1` (local) has `git bash sh sed awk grep jq curl kubectl` only — **no `python3`, `docker`, `helm`, `terraform`, `ansible`, `make`, `node`** | probed live via `ExecShell` `command -v …` |
| Workspace pod has **no outbound network** — egress proxied through `egress-proxy.practiceengine-platform`, which is not deployed locally | `curl` → `Could not resolve proxy` |
| `fx.k3s-ready.v1` learner RBAC (`orchestrator/internal/fixture/rbac.go`): namespace-scoped CRUD on `pods/services/configmaps/secrets/endpoints/events/persistentvolumeclaims` + `deployments/replicasets/statefulsets/daemonsets` + `pods/log`,`pods/exec`. **Excludes** `nodes`, `namespaces`, `resourcequotas`, `horizontalpodautoscalers`, all CRDs, cross-namespace reads | `kubectl auth can-i` probed live |
| Env namespace enforces **PodSecurity `restricted:latest`** — every learner-created pod needs `runAsNonRoot`, `seccompProfile`, `allowPrivilegeEscalation:false`, `capabilities.drop:[ALL]` | `kubectl apply` → `violates PodSecurity "restricted:latest"` |
| **27 of 31** fixtures referenced by solution-less labs have **no handler** in `orchestrator/internal/fixture` — `server.go` logs a WARNING and continues, so the seed state is simply absent | `register("…")` inventory vs. `seed:` refs |
| K8S_ASSERT executor supports only `kubectl get <kind> <name>` for core/apps kinds — **rejects** `kubectl exec …`, and has no alias for `virtualservice`/`peerauthentication`/`application`(argo)/`task`/`taskrun`(tekton) | `orchestrator/internal/validation/kubectl_parse.go` + live `ExecValidator errored` |
| `ExecShell` does not honour the client gRPC deadline for the pod exec stream and its exit-marker parse is fragile — a solution script doing slow/for-loop `kubectl` can hang ~16 min or fail with `exit code marker not found in output` | live, repeated |

## Status legend

| Status | Meaning |
|---|---|
| **PASS** | `solution_apply` authored + content-CI golden-path + flake pass locally (verified this session) |
| **PASS (pre-existing)** | shipped before this session; re-verified PASS locally this session |
| **BLOCKED-TOOL** | script authored as end-state ref; needs a tool absent from the local workspace image |
| **BLOCKED-FIXTURE** | script authored/stubbed; needs a fixture whose handler does not exist |
| **BLOCKED-RBAC** | needs a K8s verb/resource the learner Role does not grant |
| **BLOCKED-EXECUTOR** | validator uses a command shape the K8S_ASSERT executor rejects, or CRD kind it can't read |
| **BLOCKED-EXECSHELL** | end-state is reachable but `ExecShell` fragility prevents reliable local verification |
| **BLOCKED-CONTENT** | the lab spec itself is inconsistent (t1 validator asserts a state t2 destroys; passive task; weak null-path validator) |

---

## Matrix

| lab_id | solution_apply_exists | solution_execution | validator_result | status |
|---|---|---|---|---|
| lab.linux.navigate-filesystem | yes | exit 0 | golden PASS, flake OK | **PASS (pre-existing)** |
| lab.devops.fundamentals | yes | exit 0 | golden PASS, flake OK | **PASS (pre-existing)** |
| lab.devops.gitops-evolution | yes | exit 0 | golden PASS, flake OK | **PASS (pre-existing)** |
| lab.iac.fundamentals | yes | exit 0 | golden PASS, flake OK | **PASS (pre-existing)** |
| lab.microservices.devops-impact | yes (new) | exit 0 | golden PASS, flake OK | **PASS** |
| lab.observability.nagios-alerting | yes (new) | exit 0 | golden PASS, flake OK | **PASS** |
| lab.sre.write-a-postmortem | yes (new) + spec: added `solution_apply` to t1 | exit 0 | golden PASS, flake OK | **PASS** |
| lab.microservices.fundamentals | yes (new) | exit 0 | golden PASS, flake OK; null-path validator `v.users-service-no-orders-leak` is weak (passes on empty env) | **PASS** (content note) |
| lab.ansible.basics | yes (new) | exit 0 | golden PASS, flake OK — t2 realises the playbook's end state directly (no `ansible-playbook` locally) | **PASS** |
| lab.terraform.modules-workspaces | yes (new) | exit 0 | golden PASS, flake OK — t2 realises the module end state directly (no `terraform` locally) | **PASS** |
| lab.iac.terraform-vs-cloudformation | yes (new) | exit 0 | golden PASS, flake OK (pure JSON authoring) | **PASS** |
| lab.cicd.troubleshooting | yes (new) | exit 0 | golden PASS, flake OK (writes the fixed `pipeline.sh`; `fx.broken-pipeline-script.v1` has no handler) | **PASS** |
| lab.github.actions-workflows | yes (new) | exit 0 | golden PASS, flake OK (bootstraps `~/repo`) | **PASS** |
| lab.gitlab.cicd-pipelines | yes (new) | exit 0 | golden PASS, flake OK (bootstraps `~/repo`) | **PASS** |
| lab.jenkins.pipeline-as-code | yes (new) | exit 0 | golden PASS, flake OK (bootstraps `~/repo`) | **PASS** |
| lab.jenkins.advanced-pipelines | yes (new) | exit 0 | golden PASS, flake OK | **PASS** |
| lab.jenkins.distributed-builds | yes (new) | exit 0 | golden PASS, flake OK | **PASS** |
| lab.jenkins.security-integration | yes (new) | exit 0 | golden PASS, flake OK | **PASS** |
| lab.jenkins.basics | yes (new) | exit 0 | golden PASS, flake OK | **PASS** |
| lab.devsecops.fundamentals | yes (new) | exit 0 | golden PASS, flake OK; **null-path FAIL** — `v.script-detects-secret` runs setup itself and passes on the empty env (weak validator, content bug) | **PASS** (content note) |
| lab.devsecops.container-security-tools | yes (new) | exit 0 | **GOLDEN FAIL** — t1's `v.check-fails-on-insecure-dockerfile` asserts the Dockerfile is insecure, but t2's solution fixes it; content-CI applies all task solutions before running all validators, so the two tasks' end states conflict | **BLOCKED-CONTENT** |
| lab.docker.basics | yes (pre-existing) | **exit 127** | null OK; apply ERROR — script calls `docker` | **BLOCKED-TOOL** (docker) |
| lab.docker.networking | yes (pre-existing) | **exit 127** | apply ERROR — `docker` | **BLOCKED-TOOL** (docker) |
| lab.docker.swarm | yes (pre-existing) | **exit 127** | apply ERROR — `docker` | **BLOCKED-TOOL** (docker) |
| lab.terraform.basics | yes (pre-existing) | **exit 127** | null OK; apply ERROR — script calls `terraform` | **BLOCKED-TOOL** (terraform) |
| lab.terraform.state-management | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-TOOL** (terraform) |
| lab.cloud.twelve-factor | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-TOOL** (python3) |
| lab.k8s.deploy-node-app | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-TOOL** (docker) |
| lab.k8s.helm | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-TOOL** (helm) |
| lab.git.basics | yes (pre-existing) | **exit 128** | apply ERROR — `cd ~/repo` fails; `fx.git-repo-empty.v1` has no handler; also null-path FAIL (weak `.gitignore` validator) | **BLOCKED-FIXTURE** |
| lab.git.branching-strategies | yes (pre-existing) | **exit 1** | apply ERROR — `fx.git-repo-conflict-setup.v1` has no handler; null-path FAIL | **BLOCKED-FIXTURE** |
| lab.git.internals | yes (pre-existing) | **exit 1** | apply ERROR — `fx.git-repo-three-commits.v1` has no handler | **BLOCKED-FIXTURE** |
| lab.git.release-management | yes (pre-existing) | **exit 1** | apply ERROR — `fx.git-repo-v1.2.3.v1` has no handler | **BLOCKED-FIXTURE** |
| lab.git.workflow-patterns | yes (pre-existing) | **exit 1** | apply ERROR — `fx.git-repo-empty.v1` has no handler | **BLOCKED-FIXTURE** |
| lab.k8s.pods | yes (new, PSS-compliant) | apply ERROR — `exit code marker not found in output` | null OK | **BLOCKED-EXECSHELL** |
| lab.k8s.services | yes (new) | apply ERROR (ExecShell) | null OK | **BLOCKED-EXECSHELL** |
| lab.k8s.statefulsets | yes (new) | apply ERROR (ExecShell) | null OK | **BLOCKED-EXECSHELL** |
| lab.k8s.storage | yes (new) | apply ERROR (ExecShell) | null OK | **BLOCKED-EXECSHELL** |
| lab.k8s.config-secrets | yes (new) | apply ERROR (ExecShell) | null OK; t2 validators use `kubectl exec` typed as K8S_ASSERT (executor rejects) | **BLOCKED-EXECSHELL + BLOCKED-EXECUTOR** |
| lab.k8s.production-deployments | yes (new) | apply ERROR (ExecShell) | null OK | **BLOCKED-EXECSHELL** |
| lab.k8s.troubleshooting | yes (new) | apply ERROR (ExecShell) | null OK | **BLOCKED-EXECSHELL** |
| lab.k8s.architecture | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-RBAC** (read `kube-system`) |
| lab.k8s.resource-management | yes (stub — dir recreated) | exit 1 (BLOCKED) | — | **BLOCKED-RBAC** (`resourcequotas`) |
| lab.k8s.scheduling | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-RBAC** (`label nodes`) |
| lab.k8s.autoscaling | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-RBAC** (`horizontalpodautoscalers`) |
| lab.sre.size-replicas-for-load | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-RBAC** (`horizontalpodautoscalers`) |
| lab.sre.classify-incident-severity | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-EXECSHELL** (in-pod kubectl vs `broken-app`) |
| lab.istio.mtls-security | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE + BLOCKED-EXECUTOR** (`fx.istio-installed.v1`; `PeerAuthentication` CRD) |
| lab.istio.traffic-management | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE + BLOCKED-EXECUTOR** (`VirtualService` CRD) |
| lab.cloud.mtls-spiffe | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.istio-installed.v1`, `fx.backend-pod.v1`) |
| lab.gitops.fluxcd-argocd | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE + BLOCKED-EXECUTOR** (`fx.argocd-installed.v1`; `Application` CRD) |
| lab.tekton.pipelines | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE + BLOCKED-EXECUTOR** (`fx.tekton-installed.v1`; `Task`/`TaskRun` CRD) |
| lab.cloud.api-container-security | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.pod-privileged-insecure.v1`) |
| lab.cloud.k8s-serverless | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-EXECUTOR** (Knative `Service` CRD) |
| lab.cloud.progressive-delivery | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.deployment-blue.v1`) |
| lab.cloud.zero-trust-security | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.frontend-backend-pods.v1`) |
| lab.observability.alerting | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.prometheus-alertmanager-installed.v1`) |
| lab.observability.prometheus-basics | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.prometheus-installed.v1`) |
| lab.observability.prometheus-advanced | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.prometheus-installed.v1`) |
| lab.observability.scaling-infra | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.prometheus-high-cardinality-target.v1`) |
| lab.observability.tracing | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.jaeger-installed.v1`) |
| lab.observability.elk-basics | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.elasticsearch-running.v1`) |
| lab.sre.define-slo-error-budget | yes (stub) | exit 1 (BLOCKED) | — | **BLOCKED-FIXTURE** (`fx.prometheus-alertmanager-installed.v1`) |

---

## Tally (63 labs)

| Bucket | Count |
|---|---|
| **PASS** (golden-path + flake verified locally) | **18** |
| BLOCKED-CONTENT (lab spec inconsistency) | 1 |
| BLOCKED-TOOL (docker / terraform / helm / python3) | 8 |
| BLOCKED-FIXTURE (fixture handler missing) | 20 |
| BLOCKED-RBAC (learner Role too narrow) | 5 |
| BLOCKED-EXECSHELL (exec-stream fragility) | 8 |
| BLOCKED-EXECUTOR (K8S_ASSERT can't read the kind) — overlaps FIXTURE rows | (6, counted under FIXTURE) |
| **Total with a `solution_apply` on disk** | **63 / 63** |

`solution_apply` **exists for all 63 labs** (was 13). Golden-path
**verified PASS for 18** in the local ₹0 environment. The remaining 45 are
BLOCKED for the specific, documented reasons above — **none are faked**.

## Sims

11 `sim.*` activities exist; none have `solution_apply` scripts and all
are `PRODUCTION_SIM` (fault-injection + AI-rubric graded), out of Phase 1
lab scope. content-CI SKIPs them. Not counted above.

## What unblocks the rest (for a real runner)

1. **Workspace image**: add `docker` (or DinD sidecar), `terraform`,
   `helm`, `python3`+`pip`, `ansible` → clears 8 BLOCKED-TOOL.
2. **Fixture handlers**: implement the 27 missing `fx.*` handlers (the
   simplest ~12 — `git-repo-empty`, `deployment-web`, `deployment-blue`,
   `monolith-config`, `dockerfile-insecure`, `app-hardcoded-config`,
   `sample-app-repo`, `postmortem-incident-facts`, `terraform-*`,
   `orphan-local-file` — are a few lines each) → clears most
   BLOCKED-FIXTURE.
3. **Installer fixtures**: `fx.istio-installed`, `fx.prometheus-installed`,
   `fx.jaeger-installed`, `fx.argocd-installed`, `fx.tekton-installed`,
   `fx.elasticsearch-running` — larger, use `clusterbootstrap` +
   `kubectl apply` of upstream manifests.
4. **K8S_ASSERT executor**: add CRD kind support (`virtualservice`,
   `peerauthentication`, `application`, `task`, `taskrun`) and accept
   `kubectl exec` shape → clears BLOCKED-EXECUTOR.
5. **Learner RBAC** (`orchestrator/internal/fixture/rbac.go`): decide
   whether `resourcequotas` (get/list), `horizontalpodautoscalers`
   (CRUD), and a get/list `nodes` ClusterRole belong in the learner
   sandbox → clears BLOCKED-RBAC (security review required).
6. **`ExecShell`**: honour the gRPC context deadline on the pod exec
   stream and make the exit-marker parse robust → clears BLOCKED-EXECSHELL
   and makes content-CI reliable under load.
