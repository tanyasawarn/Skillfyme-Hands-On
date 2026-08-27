-- Doc §2.6: "difficulty_elo is the measured difficulty, recalibrated
-- nightly from attempt outcomes." Doc §3.6 rule 11's own trade-off note
-- (line 733) frames content immutability as *protecting* Elo calibration
-- from corruption, not as blocking it -- difficulty_elo is a derived
-- measurement, not authored spec content, so it was never meant to be
-- covered by the same immutability guarantee as spec_jsonb. The original
-- trigger (migration 0001) didn't carve out this exception; this
-- narrows it to allow exactly one column (difficulty_elo) to change on
-- an otherwise immutable PUBLISHED row -- any other column changing on a
-- PUBLISHED row still raises, unchanged from the original behaviour.
CREATE OR REPLACE FUNCTION content.reject_published_edit() RETURNS trigger AS $$
BEGIN
  IF OLD.status = 'PUBLISHED' THEN
    IF NEW.id                      IS DISTINCT FROM OLD.id
    OR NEW.activity_id             IS DISTINCT FROM OLD.activity_id
    OR NEW.version                 IS DISTINCT FROM OLD.version
    OR NEW.semver_kind             IS DISTINCT FROM OLD.semver_kind
    OR NEW.status                  IS DISTINCT FROM OLD.status
    OR NEW.spec_jsonb              IS DISTINCT FROM OLD.spec_jsonb
    OR NEW.blueprint_id            IS DISTINCT FROM OLD.blueprint_id
    OR NEW.blueprint_version       IS DISTINCT FROM OLD.blueprint_version
    OR NEW.scoring_profile_id      IS DISTINCT FROM OLD.scoring_profile_id
    OR NEW.scoring_profile_version IS DISTINCT FROM OLD.scoring_profile_version
    OR NEW.difficulty_level        IS DISTINCT FROM OLD.difficulty_level
    OR NEW.estimated_minutes       IS DISTINCT FROM OLD.estimated_minutes
    OR NEW.cost_budget_usd         IS DISTINCT FROM OLD.cost_budget_usd
    OR NEW.canary_pct              IS DISTINCT FROM OLD.canary_pct
    OR NEW.published_at            IS DISTINCT FROM OLD.published_at
    OR NEW.published_by            IS DISTINCT FROM OLD.published_by
    OR NEW.created_at              IS DISTINCT FROM OLD.created_at
    THEN
      RAISE EXCEPTION 'activity_version % is PUBLISHED and immutable (doc §3.6 rule 11)', OLD.id;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
