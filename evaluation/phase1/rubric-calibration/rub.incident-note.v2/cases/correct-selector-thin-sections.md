---
case_id: correct-selector-thin-sections
constraints_id: gt.wrong-service-selector
adversarial: false
sme_scores:
  root_cause_accuracy: 3
  completeness: 3
  prevention_quality: 2
  overall: 3
notes: >
  Root cause correctly identifies the selector/label mismatch and zero
  endpoints — but doesn't explain WHY the healthy pods stopped matching
  (never inspected the diff), so it's a 3 not a 4 (mechanism right,
  causal explanation incomplete). All five sections present but timeline
  and detection are one line each -> completeness 3. Prevention is
  "review Service changes more carefully" with a vague nod to CI -> 2.
  Overall 3: correct and usable, not thorough.
---

# Incident note — INC-5809: checkout unreachable

## Root cause

The `checkout` Service had zero endpoints. `kubectl get endpoints
checkout` showed an empty ADDRESSES list even though `kubectl get pods
-l app=checkout` showed three Running, Ready pods. The Service's
`spec.selector` no longer matched the pods' labels, so kube-proxy had
no backends to route to and every request to the Service ClusterIP
failed immediately.

## Detection

Synthetic checkout probe started returning connection-refused.

## Timeline

- 09:14 — probe alert; 09:20 — found zero endpoints; 09:28 — fixed the
  selector; 09:31 — recovered.

## Remediation

Edited the Service `spec.selector` back to `app: checkout` (it had been
changed to `app: checkout-v2`). Endpoints repopulated within seconds and
the probe recovered.

## Prevention

Review Service selector changes more carefully in code review, and add
a CI check on Service manifests.
