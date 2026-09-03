-- Phase 3 (PLAN_PHASE3_PROJECTS.md 0.11 + A4). Dev A's env schema.
--
-- The Account Pool Manager (Stage 2.4, orchestrator/internal/accountpool)
-- maintains a warm pool of vended AWS sandbox accounts and moves each one
-- through AVAILABLE -> IN_USE -> NUKING -> (AVAILABLE | QUARANTINED) as
-- attempts claim and release them (memory.md §5.3). This table is that
-- pool's durable state; the fast claim path (SPop-style CAS) is in Redis,
-- same split as internal/warmpool, but the authoritative record and the
-- quarantine queue live here.
--
-- Ownership: env schema = Dev A. This migration lives under
-- orchestrator/db/migrations and is applied by the compose
-- `db-migrate-orchestrator` one-shot (idempotent: every statement is
-- IF NOT EXISTS / additive), same as 0001-0004.
--
-- NOTE: `usage_meter.cloud_cost_usd` already exists (0001_env_and_billing.sql
-- line ~53), so the "add cloud_cost_usd column" half of 0.11 was already
-- satisfied; this migration only adds the cloud_account table.

CREATE TABLE IF NOT EXISTS env.cloud_account (
  -- The real AWS account id (12 digits). Natural key -- one row per vended
  -- account for the life of the pool.
  aws_account_id     text PRIMARY KEY,

  -- AVAILABLE : clean, nuked-and-verified, ready to claim. Holds no billable
  --             resources, so an idle pool is near-zero cost.
  -- IN_USE    : claimed by exactly one attempt (see attempt_id). Baseline
  --             Terraform applied, STS role assumable, budget alarm armed.
  -- NUKING    : release in progress -- aws-nuke running, or the mandatory
  --             post-nuke verification pass running. Never claimable.
  -- QUARANTINED: verification found leftover resources, or nuke errored, or a
  --             budget breach left it in an unknown state. Held for a human;
  --             NEVER returned to the pool automatically. Emits
  --             ACCOUNT_QUARANTINED (pages on-call).
  state              text NOT NULL DEFAULT 'AVAILABLE'
                       CHECK (state IN ('AVAILABLE', 'IN_USE', 'NUKING', 'QUARANTINED')),

  -- Which of the SCP-allowed regions this account's baseline is pinned to.
  -- The pool is sized per-region (memory.md §10.5: "multi-region pools").
  region             text NOT NULL,

  -- Set while state = IN_USE (and kept as the "last holder" after release so
  -- sweeper-time quarantine still has an attempt_id for the event envelope).
  -- No FK -- attempt lives in Dev B's `attempt` schema, across the service
  -- boundary (same rule as env.environment.attempt_id).
  attempt_id         uuid,

  -- The per-account AWS Budgets alarm ceiling set at claim time, from
  -- activity_spec.environment.cost_budget_usd. Null when AVAILABLE.
  budget_usd         numeric(10, 2),

  -- Exceptions the expensive-SKU SCP is conditioned on for this claim
  -- (activity_spec.environment.cloud.sku_exceptions). Written as an account
  -- tag at claim time; mirrored here for the pool manager's own view.
  sku_exceptions     text[] NOT NULL DEFAULT '{}',

  -- Lifecycle timestamps.
  claimed_at         timestamptz,
  released_at        timestamptz,
  last_nuked_at      timestamptz,

  -- Populated only while state = QUARANTINED: why, and how many resources the
  -- verification pass still found. `quarantine_detail` is human-facing runbook
  -- text -- never raw credentials (same stance as env.audit_log.detail).
  quarantine_reason  text
                       CHECK (quarantine_reason IS NULL OR quarantine_reason IN (
                         'verification_nonempty', 'nuke_error', 'budget_breach_stuck', 'sweeper', 'manual'
                       )),
  quarantine_resources_remaining integer,
  quarantine_detail  text,

  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);

-- Hot query 1: the pool manager's claim path -- "give me an AVAILABLE
-- account in region X". Partial index keeps it off the IN_USE / QUARANTINED
-- rows entirely.
CREATE INDEX IF NOT EXISTS idx_cloud_account_available
  ON env.cloud_account (region)
  WHERE state = 'AVAILABLE';

-- Hot query 2: "everything for one attempt" (incident investigation, cost
-- attribution -- one account = one attempt, memory.md §10.3).
CREATE INDEX IF NOT EXISTS idx_cloud_account_attempt
  ON env.cloud_account (attempt_id)
  WHERE attempt_id IS NOT NULL;

-- Hot query 3: the human quarantine queue.
CREATE INDEX IF NOT EXISTS idx_cloud_account_quarantined
  ON env.cloud_account (updated_at)
  WHERE state = 'QUARANTINED';

-- Belt-and-braces invariants the state machine must uphold (a claimed
-- account has an attempt + budget; an AVAILABLE one does not).
ALTER TABLE env.cloud_account
  DROP CONSTRAINT IF EXISTS ck_cloud_account_in_use_has_attempt;
ALTER TABLE env.cloud_account
  ADD CONSTRAINT ck_cloud_account_in_use_has_attempt
  CHECK (state <> 'IN_USE' OR (attempt_id IS NOT NULL AND budget_usd IS NOT NULL));

ALTER TABLE env.cloud_account
  DROP CONSTRAINT IF EXISTS ck_cloud_account_available_is_clean;
ALTER TABLE env.cloud_account
  ADD CONSTRAINT ck_cloud_account_available_is_clean
  CHECK (state <> 'AVAILABLE' OR (attempt_id IS NULL AND budget_usd IS NULL AND quarantine_reason IS NULL));
