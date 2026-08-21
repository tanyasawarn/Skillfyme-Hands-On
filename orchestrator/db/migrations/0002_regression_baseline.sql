-- Doc §7.3 step 3 (BASELINE SNAPSHOT): "Capture full resource inventory +
-- health matrix. This is the NO_REGRESSION reference and the 'what did
-- they break' diff source." Distinct from contracts/orchestrator.proto's
-- Snapshot RPC (filesystem/workspace tarball for suspend/resume, Phase 3
-- project mode) -- same word, different concept: this is a point-in-time
-- K8s health-state capture used only for the NO_REGRESSION validator
-- type, not a resumable workspace artifact.
CREATE TABLE IF NOT EXISTS env.regression_baseline (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  environment_id    uuid NOT NULL REFERENCES env.environment(id),
  snapshot_key      text NOT NULL,       -- content-authored label, e.g. "baseline.other-services" (doc §7.3 worked example)
  health_matrix     jsonb NOT NULL,      -- captured resource inventory + health per doc §7.3 step 3
  captured_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (environment_id, snapshot_key)
);

CREATE INDEX IF NOT EXISTS idx_regression_baseline_env ON env.regression_baseline (environment_id);
