-- Phase 3 1.5 / B8. The analytics schema: the canonical attempt_events
-- stream ingested from NATS, plus hourly rollups (attempt → activity →
-- course → tenant → global — memory.md line 1851).
--
-- Applied by the ClickHouse container's initdb on first boot; the same
-- file is applied to the Cloud service by cloud/apply-schema.sh.

CREATE DATABASE IF NOT EXISTS practice_analytics;

-- Raw event stream. One row per attempt_events row in Postgres; the B8
-- consumer fans out from the NATS `attempt_events` subject (no
-- point-to-point — line 1463) and inserts here.
CREATE TABLE IF NOT EXISTS practice_analytics.attempt_events
(
    attempt_id      String,
    tenant_id       String,
    activity_id     String,
    course_id       String,
    user_id         String,
    seq             UInt64,
    type            LowCardinality(String),
    actor           LowCardinality(String),
    occurred_at     DateTime64(3, 'UTC'),
    payload         String,                      -- raw JSON, parsed lazily
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (tenant_id, activity_id, attempt_id, seq)
TTL toDateTime(occurred_at) + INTERVAL 400 DAY;   -- generous; OLTP Postgres stays source of truth

-- Hourly rollup at the attempt grain. AggregatingMergeTree so the
-- consumer just INSERTs event rows and the view maintains the aggregate.
CREATE TABLE IF NOT EXISTS practice_analytics.attempt_hourly
(
    hour            DateTime('UTC'),
    tenant_id       String,
    course_id       String,
    activity_id     String,
    attempt_id      String,
    events          AggregateFunction(count, UInt64),
    milestones_gated AggregateFunction(countIf, UInt8),
    defence_msgs    AggregateFunction(countIf, UInt8),
    last_event_at   AggregateFunction(max, DateTime64(3, 'UTC'))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, course_id, activity_id, attempt_id, hour);

CREATE MATERIALIZED VIEW IF NOT EXISTS practice_analytics.attempt_hourly_mv
TO practice_analytics.attempt_hourly AS
SELECT
    toStartOfHour(occurred_at)                             AS hour,
    tenant_id,
    course_id,
    activity_id,
    attempt_id,
    countState(seq)                                        AS events,
    countIfState(type = 'MILESTONE_GATED')                 AS milestones_gated,
    countIfState(type = 'DEFENCE_MESSAGE')                 AS defence_msgs,
    maxState(occurred_at)                                  AS last_event_at
FROM practice_analytics.attempt_events
GROUP BY hour, tenant_id, course_id, activity_id, attempt_id;

-- Convenience view: the attempt rollup with the aggregate functions
-- merged, for the admin analytics queries (B10).
CREATE VIEW IF NOT EXISTS practice_analytics.attempt_hourly_merged AS
SELECT
    hour, tenant_id, course_id, activity_id, attempt_id,
    countMerge(events)            AS events,
    countIfMerge(milestones_gated) AS milestones_gated,
    countIfMerge(defence_msgs)    AS defence_msgs,
    maxMerge(last_event_at)       AS last_event_at
FROM practice_analytics.attempt_hourly
GROUP BY hour, tenant_id, course_id, activity_id, attempt_id;
