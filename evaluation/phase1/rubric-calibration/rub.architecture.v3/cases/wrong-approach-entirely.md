---
case_id: wrong-approach-entirely
constraints_id: cs.event-ingest-mid-scale
adversarial: false
sme_scores:
  constraint_fit: 1
  failure_mode_reasoning: 1
  tradeoff_honesty: 1
  data_model_soundness: 1
  overall: 1
notes: >
  Event-ingestion activity (~2,000 events/s sustained, 24h retention,
  at-least-once). This design proposes synchronous writes straight into a
  single spreadsheet-style table with a per-request full-table read to
  dedupe, and no buffering. It cannot serve the stated load, the data
  model cannot serve the described reads, and a competent engineer would
  reject the core approach. 1 across the board.
---

# Design — Event ingestion

## Context and constraints

Needs to accept events and store them for a day.

## Component choices

A single API server (one EC2 instance) that, for each incoming event,
runs `SELECT * FROM events` to check whether the event already exists,
and if not, `INSERT`s it. If the instance gets busy we can make it a
bigger instance.

## Data model

One table `events(data TEXT)` holding the whole event as a JSON string.
To find things we scan the table and parse each row.

## Failure modes

If the server is down, events are dropped. The client can send them again
later if it wants.

## Trade-offs considered

Keeping everything in one place makes it easy to understand.
