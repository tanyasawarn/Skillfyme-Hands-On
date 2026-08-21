-- Doc §8.4, §2.2, §2.3. Dev B owns: content, learner, attempt, skill, admin schemas.
-- Phase 1 scope only (guided labs, DevOps track) — no project/milestone/AI tables yet (Phase 3/4).

CREATE TABLE IF NOT EXISTS learner.tenant (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL,
  plan        text NOT NULL DEFAULT 'default',
  settings    jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS learner.user_account (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES learner.tenant(id),
  email       text NOT NULL,
  role        text NOT NULL DEFAULT 'learner',
  status      text NOT NULL DEFAULT 'active',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);

CREATE TABLE IF NOT EXISTS content.course (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES learner.tenant(id),
  slug        text NOT NULL,
  title       text NOT NULL,
  status      text NOT NULL DEFAULT 'draft',
  UNIQUE (tenant_id, slug)
);

CREATE TABLE IF NOT EXISTS content.module (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  course_id   uuid NOT NULL REFERENCES content.course(id),
  title       text NOT NULL,
  position    integer NOT NULL
);

CREATE TABLE IF NOT EXISTS content.topic (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  module_id   uuid NOT NULL REFERENCES content.module(id),
  title       text NOT NULL,
  position    integer NOT NULL
);

CREATE TABLE IF NOT EXISTS content.subtopic (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  topic_id    uuid NOT NULL REFERENCES content.topic(id),
  title       text NOT NULL,
  position    integer NOT NULL
);

-- §2.1 D5: skill graph is a separate structure, small (300-600 skills target).
CREATE TABLE IF NOT EXISTS skill.skill (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug                  text NOT NULL UNIQUE,
  name                  text NOT NULL,
  domain                text NOT NULL,
  description           text,
  decay_half_life_days  integer NOT NULL DEFAULT 180,
  bkt_p_init            numeric(4,3) NOT NULL DEFAULT 0.15,
  bkt_p_transit         numeric(4,3) NOT NULL DEFAULT 0.25,
  bkt_p_slip            numeric(4,3) NOT NULL DEFAULT 0.10,
  bkt_p_guess           numeric(4,3) NOT NULL DEFAULT 0.08,
  status                text NOT NULL DEFAULT 'active'
);

-- §2.3 edge semantics table: REQUIRES/BUILDS_ON/SIBLING/SPECIALIZES/SUPERSEDES
CREATE TABLE IF NOT EXISTS skill.skill_edge (
  from_skill_id  uuid NOT NULL REFERENCES skill.skill(id),
  to_skill_id    uuid NOT NULL REFERENCES skill.skill(id),
  type           text NOT NULL CHECK (type IN ('REQUIRES','BUILDS_ON','SIBLING','SPECIALIZES','SUPERSEDES')),
  strength       numeric(4,3) NOT NULL DEFAULT 1.0,
  PRIMARY KEY (from_skill_id, to_skill_id, type)
);

-- §2.2 materialised closure, rebuilt synchronously on skill-graph publish.
CREATE TABLE IF NOT EXISTS skill.skill_closure (
  ancestor_id    uuid NOT NULL REFERENCES skill.skill(id),
  descendant_id  uuid NOT NULL REFERENCES skill.skill(id),
  depth          integer NOT NULL,
  edge_types     text[] NOT NULL,
  PRIMARY KEY (ancestor_id, descendant_id)
);

CREATE TABLE IF NOT EXISTS content.topic_skill (
  topic_id         uuid NOT NULL REFERENCES content.topic(id),
  skill_id         uuid NOT NULL REFERENCES skill.skill(id),
  coverage_weight  numeric(4,3) NOT NULL,
  bloom_level      text NOT NULL,
  PRIMARY KEY (topic_id, skill_id)
);

-- §8.4 core entities: activity / activity_version (immutable once PUBLISHED).
CREATE TABLE IF NOT EXISTS content.activity (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES learner.tenant(id),
  slug        text NOT NULL,
  mode        text NOT NULL CHECK (mode IN ('GUIDED_LAB','PRODUCTION_SIM','PROJECT')),
  owner_id    uuid REFERENCES learner.user_account(id),
  status      text NOT NULL DEFAULT 'active',
  retired_at  timestamptz,
  UNIQUE (tenant_id, slug)
);

CREATE TABLE IF NOT EXISTS content.activity_version (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  activity_id                 uuid NOT NULL REFERENCES content.activity(id),
  version                     integer NOT NULL,
  semver_kind                 text CHECK (semver_kind IN ('PATCH','MINOR','MAJOR')),
  status                      text NOT NULL DEFAULT 'DRAFT'
                                CHECK (status IN ('DRAFT','IN_REVIEW','APPROVED','PUBLISHED','CANARY','DEPRECATED','RETIRED','ROLLED_BACK')),
  spec_jsonb                  jsonb NOT NULL,
  blueprint_id                text,
  blueprint_version            text,
  scoring_profile_id          text,
  scoring_profile_version     text,
  difficulty_level            text CHECK (difficulty_level IN ('L1','L2','L3','L4','L5')),
  difficulty_elo              numeric(7,2),
  estimated_minutes           integer,
  cost_budget_usd             numeric(8,4),
  canary_pct                  numeric(5,2) NOT NULL DEFAULT 0,
  published_at                timestamptz,
  published_by                uuid REFERENCES learner.user_account(id),
  created_at                  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (activity_id, version)
);

-- §3.6 rule 11: editing a PUBLISHED version is impossible, enforced at DB level.
CREATE OR REPLACE FUNCTION content.reject_published_edit() RETURNS trigger AS $$
BEGIN
  IF OLD.status = 'PUBLISHED' THEN
    RAISE EXCEPTION 'activity_version % is PUBLISHED and immutable (doc §3.6 rule 11)', OLD.id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_activity_version_immutable ON content.activity_version;
CREATE TRIGGER trg_activity_version_immutable
  BEFORE UPDATE ON content.activity_version
  FOR EACH ROW EXECUTE FUNCTION content.reject_published_edit();

CREATE TABLE IF NOT EXISTS content.activity_skill (
  activity_version_id  uuid NOT NULL REFERENCES content.activity_version(id),
  skill_id             uuid NOT NULL REFERENCES skill.skill(id),
  weight                numeric(4,3) NOT NULL,
  is_primary            boolean NOT NULL DEFAULT false,
  bloom_level           text,
  PRIMARY KEY (activity_version_id, skill_id)
);

CREATE TABLE IF NOT EXISTS content.activity_topic (
  activity_version_id  uuid NOT NULL REFERENCES content.activity_version(id),
  topic_id             uuid NOT NULL REFERENCES content.topic(id),
  relevance             numeric(4,3) NOT NULL DEFAULT 1.0,
  PRIMARY KEY (activity_version_id, topic_id)
);

-- Indexes per §8.4 indexing strategy table (subset relevant to Phase 1).
CREATE INDEX IF NOT EXISTS idx_activity_version_published
  ON content.activity_version (activity_id, version DESC)
  WHERE status = 'PUBLISHED';

CREATE INDEX IF NOT EXISTS idx_activity_version_spec_gin
  ON content.activity_version USING gin (spec_jsonb);

CREATE INDEX IF NOT EXISTS idx_skill_closure_descendant
  ON skill.skill_closure (descendant_id, ancestor_id);
