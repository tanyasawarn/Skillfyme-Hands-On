-- Doc §8.4, §4.2 (event model), §2.4 (BKT). Phase 1 scope: guided labs only.

CREATE TABLE IF NOT EXISTS attempt.attempt (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES learner.tenant(id),
  user_id               uuid NOT NULL REFERENCES learner.user_account(id),
  activity_id           uuid NOT NULL REFERENCES content.activity(id),
  activity_version_id   uuid NOT NULL REFERENCES content.activity_version(id), -- never null, never updated (§8.4 "relationships that matter most")
  mode                  text NOT NULL,
  status                text NOT NULL DEFAULT 'CREATED'
                          CHECK (status IN (
                            'CREATED','PROVISIONING','READY','IN_PROGRESS','SUBMITTED','EVALUATING',
                            'PASSED','FAILED','COMPLETED','PROVISION_FAILED','SUSPENDED','EVAL_FAILED',
                            'EXPIRED','ABANDONED'
                          )),
  retry_of_attempt_id   uuid REFERENCES attempt.attempt(id),
  retry_index           integer NOT NULL DEFAULT 0,
  assistance_flags      text[] NOT NULL DEFAULT '{}',
  environment_id        text,               -- Dev A's opaque environment_id, no FK across service boundary
  idempotency_key       text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  started_at            timestamptz,
  submitted_at          timestamptz,
  completed_at          timestamptz,
  expires_at            timestamptz,
  active_seconds        integer NOT NULL DEFAULT 0,
  reset_count           integer NOT NULL DEFAULT 0,
  hint_penalty_total    numeric(5,4) NOT NULL DEFAULT 0,
  version               integer NOT NULL DEFAULT 1  -- optimistic concurrency, §4.4
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_attempt_idempotency
  ON attempt.attempt (user_id, activity_version_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_attempt_user_status
  ON attempt.attempt (user_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_attempt_version_status
  ON attempt.attempt (activity_version_id, status)
  WHERE status IN ('IN_PROGRESS','SUSPENDED');

CREATE TABLE IF NOT EXISTS attempt.attempt_task_state (
  attempt_id          uuid NOT NULL REFERENCES attempt.attempt(id),
  task_key            text NOT NULL,
  status               text NOT NULL DEFAULT 'PENDING',
  first_pass_at        timestamptz,
  attempts_count       integer NOT NULL DEFAULT 0,
  hints_used_max_level integer NOT NULL DEFAULT 0,
  skipped               boolean NOT NULL DEFAULT false,
  assisted              boolean NOT NULL DEFAULT false,
  PRIMARY KEY (attempt_id, task_key)
);

-- §4.2 append-only, partitioned by month. seq assigned by the writer (this service).
-- Note: doc §4.2 states PK(attempt_id, seq), but Postgres requires every unique/PK
-- constraint on a partitioned table to include the partition key column. occurred_at
-- is added to the key for that reason; (attempt_id, seq) alone is enforced by the
-- application (the writer already serialises seq assignment per attempt) and can be
-- backed by a separate non-unique index if a query needs it without occurred_at.
CREATE TABLE IF NOT EXISTS attempt.attempt_events (
  id            bigserial,
  attempt_id    uuid NOT NULL,
  seq           bigint NOT NULL,
  occurred_at   timestamptz NOT NULL,
  actor         text NOT NULL CHECK (actor IN ('LEARNER','SYSTEM','VALIDATOR','AI','ADMIN')),
  type          text NOT NULL,
  payload       jsonb NOT NULL,
  PRIMARY KEY (attempt_id, seq, occurred_at)
) PARTITION BY RANGE (occurred_at);

-- Phase 1 bootstrap partitions; a real partition-management job replaces this later.
CREATE TABLE IF NOT EXISTS attempt.attempt_events_default
  PARTITION OF attempt.attempt_events DEFAULT;

CREATE INDEX IF NOT EXISTS idx_attempt_events_time_brin
  ON attempt.attempt_events USING brin (occurred_at);

-- Ordered replay per attempt (doc §4.4: "write a replay tool in week one").
CREATE INDEX IF NOT EXISTS idx_attempt_events_replay
  ON attempt.attempt_events (attempt_id, seq);

CREATE TABLE IF NOT EXISTS attempt.validation_run (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  attempt_id   uuid NOT NULL REFERENCES attempt.attempt(id),
  scope        text NOT NULL,          -- "task:t3" | "all"
  trigger      text NOT NULL,          -- "manual" | "auto_debounce" | "submit"
  started_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz,
  status       text NOT NULL DEFAULT 'RUNNING'
);

CREATE TABLE IF NOT EXISTS attempt.validator_result (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  validation_run_id   uuid NOT NULL REFERENCES attempt.validation_run(id),
  validator_id        text NOT NULL,
  status              text NOT NULL CHECK (status IN ('PASS','FAIL','ERROR','SKIP')),
  observed            jsonb,
  expected            jsonb,
  duration_ms         integer,
  evidence_ref        text
);

CREATE INDEX IF NOT EXISTS idx_validator_result_flake
  ON attempt.validator_result (validator_id, status);

-- §6.4: scoring reads signals, never raw events.
CREATE TABLE IF NOT EXISTS attempt.attempt_signal (
  attempt_id   uuid NOT NULL REFERENCES attempt.attempt(id),
  signal_key   text NOT NULL,
  value_num    numeric,
  value_jsonb  jsonb,
  computed_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (attempt_id, signal_key)
);

CREATE TABLE IF NOT EXISTS attempt.attempt_score (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  attempt_id                  uuid NOT NULL REFERENCES attempt.attempt(id),
  profile_version_id          text NOT NULL,
  criterion_fn_versions_jsonb jsonb NOT NULL,
  final_score                 numeric(5,4) NOT NULL,
  passed                      boolean NOT NULL,
  breakdown_jsonb             jsonb NOT NULL,
  penalties_jsonb             jsonb NOT NULL DEFAULT '{}'::jsonb,
  computed_at                 timestamptz NOT NULL DEFAULT now(),
  rescored_from_id            uuid REFERENCES attempt.attempt_score(id),
  rescore_reason               text
);

CREATE TABLE IF NOT EXISTS attempt.artifact (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  attempt_id    uuid NOT NULL REFERENCES attempt.attempt(id),
  kind          text NOT NULL,
  storage_uri   text NOT NULL,
  bytes         bigint,
  checksum      text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  retain_until  timestamptz
);

-- §2.4 BKT mastery + §2.6 Elo.
CREATE TABLE IF NOT EXISTS skill.skill_mastery (
  user_id          uuid NOT NULL REFERENCES learner.user_account(id),
  skill_id         uuid NOT NULL REFERENCES skill.skill(id),
  p_mastery        numeric(6,5) NOT NULL,
  last_evidence_at timestamptz,
  evidence_count   integer NOT NULL DEFAULT 0,
  elo_rating       numeric(7,2),
  review_due_at    timestamptz,
  band             text,
  PRIMARY KEY (user_id, skill_id)
);

-- Doc §8.4 specifies a partial index "WHERE review_due_at < now()", but Postgres
-- rejects now() in an index predicate (not IMMUTABLE). A plain btree on the column
-- serves the same "review-due" query (WHERE review_due_at < now()) via a range scan;
-- functionally equivalent for this table's size (small: one row per learner*skill).
CREATE INDEX IF NOT EXISTS idx_skill_mastery_review_due
  ON skill.skill_mastery (review_due_at);

CREATE TABLE IF NOT EXISTS skill.mastery_evidence (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES learner.user_account(id),
  skill_id     uuid NOT NULL REFERENCES skill.skill(id),
  attempt_id   uuid NOT NULL REFERENCES attempt.attempt(id),
  delta        numeric(6,5) NOT NULL,
  p_before     numeric(6,5) NOT NULL,
  p_after      numeric(6,5) NOT NULL,
  weight       numeric(4,3) NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS learner.learner_activity_state (
  user_id          uuid NOT NULL REFERENCES learner.user_account(id),
  activity_id      uuid NOT NULL REFERENCES content.activity(id),
  best_score       numeric(5,4),
  latest_score     numeric(5,4),
  status            text NOT NULL DEFAULT 'not_started',
  attempts_count    integer NOT NULL DEFAULT 0,
  last_attempt_at   timestamptz,
  cooldown_until    timestamptz,
  PRIMARY KEY (user_id, activity_id)
);

CREATE TABLE IF NOT EXISTS learner.learner_elo (
  user_id       uuid NOT NULL REFERENCES learner.user_account(id),
  skill_domain  text NOT NULL,
  rating        numeric(7,2) NOT NULL DEFAULT 1200,
  matches       integer NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, skill_domain)
);

-- §2.5 recommendation, rules-only for Phase 1.
CREATE TABLE IF NOT EXISTS learner.recommendation (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id             uuid NOT NULL REFERENCES learner.user_account(id),
  activity_id         uuid NOT NULL REFERENCES content.activity(id),
  score                numeric(6,4) NOT NULL,
  features_jsonb       jsonb NOT NULL DEFAULT '{}'::jsonb,
  reason_code          text NOT NULL,
  reason_params_jsonb  jsonb NOT NULL DEFAULT '{}'::jsonb,
  generated_at         timestamptz NOT NULL DEFAULT now(),
  shown_at             timestamptz,
  clicked_at           timestamptz,
  started_at           timestamptz,
  dismissed_at         timestamptz,
  ranker_version        text NOT NULL DEFAULT 'rules-v1'
);

CREATE INDEX IF NOT EXISTS idx_recommendation_user_time
  ON learner.recommendation (user_id, generated_at DESC);
