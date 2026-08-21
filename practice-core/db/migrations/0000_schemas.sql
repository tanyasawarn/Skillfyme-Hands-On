-- Phase 0 deliverable #4 (doc §8.4): schema-per-bounded-context, created empty.
-- Ownership per PLAN.md "Cross-cutting ownership":
--   env, billing        -> Dev A (migrated from /orchestrator/db)
--   content, learner,
--   attempt, skill,
--   admin               -> Dev B (migrated from here, /practice-core/db)
--
-- Nobody edits another dev's schema migrations. Dev A adds columns to
-- attempt-adjacent tables only via new migration files in their own tree,
-- never by editing files under practice-core/db.

CREATE SCHEMA IF NOT EXISTS content;
CREATE SCHEMA IF NOT EXISTS learner;
CREATE SCHEMA IF NOT EXISTS attempt;
CREATE SCHEMA IF NOT EXISTS skill;
CREATE SCHEMA IF NOT EXISTS admin;

-- env and billing are Dev A's, declared here only so the empty-skeleton
-- exit criteria (Phase 0) is satisfiable from a single init pass in local
-- dev. Dev A owns all migrations that alter these from here on.
CREATE SCHEMA IF NOT EXISTS env;
CREATE SCHEMA IF NOT EXISTS billing;
