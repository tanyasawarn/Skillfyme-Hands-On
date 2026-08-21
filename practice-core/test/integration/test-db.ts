import { Kysely, PostgresDialect, sql } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../../src/db/schema';

/**
 * Real Postgres connection for integration tests, against the
 * docker-compose instance. Default port is 5433, not 5432 -- see
 * memory/docker-compose.yml comment: host 5432 is commonly taken by a
 * native Postgres install, so this project's compose file remaps to 5433.
 */
export function createTestDb(): Kysely<Database> {
  const pool = new Pool({
    connectionString:
      process.env.DATABASE_URL ??
      'postgres://practice:practice@localhost:5433/practice_engine',
  });
  return new Kysely<Database>({ dialect: new PostgresDialect({ pool }) });
}

/**
 * Truncates all tables used by tests, cascading, leaving schema/migrations
 * intact. IMPORTANT: this file's jest config (jest-integration.json) pins
 * maxWorkers to 1 deliberately -- every integration spec calls this in
 * beforeEach against the *same* shared database, so running spec files in
 * parallel means one file's truncate can fire mid-transaction of another
 * file's test, producing intermittent, non-deterministic failures (seen
 * and confirmed while building the attempt-lifecycle suite: the full
 * integration run failed under default parallel workers and passed 100%
 * under --runInBand). Do not remove maxWorkers:1 without giving each test
 * file its own database/schema instead.
 */
export async function truncateAll(db: Kysely<Database>): Promise<void> {
  await sql`
    TRUNCATE TABLE
      skill.mastery_evidence, skill.skill_mastery, skill.skill_closure, skill.skill_edge, skill.skill,
      content.activity_skill, content.activity_topic, content.activity_version, content.activity,
      content.topic_skill, content.subtopic, content.topic, content.module, content.course,
      attempt.attempt_score, attempt.attempt_signal, attempt.validator_result, attempt.validation_run,
      attempt.attempt_task_state, attempt.attempt_events, attempt.artifact, attempt.attempt,
      learner.recommendation, learner.learner_elo, learner.learner_activity_state,
      learner.user_account, learner.tenant
    CASCADE
  `.execute(db);
}
