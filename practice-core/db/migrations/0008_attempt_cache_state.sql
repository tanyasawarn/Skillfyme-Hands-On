-- Lab quota/history requirement: 72h-inactive attempts move to a soft
-- CACHED state (not deleted) instead of staying "active" forever, and
-- reactivate on the next user click. last_activity_at tracks the most
-- recent learner-driven touch (start/connect/file write/etc.) separately
-- from created_at, which the 72h sweep needs and nothing existing tracks.

ALTER TABLE attempt.attempt
  ADD COLUMN IF NOT EXISTS last_activity_at timestamptz;

UPDATE attempt.attempt
  SET last_activity_at = COALESCE(started_at, created_at)
  WHERE last_activity_at IS NULL;

ALTER TABLE attempt.attempt
  ALTER COLUMN last_activity_at SET DEFAULT now(),
  ALTER COLUMN last_activity_at SET NOT NULL;

ALTER TABLE attempt.attempt DROP CONSTRAINT IF EXISTS attempt_status_check;
ALTER TABLE attempt.attempt ADD CONSTRAINT attempt_status_check
  CHECK (status IN (
    'CREATED','PROVISIONING','READY','IN_PROGRESS','SUBMITTED','EVALUATING',
    'PASSED','FAILED','COMPLETED','PROVISION_FAILED','SUSPENDED','EVAL_FAILED',
    'EXPIRED','ABANDONED','CACHED'
  ));

CREATE INDEX IF NOT EXISTS idx_attempt_cache_sweep
  ON attempt.attempt (last_activity_at)
  WHERE status IN ('CREATED','PROVISIONING','READY','IN_PROGRESS');
