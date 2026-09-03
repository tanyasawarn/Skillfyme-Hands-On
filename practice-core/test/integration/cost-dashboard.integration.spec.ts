import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { CostDashboardService } from '../../src/modules/admin/cost-dashboard.service';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md F14 / doc §10.3, §11.3: the full admin cost dashboard --
 * blended cost (compute + cloud + AI) per learner / course / activity /
 * tenant, from env.usage_meter joined to attempts. This closes the gap
 * where CostDashboardService had no test coverage at all.
 *
 * env.usage_meter is Dev A's schema -- present in the test DB because
 * the orchestrator migrations are applied to it alongside practice-core's
 * (see test-db setup). costByGrain also LEFT JOINs the curriculum so a
 * course-grain rollup needs a topic->module->course chain.
 */
describe('CostDashboardService — blended cost per grain (§10.3)', () => {
  let db: Kysely<Database>;
  let svc: CostDashboardService;

  let tenantId: string;
  let userId: string;
  let activityId: string;
  let activityVersionId: string;

  beforeAll(() => {
    db = createTestDb();
    svc = new CostDashboardService(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    // env.usage_meter has no FK to attempt (attempt_id is text), so a
    // stale row from a prior run could bleed in -- clear it explicitly.
    await db
      .deleteFrom('env.usage_meter' as never)
      .execute()
      .catch(() => {});

    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'cost-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;

    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'costlearner@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    userId = user.id;

    const activity = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug: 'lab.cost', mode: 'GUIDED_LAB' })
      .returningAll()
      .executeTakeFirstOrThrow();
    activityId = activity.id;

    const version = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: activityId,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {},
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    activityVersionId = version.id;

    const attempt = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: userId,
        activity_id: activityId,
        activity_version_id: activityVersionId,
        mode: 'GUIDED_LAB',
      })
      .returningAll()
      .executeTakeFirstOrThrow();

    // Two 60s usage windows for this one attempt: total 0.10, of which
    // cloud 0.04 and AI 0.01 -> compute is the 0.05 remainder.
    for (const [total, cloud, ai] of [
      [0.06, 0.02, 0.01],
      [0.04, 0.02, 0.0],
    ]) {
      await db
        .insertInto('env.usage_meter' as never)
        .values({
          environment_id: 'env-cost-test',
          attempt_id: attempt.id,
          window_start: new Date(Date.now() - 60_000),
          window_end: new Date(),
          total_cost_usd: total,
          cloud_cost_usd: cloud,
          ai_cost_usd: ai,
        })
        .execute();
    }
  });

  it('rolls up blended cost at learner grain', async () => {
    const rows = await svc.costByGrain(tenantId, 'learner', 30);
    expect(rows).toHaveLength(1);
    const r = rows[0];
    expect(r.attempts).toBe(1);
    expect(r.total_cost_usd).toBeCloseTo(0.1, 4);
    expect(r.cloud_cost_usd).toBeCloseTo(0.04, 4);
    expect(r.ai_cost_usd).toBeCloseTo(0.01, 4);
    expect(r.compute_cost_usd).toBeCloseTo(0.05, 4);
  });

  it('rolls up at activity grain', async () => {
    const rows = await svc.costByGrain(tenantId, 'activity', 30);
    expect(rows).toHaveLength(1);
    expect(rows[0].bucket).toBe(activityId);
    expect(rows[0].total_cost_usd).toBeCloseTo(0.1, 4);
  });

  it('excludes windows older than the requested day range', async () => {
    // Move both windows 40 days back, then ask for 30.
    await db
      .updateTable('env.usage_meter' as never)
      .set({
        window_start: new Date(Date.now() - 40 * 24 * 3600_000),
      } as never)
      .execute();
    const rows = await svc.costByGrain(tenantId, 'learner', 30);
    expect(rows).toHaveLength(0);
  });

  it('scopes to the requesting tenant only', async () => {
    const otherTenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'other-cost-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const rows = await svc.costByGrain(otherTenant.id, 'learner', 30);
    expect(rows).toHaveLength(0);
  });
});
