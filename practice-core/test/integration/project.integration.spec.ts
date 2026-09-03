import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { ProjectRepository } from '../../src/modules/project/project.repository';
import { createTestDb, truncateAll } from './test-db';

/**
 * Phase 3 (0.10 / B1). ProjectRepository against real Postgres --
 * exercises db/migrations/0010_project_mode.sql: milestone seeding,
 * the LOCKED->OPEN cascade on GATED_PASS, the append-only submission
 * history, and the schema-level CHECK constraints.
 */
describe('ProjectRepository (integration, real Postgres) — Phase 3 project mode', () => {
  let db: Kysely<Database>;
  let repo: ProjectRepository;
  let attemptId: string;

  const SEQUENCE = [
    'design',
    'infra',
    'implementation',
    'hardening',
    'final',
  ] as const;

  beforeAll(() => {
    db = createTestDb();
    repo = new ProjectRepository(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'test-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenant.id, email: 'learner@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenant.id, slug: 'proj.test', mode: 'PROJECT' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const version = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: activity.id,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {},
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    const attempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenant.id,
        user_id: user.id,
        activity_id: activity.id,
        activity_version_id: version.id,
        mode: 'PROJECT',
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    attemptId = attempt.id;
  });

  async function seedFullSequence(): Promise<void> {
    await repo.seedMilestones(
      attemptId,
      SEQUENCE.map((key, i) => ({
        milestoneKey: key,
        ordinal: i,
        status: i === 0 ? ('OPEN' as const) : ('LOCKED' as const),
      })),
    );
  }

  it('seeds the milestone sequence with the first OPEN and the rest LOCKED, in ordinal order', async () => {
    await seedFullSequence();
    const rows = await repo.listMilestones(attemptId);
    expect(rows.map((r) => r.milestone_key)).toEqual([...SEQUENCE]);
    expect(rows.map((r) => r.status)).toEqual([
      'OPEN',
      'LOCKED',
      'LOCKED',
      'LOCKED',
      'LOCKED',
    ]);
    expect(rows.map((r) => r.ordinal)).toEqual([0, 1, 2, 3, 4]);
  });

  it('seedMilestones is idempotent (re-seeding does not error or duplicate)', async () => {
    await seedFullSequence();
    await seedFullSequence();
    const rows = await repo.listMilestones(attemptId);
    expect(rows).toHaveLength(5);
  });

  it('markSubmitted moves a milestone to SUBMITTED and bumps attempt_count', async () => {
    await seedFullSequence();
    const n1 = await repo.markSubmitted(attemptId, 'design');
    expect(n1).toBe(1);
    const m = await repo.getMilestone(attemptId, 'design');
    expect(m?.status).toBe('SUBMITTED');
    expect(m?.submitted_at).toBeInstanceOf(Date);
    const n2 = await repo.markSubmitted(attemptId, 'design');
    expect(n2).toBe(2);
  });

  it('GATED_PASS on a milestone opens exactly the next LOCKED milestone', async () => {
    await seedFullSequence();
    await repo.markSubmitted(attemptId, 'design');
    await repo.applyGateOutcome({
      attemptId,
      milestoneKey: 'design',
      outcome: 'GATED_PASS',
      score: 0.82,
      rubricLevel: 4,
    });
    const rows = await repo.listMilestones(attemptId);
    const byKey = Object.fromEntries(rows.map((r) => [r.milestone_key, r]));
    expect(byKey.design.status).toBe('GATED_PASS');
    // pg returns numeric columns as strings (same as `seq` elsewhere in this codebase)
    expect(Number(byKey.design.score)).toBeCloseTo(0.82);
    expect(Number(byKey.design.rubric_level)).toBe(4);
    expect(byKey.infra.status).toBe('OPEN'); // opened
    expect(byKey.implementation.status).toBe('LOCKED'); // untouched
  });

  it('GATED_FAIL does not open the next milestone', async () => {
    await seedFullSequence();
    await repo.markSubmitted(attemptId, 'design');
    await repo.applyGateOutcome({
      attemptId,
      milestoneKey: 'design',
      outcome: 'GATED_FAIL',
      score: 0.3,
      rubricLevel: 2,
    });
    const rows = await repo.listMilestones(attemptId);
    const byKey = Object.fromEntries(rows.map((r) => [r.milestone_key, r]));
    expect(byKey.design.status).toBe('GATED_FAIL');
    expect(byKey.infra.status).toBe('LOCKED');
  });

  it('records an append-only submission history, newest first, and stamps outcomes', async () => {
    await seedFullSequence();

    const n1 = await repo.markSubmitted(attemptId, 'design');
    await repo.recordSubmission({
      attemptId,
      milestoneKey: 'design',
      repoRef: 'forgejo:learners/alice/proj',
      commitSha: 'a'.repeat(40),
      attemptNumber: n1,
    });
    await repo.applyGateOutcome({
      attemptId,
      milestoneKey: 'design',
      outcome: 'GATED_FAIL',
      score: 0.4,
    });
    await repo.stampSubmissionOutcome(
      attemptId,
      'design',
      n1,
      'GATED_FAIL',
      0.4,
    );

    const n2 = await repo.markSubmitted(attemptId, 'design');
    await repo.recordSubmission({
      attemptId,
      milestoneKey: 'design',
      repoRef: 'forgejo:learners/alice/proj',
      commitSha: 'b'.repeat(40),
      attemptNumber: n2,
    });

    const subs = await repo.listSubmissions(attemptId, 'design');
    expect(subs).toHaveLength(2);
    expect(subs[0].commit_sha).toBe('b'.repeat(40)); // newest first
    expect(subs[0].attempt_number).toBe(2);
    expect(subs[0].outcome).toBeNull(); // not yet graded
    expect(subs[1].commit_sha).toBe('a'.repeat(40));
    expect(subs[1].outcome).toBe('GATED_FAIL');
    expect(Number(subs[1].score)).toBeCloseTo(0.4);
  });

  it('rejects an out-of-enum milestone_key at the DB layer', async () => {
    await expect(
      db
        .insertInto('attempt.project_milestone_state')
        .values({
          attempt_id: attemptId,
          // deliberately invalid -- not in the schema CHECK
          milestone_key: 'deployment' as never,
          ordinal: 9,
        })
        .execute(),
    ).rejects.toThrow(
      /project_milestone_state_milestone_key_check|violates check constraint/,
    );
  });

  it('rejects a rubric_level outside 1..5', async () => {
    await seedFullSequence();
    await expect(
      db
        .updateTable('attempt.project_milestone_state')
        .set({ rubric_level: 7 })
        .where('attempt_id', '=', attemptId)
        .where('milestone_key', '=', 'design')
        .execute(),
    ).rejects.toThrow(/rubric_level_check|violates check constraint/);
  });
});
