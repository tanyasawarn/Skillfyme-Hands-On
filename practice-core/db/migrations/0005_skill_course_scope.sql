-- Second independent course requested (GenAI with ML), launched from an
-- external LMS as its own learning track alongside DevOps With AI. The
-- skill graph so far has no notion of "which course" a domain belongs
-- to -- domain/domain_order/sequence only encode ordering WITHIN one
-- course's curriculum. Without course_slug, two courses whose curricula
-- happen to both use a domain name like "core" would collide in the
-- same catalog listing. Backfill sets every existing (DevOps) skill to
-- 'devops-with-ai' so nothing already loaded silently becomes
-- unscoped/ambiguous.
ALTER TABLE skill.skill ADD COLUMN IF NOT EXISTS course_slug text NOT NULL DEFAULT 'devops-with-ai';

CREATE INDEX IF NOT EXISTS idx_skill_course_slug ON skill.skill (course_slug);
