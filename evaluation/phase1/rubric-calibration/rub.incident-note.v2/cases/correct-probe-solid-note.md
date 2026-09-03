---
case_id: correct-probe-solid-note
constraints_id: gt.readiness-probe-too-aggressive
adversarial: false
sme_scores:
  root_cause_accuracy: 4
  completeness: 3
  prevention_quality: 3
  overall: 3
notes: >
  Root cause is a clean 4: identifies the aggressive readiness-probe
  timeout, explains the endpoint-flapping mechanism, and rules out the
  container actually being unhealthy (tested with a manual curl at a
  normal timeout). Completeness 3 — all sections present, prevention
  section is a touch thin. Prevention proposes a concrete change (floor
  on probe timeoutSeconds in an admission policy) tied to the cause but
  doesn't spell out how it catches THIS case -> 3. Overall 3.
---

# Incident note — INC-7033: intermittent checkout 503s

## Root cause

The `checkout` container's readinessProbe was configured with
`timeoutSeconds: 1` and `periodSeconds: 2`. The endpoint being probed
(`/healthz`) normally responds in ~700–950ms under load — close enough
to the 1s timeout that a fraction of probes time out. Each failed probe
removed the pod from the Service's endpoint list; the next successful
probe added it back. With all three replicas flapping independently,
the Service intermittently had 0–2 endpoints, producing the 503s.
Confirmed the container itself was healthy by `kubectl exec` +
`curl -m 5 localhost:8080/healthz` — consistently 200 at a 5s timeout.

## Detection

Users reported intermittent "service unavailable" on checkout;
`sum(rate(http_requests_total{code="503"}[1m]))` for the checkout route
showed a sawtooth, not a step.

## Timeline

- 11:40 — 503 reports; 11:47 — endpoint count graph shows flapping;
  11:52 — probe config identified as the cause; 11:58 — probe
  `timeoutSeconds` raised to 5, `periodSeconds` to 10; 12:03 — 503s stop.

## Remediation

Set the checkout readinessProbe to `timeoutSeconds: 5`,
`periodSeconds: 10`, `failureThreshold: 3`.

## Prevention

Add an admission policy that rejects any readinessProbe with
`timeoutSeconds < 3` on a container whose health endpoint's own p95 is
above 500ms.
