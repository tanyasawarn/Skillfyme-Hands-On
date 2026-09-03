---
case_id: wrong-rootcause-plausible-prose
constraints_id: gt.oomkilled-memory-limit
adversarial: false
sme_scores:
  root_cause_accuracy: 1
  completeness: 3
  prevention_quality: 2
  overall: 2
notes: >
  This is the case the rubric exists for: well-written, all five
  sections present, confident tone — but the stated root cause (a
  connection-pool leak in application code) is FLATLY WRONG against
  ground truth (memory-limit-too-low -> OOMKilled). root_cause_accuracy
  MUST be 1: it names the wrong area entirely. Completeness is a real 3
  (all sections, some thin). Prevention names a category ("add memory
  profiling") without a concrete tie to the actual cause -> 2. Overall 2:
  a fluent note that would mislead a reader about what actually happened.
  If a grader gives root_cause_accuracy >= 3 here, calibration FAILS —
  it means the grader is rewarding prose over correctness.
---

# Incident note — INC-4471: checkout pods restarting

## Root cause

The checkout service has a connection-pool leak. Under sustained load
the service opens database connections faster than it closes them; each
leaked connection holds a buffer, and over ~40 seconds the accumulated
buffers exhaust the container's available memory, causing the process
to be killed and restarted. The 40-second restart period corresponds to
the time it takes the leak to consume the container's memory headroom
at current request rates.

## Detection

Availability alerting caught the drop below 99%. Restart-count metrics
confirmed a regular restart cadence.

## Timeline

- 14:02 — alert fires
- 14:06 — on-call begins investigation
- 14:15 — identified the restart pattern as memory-driven
- 14:20 — restarted the deployment to clear accumulated state
- 14:25 — service recovered

## Remediation

Rolled the checkout deployment to clear the leaked connections and give
the service a fresh memory state. Service recovered immediately.

## Prevention

Add memory profiling to the checkout service so connection-pool growth
is visible before it causes an outage. Consider a periodic restart as a
stopgap until the leak is fixed in code.
