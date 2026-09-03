-- Phase 3 (PLAN_PHASE3_PROJECTS.md 0.10 + B1). Dev B's attempt schema.
--
-- Project mode adds an ordered milestone sequence on top of an attempt
-- (memory.md §12.3): design -> infra -> implementation -> hardening ->
-- final, each gated by its own validator + rubric slice. This migration
-- is the data-model shape only -- the state machine that drives these
-- rows is Stage 1.6 (practice-core project module). Until 1.6 lands,
-- nothing writes these tables, exactly the same "schema ahead of the
-- writer" pattern as 0009_snapshot_stub.sql.
--
-- Ownership: attempt schema = Dev B. Applied by the compose Postgres
-- initdb.d mount (practice-core/db/migrations/*.sql), same as 0000-0009.

-- One row per (attempt, milestone). The milestone_key set mirrors
-- contracts/activity_spec.schema.json's `milestones[].key` enum exactly.
CREATE TABLE IF NOT EXISTS attempt.project_milestone_state (
  attempt_id     uuid NOT NULL REFERENCES attempt.attempt(id),
  milestone_key  text NOT NULL
                   CHECK (milestone_key IN ('design', 'infra', 'implementation', 'hardening', 'final')),

  -- LOCKED     : a blocking earlier milestone has not passed yet.
  -- OPEN       : the learner can work on and submit this milestone.
  -- SUBMITTED  : :submit called; validators + rubric slice running.
  -- GATED_PASS : gate satisfied; the next milestone (if any) becomes OPEN.
  -- GATED_FAIL : gate not satisfied; learner may resubmit (subject to the
  --              activity's retry policy). A blocking GATED_FAIL keeps the
  --              next milestone LOCKED.
  status         text NOT NULL DEFAULT 'LOCKED'
                   CHECK (status IN ('LOCKED', 'OPEN', 'SUBMITTED', 'GATED_PASS', 'GATED_FAIL')),

  -- Ordinal position in the sequence (0 = design). Denormalised from the
  -- spec so "what's the next milestone" is a cheap ORDER BY without
  -- re-reading spec_jsonb.
  ordinal        integer NOT NULL,

  -- Set once the milestone reaches a terminal gate outcome for the
  -- CURRENT submission. Null while LOCKED/OPEN/SUBMITTED.
  submitted_at   timestamptz,
  gated_at       timestamptz,

  -- Milestone score contribution in [0,1], present once GATED_*. Feeds
  -- sp.project.default's milestone-weighted roll-up (Stage 3.9).
  score          numeric(5, 4),

  -- The rubric slice level reached (1-5), present when this milestone's
  -- gate involves a rubric (design, and any milestone with gate
  -- RUBRIC_MIN_LEVEL / BOTH).
  rubric_level   integer CHECK (rubric_level IS NULL OR rubric_level BETWEEN 1 AND 5),

  attempt_count  integer NOT NULL DEFAULT 0,

  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (attempt_id, milestone_key)
);

CREATE INDEX IF NOT EXISTS idx_project_milestone_attempt_ordinal
  ON attempt.project_milestone_state (attempt_id, ordinal);

-- The "which milestones are still open across all learners" instructor
-- view (memory.md line 1948: "learners stuck >X days on a project
-- milestone").
CREATE INDEX IF NOT EXISTS idx_project_milestone_open
  ON attempt.project_milestone_state (milestone_key, updated_at)
  WHERE status IN ('OPEN', 'SUBMITTED');

-- One row per milestone submission -- the point-in-time pointer at the
-- learner's platform-hosted Git repo (PLAN_PHASE3_PROJECTS.md B9) that
-- the milestone's validators ran against, and that the milestone-5 viva
-- generator reads commit history from. Append-only: a resubmission
-- writes a new row (attempt_number increments), it does not update the
-- prior one -- same audit stance as attempt_events.
CREATE TABLE IF NOT EXISTS attempt.project_submission (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  attempt_id     uuid NOT NULL REFERENCES attempt.attempt(id),
  milestone_key  text NOT NULL
                   CHECK (milestone_key IN ('design', 'infra', 'implementation', 'hardening', 'final')),

  -- Opaque handle to the learner's repo on the platform Git host
  -- (e.g. "forgejo:learners/<user>/<project>"). No FK -- the Git host is
  -- infra, not a table.
  repo_ref       text NOT NULL,

  -- The exact commit the validators ran against. 40-hex, or empty for the
  -- `design` milestone if the design doc was submitted out-of-repo (the
  -- state machine decides; the column allows it).
  commit_sha     text NOT NULL DEFAULT '',

  attempt_number integer NOT NULL DEFAULT 1,

  -- Snapshot of the gate outcome for this specific submission, so the
  -- history is self-contained even after project_milestone_state moves on.
  outcome        text CHECK (outcome IS NULL OR outcome IN ('GATED_PASS', 'GATED_FAIL')),
  score          numeric(5, 4),

  submitted_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_project_submission_attempt_milestone
  ON attempt.project_submission (attempt_id, milestone_key, submitted_at DESC);
