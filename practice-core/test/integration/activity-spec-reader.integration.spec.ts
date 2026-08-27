import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { ActivitySpecReader } from '../../src/common/activity-spec-reader';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md Phase 4's S5: ActivitySpecReader.getActivitySpec(attemptId),
 * consolidating the attempt->activity_version->spec_jsonb join that was
 * independently rewritten in hint.service.ts, artifact.service.ts (x2),
 * and command-executed.consumer.ts. Lives in common/ (not on
 * AttemptRepository) specifically to avoid a circular module dependency
 * between AttemptModule and EvaluationModule -- see the class's own doc
 * comment.
 */
describe('ActivitySpecReader.getActivitySpec (integration, real Postgres)', () => {
  let db: Kysely<Database>;
  let specReader: ActivitySpecReader;
  let catalog: CatalogRepository;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    specReader = new ActivitySpecReader(db);
    catalog = new CatalogRepository(db);
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
    tenantId = tenant.id;
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'learner@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    userId = user.id;
    await db
      .insertInto('skill.skill')
      .values({ slug: 'test.skill', name: 'Test', domain: 'test' })
      .execute();
  });

  it('returns the real ActivitySpec for a real attempt, joined through its activity_version', async () => {
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.spec-test',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.spec-test',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.02 },
        skills: [{ skill: 'test.skill', weight: 1.0, primary: true }],
      },
    });
    const attempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: userId,
        activity_id: version.activity_id,
        activity_version_id: version.id,
        mode: 'GUIDED_LAB',
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    const spec = await specReader.getActivitySpec(attempt.id);

    expect(spec).toBeDefined();
    expect(spec?.id).toBe('lab.spec-test');
    expect(spec?.environment.blueprint).toBe('bp.test.v1');
  });

  it('returns undefined for a nonexistent attempt (not a throw) -- callers that need a hard failure wrap this in findOrThrow themselves', async () => {
    const spec = await specReader.getActivitySpec(
      '00000000-0000-0000-0000-000000000000',
    );
    expect(spec).toBeUndefined();
  });
});
