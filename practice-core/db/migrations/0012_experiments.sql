-- PLAN.md G11 / doc §11.4: "Version and A/B everything that is a
-- judgement call: recommendation ranker weights, hint ladder wording,
-- difficulty defaults, idle timeouts, mentor personas. Assignment at
-- the learner level, sticky, logged on every recommendation and attempt."
--
-- experiment: the registry of running experiments + their variants and
--   traffic split. `unit` is always 'learner' for now (doc §11.4).
-- experiment_assignment: the sticky learner->variant record. Written
--   once on first exposure, read on every subsequent one. A hash-based
--   assignment is deterministic, but persisting it means changing the
--   split later never re-buckets already-enrolled learners.

CREATE TABLE IF NOT EXISTS admin.experiment (
  key            text PRIMARY KEY,          -- e.g. 'ranker_weights', 'mentor_persona'
  description    text NOT NULL DEFAULT '',
  unit           text NOT NULL DEFAULT 'learner' CHECK (unit IN ('learner')),
  -- variants_jsonb: [{ "name": "control", "weight": 50 }, { "name": "v2", "weight": 50 }]
  variants_jsonb jsonb NOT NULL,
  status         text NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('DRAFT','RUNNING','CONCLUDED')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  concluded_at   timestamptz
);

CREATE TABLE IF NOT EXISTS admin.experiment_assignment (
  experiment_key text NOT NULL REFERENCES admin.experiment(key),
  user_id        uuid NOT NULL REFERENCES learner.user_account(id),
  variant        text NOT NULL,
  assigned_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (experiment_key, user_id)
);

CREATE INDEX IF NOT EXISTS idx_experiment_assignment_variant
  ON admin.experiment_assignment (experiment_key, variant);
