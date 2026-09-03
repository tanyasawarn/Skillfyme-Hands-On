-- PLAN.md C16 / memory.md §883: learner-facing GDPR controls.
--
-- "export (all attempts, snapshots, feedback as an archive) and
--  deletion (anonymise the attempt, purge workspace and transcripts,
--  retain aggregate counters). Build these in Phase 1 -- retrofitting
--  deletion across an event-sourced system with object-storage
--  snapshots is genuinely painful."
--
-- Export needs no schema change (it's a read). Deletion (erasure) needs
-- an audit marker so a re-run is a no-op and support can see a subject
-- was erased without keeping their identity:
--
--   * learner.user_account.erased_at   -- the account was anonymised
--   * attempt.attempt.erased_at        -- this attempt's PII-bearing
--                                         children (event payloads,
--                                         artifact bodies) were redacted
--
-- What erasure does NOT delete (memory.md §883 "retain aggregate
-- counters"): the attempt rows themselves (kept, user_id-anonymised via
-- the user_account row), learner.learner_activity_state (best_score /
-- status / counts -- no free text, no identity beyond the now-anonymised
-- user_id FK), and attempt.attempt_score breakdowns (scoring math, no
-- learner-authored text). Analytics (§11.3) stay correct.

ALTER TABLE learner.user_account
  ADD COLUMN IF NOT EXISTS erased_at timestamptz;

ALTER TABLE attempt.attempt
  ADD COLUMN IF NOT EXISTS erased_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_attempt_erased_at
  ON attempt.attempt (erased_at)
  WHERE erased_at IS NOT NULL;
