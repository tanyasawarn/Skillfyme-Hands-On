---
case_id: strong-constrained-design
constraints_id: cs.web-api-tight-budget
adversarial: false
sme_scores:
  constraint_fit: 4
  failure_mode_reasoning: 3
  tradeoff_honesty: 4
  data_model_soundness: 3
  overall: 4
notes: >
  Every component choice is tied to a specific stated constraint, the
  design names what it gives up and when to revisit, and the data model
  follows from the described access pattern. Failure-mode section covers
  blast radius + in-flight work but stops short of a component-specific
  non-obvious mode, so failure_mode_reasoning is a 3 not a 4.
---

# Design — URL shortener API

## Context and constraints

Stated constraints for this activity: budget ceiling **$40/month**,
**2 engineers**, expected peak **~30 req/s** (mostly reads), single-region
is acceptable, p95 latency target **< 400 ms**, no PII stored.

## Component choices

- **One AWS Fargate service** (0.25 vCPU / 0.5 GB, 1–2 tasks) behind an
  Application Load Balancer. Fargate over EC2 because with two engineers we
  do not want to patch hosts; over Lambda because the read path benefits
  from a warm in-process cache and Lambda cold starts risk the 400 ms p95.
- **RDS `db.t4g.micro` PostgreSQL**, single-AZ. Single-AZ because Multi-AZ
  doubles the DB cost and blows the $40 ceiling; the activity accepts
  single-region and a few minutes of recovery time.
- **No message broker, no Redis.** At 30 req/s a managed broker (~$15/mo
  minimum) plus a cache node would exceed budget, and the workload has no
  async step that needs a queue. In-process LRU cache (10k entries) on the
  read path instead.

## Data model

Single table `links(slug PK, target_url, created_at)`. Access patterns are
(1) point lookup by `slug` on redirect — the hot path, ~28 of the 30 req/s —
and (2) insert on create. A relational store is justified only because we
already need RDS for durability and transactions on create; a KV store
would serve the lookup equally well but adds a second system for two
engineers to run. `slug` is the PK so the redirect is a primary-key point
read. No secondary indexes needed.

## Failure modes

- **RDS unavailable (failover / maintenance):** redirects for cached slugs
  keep working from the in-process LRU; uncached slugs and all creates
  return 503. In-flight create requests fail and the client must retry —
  acceptable, creates are rare and non-urgent. Recovery: automatic on RDS
  coming back; nothing to do.
- **Fargate task dies:** the ALB drains it and routes to the other task;
  in-flight requests on that task get a 502 and are retried by the client.
  If both tasks are down, full outage until the service scales back up
  (~60 s). We run minimum 2 tasks specifically so a single task death is
  not an outage.
- **Cache stampede on deploy:** every new task starts cold, so a deploy
  briefly pushes all reads to RDS. At 30 req/s the micro instance handles
  it (measured p95 ~110 ms cold), so no mitigation beyond rolling deploys.

## Trade-offs considered

- **Single-AZ RDS** — we give up automatic DB failover to save ~$13/mo.
  Revisit when the budget ceiling rises above ~$70/mo or an SLA is
  attached.
- **No queue** — creates are synchronous against RDS. Fine at this write
  rate; revisit if writes exceed ~50/s or we add an async enrichment step.
- **In-process cache, not Redis** — cache is lost on every deploy and not
  shared across tasks. Revisit when read load triples (~90 req/s) or task
  count exceeds ~4, at which point a shared cache pays for itself.
