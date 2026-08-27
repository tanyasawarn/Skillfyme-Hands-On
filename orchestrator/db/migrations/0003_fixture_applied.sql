-- PLAN.md M1.3: "Idempotent, ordered, checksummed fixture application
-- step in the provisioning pipeline" (doc §5.5 step 3). Records which
-- (environment, fixture, checksum) triples have already been applied so
-- a retried Provision() call (or a future re-seed-in-place operation)
-- can skip a fixture already in its target state -- the "idempotent"
-- half. See orchestrator/internal/fixture's AppliedTracker interface.

CREATE TABLE IF NOT EXISTS env.fixture_applied (
  environment_id  uuid NOT NULL REFERENCES env.environment(id),
  fixture_id      text NOT NULL,
  checksum        text NOT NULL DEFAULT '',
  applied_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (environment_id, fixture_id)
);
