---
case_id: overbuilt-for-constraints
constraints_id: cs.web-api-tight-budget
adversarial: false
sme_scores:
  constraint_fit: 1
  failure_mode_reasoning: 2
  tradeoff_honesty: 2
  data_model_soundness: 2
  overall: 2
notes: >
  Same activity + constraints as strong-constrained-design ($40/mo, 2
  engineers, 30 req/s). This design reaches for Kubernetes, a service
  mesh, Kafka and multi-region — every one of which contradicts the
  budget and team-size constraints. constraint_fit is a clear 1. It is
  recognisable as a genuine attempt and the data model is at least
  plausible, so overall is a 2 not a 1. Failure modes and trade-offs are
  named but generic and not tied to the (wrong) choices.
---

# Design — URL shortener platform

## Context and constraints

Budget $40/mo, 2 engineers, ~30 req/s peak, single-region acceptable,
p95 < 400 ms.

## Component choices

- **Amazon EKS cluster** running the API as a Deployment with an HPA
  (3–20 pods) for scalability.
- **Istio service mesh** for mTLS, retries and observability between
  services.
- **Managed Kafka (MSK)** so the create path is fully asynchronous and we
  can add consumers later.
- **Aurora PostgreSQL global database**, primary in us-east-1 with a
  read replica in us-west-2 for high availability and low-latency reads.
- **CloudFront** in front of the ALB for global edge caching.

## Data model

`links(id UUID PK, slug UNIQUE, target_url, created_at, owner_id)` plus a
`click_events(id, link_id, ts, ip_hash)` table for analytics. Slug lookups
hit a unique index; click events are append-only.

## Failure modes

- A pod can crash — Kubernetes restarts it.
- The database could go down — the read replica takes over.
- Kafka could be unavailable — messages are retried.
- A region could fail — traffic shifts to the other region.

## Trade-offs considered

- Kubernetes adds operational complexity but gives us scalability and
  portability.
- A service mesh adds latency but improves security and observability.
- Multi-region costs more but improves availability.
