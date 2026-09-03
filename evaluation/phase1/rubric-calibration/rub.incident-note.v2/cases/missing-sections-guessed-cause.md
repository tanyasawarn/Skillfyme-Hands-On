---
case_id: missing-sections-guessed-cause
constraints_id: gt.egress-proxy-allowlist-too-strict
adversarial: false
sme_scores:
  root_cause_accuracy: 2
  completeness: 1
  prevention_quality: 1
  overall: 1
notes: >
  Only two of five sections have real content (root_cause, remediation).
  completeness MUST be 1 (two or fewer sections). Root cause names the
  right general area ("a network policy problem") but guesses the wrong
  specific mechanism (blames a default-deny on the checkout Service's
  ingress, not the namespace's own missing egress-proxy allow) -> 2.
  Prevention is a pure platitude -> 1. Overall 1: this note does not
  demonstrate the learner understood or resolved the incident.
---

# Incident note — INC-6120: outbound calls timing out

## Root cause

There's a network policy problem. Something is blocking traffic — I
think a default-deny policy on the checkout Service is dropping inbound
connections, which is why calls time out with no response instead of an
error.

## Detection

(not filled in)

## Timeline

(not filled in)

## Remediation

Re-applied the namespace's NetworkPolicy set from the fixture and the
outbound calls started working again.

## Prevention

Be more careful with network policies.
