---
case_id: reasonable-but-debatable
constraints_id: cs.event-ingest-mid-scale
adversarial: false
sme_scores:
  constraint_fit: 3
  failure_mode_reasoning: 3
  tradeoff_honesty: 3
  data_model_soundness: 3
  overall: 3
notes: >
  Different activity: an event-ingestion service, constraints are
  ~2,000 events/s sustained, $600/mo ceiling, 4 engineers, 24h data
  retention, at-least-once delivery acceptable. This design (ALB →
  stateless writers → SQS → workers → Timescale) is a reasonable
  starting point; no choice is clearly wrong, but SQS-vs-Kinesis and
  Timescale-vs-plain-Postgres are both debatable at this scale and the
  design does not fully justify them. Solid, unremarkable — a 3 across
  the board.
---

# Design — Event ingestion service

## Context and constraints

~2,000 events/s sustained (bursty to ~5,000/s), $600/month ceiling,
4 engineers, 24-hour retention, at-least-once delivery is acceptable,
consumers are internal dashboards.

## Component choices

- **Stateless HTTP writers** on Fargate (2–6 tasks) behind an ALB. They
  validate the event envelope and enqueue; they hold no state so scaling
  is linear.
- **SQS standard queue** between writers and workers. Standard (not FIFO)
  because at-least-once is acceptable and FIFO caps throughput at 300
  msg/s/group without batching. Decouples ingest spikes from write
  throughput.
- **Worker pool** on Fargate (2–8 tasks) draining SQS in batches of 10,
  writing to the store.
- **TimescaleDB on a single RDS-sized EC2 instance** (`r6g.large`), 24h
  retention via a drop-chunk policy.

## Data model

Hypertable `events(ts, source, type, payload jsonb)` partitioned by time
into 1-hour chunks. The only read pattern is dashboard aggregation queries
over the last 1–24h grouped by `source`/`type`, which time-partitioning
serves well; the 24h retention is a cheap `drop_chunks` call. `payload` is
jsonb because event shapes vary by `type` and dashboards only filter on
`source`/`type`, never inside the payload.

## Failure modes

- **Worker pool falls behind during a burst:** SQS depth grows; events
  are not lost (retention on the queue is 4 days). Dashboards lag by the
  queue drain time. Mitigation: CloudWatch alarm on `ApproximateAgeOf
  OldestMessage` > 120 s scales the worker pool.
- **Timescale instance down:** writers keep accepting and enqueuing (SQS
  buffers); workers back off and retry. No data loss within the 4-day
  queue retention. In-flight worker batches are returned to the queue by
  the visibility-timeout expiry and redelivered — hence the at-least-once
  requirement.
- **Writer task dies mid-request:** client gets a 502 and retries; the
  event may be enqueued twice (at-least-once), deduped downstream by
  event id.

## Trade-offs considered

- **SQS over Kinesis** — simpler and cheaper at this scale, but we give
  up ordered per-shard replay and a longer retention window. Revisit if a
  consumer needs strict ordering or > 4 days of replay.
- **Single Timescale instance** — no HA; a failure means dashboard data
  is delayed (not lost) until it recovers. Revisit when the ingest rate
  passes ~5,000/s sustained or an availability SLA is attached, at which
  point Timescale multi-node or a managed columnar store is warranted.
- **jsonb payload** — flexible but unindexed; if a dashboard ever needs
  to filter inside the payload this becomes a scan. Accepted because no
  such requirement is stated.
