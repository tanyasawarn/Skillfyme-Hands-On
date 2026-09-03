---
case_id: thin-design-missing-sections
constraints_id: cs.web-api-tight-budget
adversarial: false
sme_scores:
  constraint_fit: 2
  failure_mode_reasoning: 1
  tradeoff_honesty: 1
  data_model_soundness: 2
  overall: 2
notes: >
  Same activity/constraints as strong-constrained-design. The choices are
  not crazy (Fargate + RDS is fine for the constraints) so constraint_fit
  scrapes a 2, but there is no real failure-mode reasoning and no
  trade-offs section at all, so both of those are 1. data_model is a bare
  sketch that would probably work — a 2. overall is a 2: recognisable
  attempt, but too underspecified to call a reasonable starting design.
---

# Design — URL shortener

## Context and constraints

$40/mo, small team, ~30 req/s, single region, p95 < 400 ms.

## Component choices

We'll run the API on Fargate behind an ALB and use RDS Postgres for
storage. This is cheap and simple and fits the budget.

## Data model

A `links` table with the slug and the target URL.

## Failure modes

If something breaks we'll get paged and fix it. The ALB will handle a
task going down.

## Trade-offs considered

This is the simplest thing that works.
