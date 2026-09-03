# Phase 2 F — Content status

## F.1 — First 30 faults: each has ≥ 1 simulation activity

**33 of 35 faults now have a sim activity** (was 28).

Added this pass:

| New sim activity | Faults it covers | Tier |
|---|---|---|
| `sim.k8s.scheduling-autoscaling-incident` | `f.k8s.taint-blocks-scheduling`, `f.k8s.hpa-metrics-unavailable` | T1 |
| `sim.istio.mesh-and-gitops-incident` | `f.istio.mtls-mode-mismatch`, `f.istio.virtualservice-weight-sum-invalid`, `f.gitops.argocd-out-of-sync-manual-drift` | T2 (Sysbox) |

Both schema-valid against `contracts/activity_spec.schema.json` (74/74
activities pass). Solution scripts (`scripts/t1_apply.sh` etc.) and
`solutions/<id>/` reference-solution dirs still need to be authored
before these run in content-CI's golden path — tracked below.

### The 2 remaining faults with no sim

| Fault | Why no sim | Resolution |
|---|---|---|
| `f.cloud.iam-overpermissive-role` | ARN/trust-policy specific — genuinely needs real AWS IAM, not a K8s stand-in | Phase 3 (AWS account vending). A K8s-RBAC sim would not exercise the actual fault. |
| `f.iam.missing-ecr-pull` | ECR repository-policy specific — same | Phase 3 |

These 2 are **structurally blocked on Phase 3's AWS vending**, not a
content gap this phase can close. Well within the "first 30 faults"
target (33 ≥ 30).

### Fault → sim coverage summary

- 30 faults with a sim, T1-runnable today
- 3 faults with a sim, T2-runnable once the Sysbox cluster is up
  (`sim.istio.mesh-and-gitops-incident` + the 3 istio/argocd handlers
  are real and live-verified per `PHASE2_CLOSEOUT.md`)
- 2 faults AWS-blocked → Phase 3

## F.2 — Second course track (SRE) complete

`course.sre` is **structurally complete**:

| | |
|---|---|
| Seeded skills (`seed-skills-sre.ts`) | 4: `sre.incident-response-process`, `sre.postmortem-authorship`, `sre.slo-error-budgets`, `sre.capacity-and-load` |
| Skills covered by ≥ 1 activity | **4 / 4** |
| Curriculum topics (`seed-curriculum-sre.ts`) | 4: `incident-diagnosis`, `postmortems`, `slo-error-budgets`, `capacity-and-load` |
| Topics with ≥ 1 primary activity | **4 / 4** (incident-diagnosis 2, others 1 each) |
| Total activities on `course.sre` | **11** (4 guided labs + 7 production sims) |

Guided labs: `lab.sre.classify-incident-severity`,
`lab.sre.define-slo-error-budget`, `lab.sre.write-a-postmortem`,
`lab.sre.size-replicas-for-load`.

Production sims: `sim.sre.checkout-latency-incident`,
`sim.cicd.tooling-degraded-incident`,
`sim.k8s.platform-migration-incident`,
`sim.k8s.rollout-stuck-incident`,
`sim.observability.pipeline-blind-spots-incident`,
`sim.k8s.scheduling-autoscaling-incident` (new),
`sim.istio.mesh-and-gitops-incident` (new).

Every SRE skill and every SRE topic has learner-facing coverage — no
partial topic, no orphan skill. Activities are `status: DRAFT` (the
normal pre-publish state); `publish-all-content.ts` flips them on
deploy.

## Remaining content work (not blockers for F's "≥ 1 sim each" gate)

1. `scripts/t1_apply.sh` / `t2_apply.sh` / `t3_apply.sh` solution
   scripts + `solutions/<id>/` dirs for the 2 new sims, so content-CI's
   golden path runs green.
2. `bp.k8s-multinode.v1` blueprint definition (referenced by
   `sim.istio.mesh-and-gitops-incident`) — needs to exist in the
   blueprint registry with the istio/argocd/gitea fixtures wired.
3. Once the Sysbox cluster is up: run `sim.istio.mesh-and-gitops-incident`
   through the B fault-injection harness end-to-end.
