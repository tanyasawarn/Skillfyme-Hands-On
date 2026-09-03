---
case_id: correct-oom-strong-note
constraints_id: gt.oomkilled-memory-limit
adversarial: false
sme_scores:
  root_cause_accuracy: 4
  completeness: 4
  prevention_quality: 4
  overall: 4
notes: >
  Root cause correctly names the OOMKilled mechanism, ties the limit
  value to the observed working set, and cites the exact commands that
  show it (kubectl describe -> Last State: OOMKilled, kubectl top).
  All five sections substantive. Prevention proposes a specific
  pre-merge check (limit >= p95 working set + 30% headroom) AND explains
  it would have caught this. Textbook 4.
---

# Incident note — INC-4471: checkout pods restarting

## Root cause

The `checkout` Deployment's container had `resources.limits.memory` set
to **96Mi**. Under normal traffic the container's working set is
**~180Mi** (measured with `kubectl top pod checkout-*` once it was
briefly stable). Once memory use crossed 96Mi the kernel OOM killer
terminated the process; Kubernetes reported this as `OOMKilled` in
`kubectl describe pod checkout-* | grep -A3 "Last State"` and restarted
the pod, which then repeated the cycle every ~40s.

## Detection

PagerDuty fired on `checkout` availability < 99% for 5m. The
`kube_pod_container_status_restarts_total` graph for the checkout pods
showed a staircase — one restart every ~40s across all three replicas.

## Timeline

- 14:02 — availability alert fires
- 14:06 — on-call ack; `kubectl get pods` shows all checkout pods
  `Running` but `RESTARTS` climbing
- 14:11 — `kubectl describe pod` reveals `Last State: Terminated,
  Reason: OOMKilled`
- 14:14 — `kubectl get deploy checkout -o yaml` shows
  `limits.memory: 96Mi`; git blame on the manifest shows it was changed
  from 256Mi in a "tighten resource requests" PR merged that morning
- 14:19 — `kubectl set resources deploy/checkout --limits=memory=256Mi`;
  pods stabilise, restarts stop
- 14:25 — availability back to 100%, alert clears

## Remediation

Restored the memory limit to 256Mi (the pre-change value). Verified the
working set stays under 200Mi at peak with `kubectl top`.

## Prevention

Add a pre-merge check to the manifest CI: fail the PR if any container's
`limits.memory` is set below `1.3 ×` that container's observed p95
working-set (exported from Prometheus into a checked-in
`resource-baselines.yaml`). This exact incident would have been caught:
the PR set 96Mi against a p95 of ~180Mi, which the check would have
flagged as `96Mi < 1.3 × 180Mi = 234Mi`.
