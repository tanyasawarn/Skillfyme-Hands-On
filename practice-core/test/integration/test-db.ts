import { Kysely, PostgresDialect, sql } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../../src/db/schema';

/**
 * Real Postgres connection for integration tests, against the
 * docker-compose instance. Default port is 5433, not 5432 -- see
 * memory/docker-compose.yml comment: host 5432 is commonly taken by a
 * native Postgres install, so this project's compose file remaps to 5433.
 *
 * Defaults to practice_engine_test, a dedicated database (see
 * db/migrations/README or scripts/create-test-db.sh), NOT the shared
 * practice_engine database every other service and every developer's
 * browser session points at. This used to default to practice_engine
 * itself -- truncateAll() below wiped the shared demo tenant, skill
 * graph, and full activity catalog (twice, confirmed live) simply from
 * running this suite normally, since nothing distinguished "the test
 * database" from "the database." TEST_DATABASE_URL lets CI or a
 * developer point this elsewhere explicitly; DATABASE_URL is
 * deliberately NOT read here anymore, precisely because that's the same
 * env var practice-core's own app reads for the real database, and an
 * inherited/copy-pasted shell env is exactly how this kept happening.
 */
const DEFAULT_TEST_DATABASE_URL =
  'postgres://practice:practice@localhost:5433/practice_engine_test';

export function createTestDb(): Kysely<Database> {
  const connectionString =
    process.env.TEST_DATABASE_URL ?? DEFAULT_TEST_DATABASE_URL;
  assertNotSharedDatabase(connectionString);
  const pool = new Pool({ connectionString });
  return new Kysely<Database>({ dialect: new PostgresDialect({ pool }) });
}

/**
 * Last-resort guard: even if TEST_DATABASE_URL is misconfigured to point
 * at the real database (a copy-pasted DATABASE_URL, a shared shell env),
 * refuse outright rather than silently truncating it. The two incidents
 * this fixes both happened via exactly that kind of inherited env value,
 * not a deliberate override -- a loud failure here is strictly better
 * than a quiet one discovered by the whole platform's catalog vanishing.
 */
function assertNotSharedDatabase(connectionString: string): void {
  if (/\/practice_engine(\?|$)/.test(connectionString)) {
    throw new Error(
      `Refusing to run integration tests against practice_engine (the shared, non-test database): ${connectionString}\n` +
        `Set TEST_DATABASE_URL to a dedicated test database instead (default: ${DEFAULT_TEST_DATABASE_URL}).`,
    );
  }
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
