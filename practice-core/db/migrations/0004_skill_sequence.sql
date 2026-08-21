-- Skill-driven catalog (requested) needs skills to render in curriculum
-- teaching order, not alphabetically -- domain/name sort put "DevOps with
-- AI" (module 7, last taught) above "DevOps Core Concepts" (module 1,
-- first taught) since 'aiops' < 'core' alphabetically. domain_order ranks
-- the 7 curriculum modules in their actual sequence; sequence ranks
-- skills within a domain in teaching order. Both are plain integers set
-- once by the seed script, not derived/computed, since the curriculum's
-- order is authored content, not something the DB can infer.
ALTER TABLE skill.skill ADD COLUMN IF NOT EXISTS domain_order integer NOT NULL DEFAULT 0;
ALTER TABLE skill.skill ADD COLUMN IF NOT EXISTS sequence integer NOT NULL DEFAULT 0;
