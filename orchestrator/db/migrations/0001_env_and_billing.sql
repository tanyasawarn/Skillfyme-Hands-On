-- Dev A's tables in the env/billing schemas (doc §8.4). Schema ownership
-- per PLAN.md "Cross-cutting ownership": env/billing = Dev A;
-- content/learner/attempt/skill/admin = Dev B. Dev A never edits Dev B's
-- migration files under practice-core/db/migrations, and vice versa --
-- this file lives under orchestrator/db/migrations for that reason, even
-- though both apply to the same physical Postgres instance in local dev.

CREATE TABLE IF NOT EXISTS env.environment (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  attempt_id            uuid NOT NULL,
  tier                  text NOT NULL DEFAULT 'T1_SHARED_CONTAINER',
  blueprint_id          text,
  status                text NOT NULL DEFAULT 'PROVISIONING',
  namespace             text NOT NULL,
  ready_at              timestamptz,
  expires_at            timestamptz,
  idle_since            timestamptz,
  destroyed_at          timestamptz,
  destroy_reason        text,
  created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_environment_attempt ON env.environment (attempt_id);
-- Doc §8.4 indexing strategy: "the reaper's hot query -- must be fast
-- and must never seq-scan."
CREATE INDEX IF NOT EXISTS idx_environment_reaper
  ON env.environment (status, expires_at)
  WHERE status NOT IN ('DESTROYED');

-- Doc §5.6: "Every environment is registered in an environment_reaper
-- table with a hard deadline. A reaper job runs every 60s and
-- force-destroys anything past deadline regardless of what the
-- orchestrator thinks."
CREATE TABLE IF NOT EXISTS env.environment_reaper (
  environment_id   uuid PRIMARY KEY REFERENCES env.environment(id),
  namespace        text NOT NULL,
  hard_deadline    timestamptz NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_reaper_deadline ON env.environment_reaper (hard_deadline);

-- Doc §8.4 usage_meter table.
CREATE TABLE IF NOT EXISTS env.usage_meter (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  environment_id    text NOT NULL,
  attempt_id        text NOT NULL,
  window_start      timestamptz NOT NULL,
  window_end        timestamptz NOT NULL,
  cpu_seconds       numeric,
  mem_gb_seconds    numeric,
  storage_gb_hours  numeric,
  egress_gb         numeric,
  cloud_cost_usd    numeric(10,6) NOT NULL DEFAULT 0,
  ai_cost_usd       numeric(10,6) NOT NULL DEFAULT 0,
  total_cost_usd    numeric(10,6) NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_usage_meter_attempt ON env.usage_meter (attempt_id);
CREATE INDEX IF NOT EXISTS idx_usage_meter_window_brin ON env.usage_meter USING brin (window_start);

-- Doc §8.4 budget table -- shared scope types (learner/course/tenant/global).
CREATE TABLE IF NOT EXISTS billing.budget (
  scope_type        text NOT NULL,
  scope_id          text NOT NULL,
  period            text NOT NULL DEFAULT 'daily',
  limit_usd         numeric(10,2) NOT NULL,
  spent_usd         numeric(10,4) NOT NULL DEFAULT 0,
  alert_thresholds  numeric[] NOT NULL DEFAULT ARRAY[0.5, 0.8, 1.0, 1.2],
  PRIMARY KEY (scope_type, scope_id, period)
);
