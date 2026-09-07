-- Phase 3 (PLAN.md Phase 3 / PLAN_PHASE3_PROJECTS.md 3.2-3.3). Dev A's env schema.
--
-- The T3 tier (TIER_T3_CLOUD_ACCOUNT) provisions a workspace pod on the
-- platform cluster PLUS a claimed sandbox account. env.environment
-- already records the pod side (namespace, tier, attempt_id); these two
-- columns record the account side so the Snapshot / Destroy RPC handlers
-- can recover the account id and the in-pod Terraform root without a
-- cross-schema join.
--
-- Additive + idempotent, same as 0001-0005. Applied by the compose
-- `db-migrate-orchestrator` one-shot.

ALTER TABLE env.environment
  ADD COLUMN IF NOT EXISTS cloud_account_id text;

ALTER TABLE env.environment
  ADD COLUMN IF NOT EXISTS tf_workspace_dir text NOT NULL DEFAULT '/workspace';

-- Lookup: "which account backs this T3 env" (Snapshot/Destroy handlers).
CREATE INDEX IF NOT EXISTS idx_environment_cloud_account
  ON env.environment (cloud_account_id)
  WHERE cloud_account_id IS NOT NULL;
