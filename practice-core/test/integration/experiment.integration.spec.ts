import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { ExperimentService } from '../../src/modules/experiment/experiment.service';
import { NorthStarService } from '../../src/modules/experiment/north-star.service';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md G11 / doc §11.4 -- A/B assignment (sticky, learner-level,
 * deterministic) + the north-star metric (skill-mastery gain per
 * learner-hour) and its guardrails.
 */
describe('ExperimentService + NorthStarService (§11.4, G11)', () => {
  let db: Kysely<Database>;
  let exp: ExperimentService;
  let ns: NorthStarService;

  let tenantId: string;

  beforeAll(() => {
    db = createTestDb();
    exp = new ExperimentService(db);
    ns = new NorthStarService(db);
  });
  afterAll(async () => {
    await db.destroy();
  });

  async function makeUser(email: string) {
    return (
      await db
        .insertInto('learner.user_account')
        .values({ tenant_id: tenantId, email })
        .returningAll()
        .executeTakeFirstOrThrow()
    ).id;
  }

  beforeEach(async () => {
    await truncateAll(db);
    await db.deleteFrom('admin.experiment_assignment' as never).execute().catch(() => {});
    await db.deleteFrom('admin.experiment' as never).execute().catch(() => {});
    await db.deleteFrom('env.usage_meter' as never).execute().catch(() => {});
    const t = await db
      .insertInto('learner.tenant')
      .values({ name: 'exp-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = t.id;
  });

  it('assign() returns "control" for an unknown experiment (default behavior)', async () => {
    const u = await makeUser('a@t.dev');
    expect(await exp.assign('no-such-exp', u)).toBe('control');
  });

  it('assign() is sticky: a second call returns the same variant', async () => {
    await db
      .insertInto('admin.experiment')
      .values({
        key: 'ranker_weights',
        variants_jsonb: JSON.stringify([
          { name: 'control', weight: 50 },
          { name: 'v2', weight: 50 },
        ]) as never,
        status: 'RUNNING',
      })
      .execute();
    const u = await makeUser('b@t.dev');
    const first = await exp.assign('ranker_weights', u);
    const second = await exp.assign('ranker_weights', u);
    expect(second).toBe(first);
    expect(['control', 'v2']).toContain(first);
  });

  it('assign() splits a population roughly by weight and is deterministic per learner', async () => {
    await db
      .insertInto('admin.experiment')
      .values({
        key: 'split80',
        variants_jsonb: JSON.stringify([
          { name: 'control', weight: 80 },
          { name: 'treat', weight: 20 },
        ]) as never,
        status: 'RUNNING',
      })
      .execute();
    let control = 0;
    for (let i = 0; i < 200; i++) {
      const u = await makeUser(`s${i}@t.dev`);
      const v = await exp.assign('split80', u);
      // deterministic: assigning again gives the same answer
      expect(await exp.assign('split80', u)).toBe(v);
      if (v === 'control') control++;
    }
    // 80/20 with N=200 -> expect ~160 control, allow a wide band
    expect(control).toBeGreaterThan(130);
    expect(control).toBeLessThan(185);
  });

  it('a CONCLUDED experiment falls back to control without enrolling', async () => {
    await db
      .insertInto('admin.experiment')
      .values({
        key: 'done',
        variants_jsonb: JSON.stringify([
          { name: 'control', weight: 50 },
          { name: 'v2', weight: 50 },
        ]) as never,
        status: 'CONCLUDED',
      })
      .execute();
    const u = await makeUser('c@t.dev');
    expect(await exp.assign('done', u)).toBe('control');
    const enrol = await exp.enrolment('done');
    expect(enrol).toEqual([]);
  });

  it('north-star: mastery gain per learner-hour is positive-deltas / active-hours', async () => {
    const u = await makeUser('ns@t.dev');
    const skill = await db
      .insertInto('skill.skill')
      .values({ slug: 'ns.skill', name: 'NS', domain: 'test' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const act = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug: 'lab.ns', mode: 'GUIDED_LAB' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const ver = await db
      .insertInto('content.activity_version')
      .values({ activity_id: act.id, version: 1, status: 'PUBLISHED', spec_jsonb: {} })
      .returningAll()
      .executeTakeFirstOrThrow();
    const att = await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: u,
        activity_id: act.id,
        activity_version_id: ver.id,
        mode: 'GUIDED_LAB',
        status: 'PASSED',
        active_seconds: 1800, // 0.5h
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    // one positive delta (+0.2) and one negative (-0.05, ignored)
    await db
      .insertInto('skill.mastery_evidence')
      .values([
        {
          user_id: u,
          skill_id: skill.id,
          attempt_id: att.id,
          delta: 0.2,
          p_before: 0.4,
          p_after: 0.6,
          weight: 1,
        },
        {
          user_id: u,
          skill_id: skill.id,
          attempt_id: att.id,
          delta: -0.05,
          p_before: 0.6,
          p_after: 0.55,
          weight: 1,
        },
      ])
      .execute();

    const [row] = await ns.metrics({ sinceDays: 30 });
    expect(row.group).toBe('all');
    expect(row.positiveMasteryDeltaSum).toBeCloseTo(0.2, 4);
    expect(row.activeLearnerHours).toBeCloseTo(0.5, 4);
    expect(row.masteryGainPerLearnerHour).toBeCloseTo(0.4, 4); // 0.2 / 0.5
    expect(row.attempts).toBe(1);
    expect(row.completionRate).toBe(1);
    expect(row.abandonmentRate).toBe(0);
  });

  it('north-star: per-variant comparison joins through experiment_assignment', async () => {
    await db
      .insertInto('admin.experiment')
      .values({
        key: 'mentor_persona',
        variants_jsonb: JSON.stringify([
          { name: 'control', weight: 50 },
          { name: 'warm', weight: 50 },
        ]) as never,
        status: 'RUNNING',
      })
      .execute();

    const skill = await db
      .insertInto('skill.skill')
      .values({ slug: 'v.skill', name: 'V', domain: 'test' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const act = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug: 'lab.v', mode: 'GUIDED_LAB' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const ver = await db
      .insertInto('content.activity_version')
      .values({ activity_id: act.id, version: 1, status: 'PUBLISHED', spec_jsonb: {} })
      .returningAll()
      .executeTakeFirstOrThrow();

    // control learner: +0.1 over 1h ; warm learner: +0.4 over 1h
    for (const [email, variant, delta] of [
      ['ctl@t.dev', 'control', 0.1],
      ['wrm@t.dev', 'warm', 0.4],
    ] as const) {
      const uid = await makeUser(email);
      await db
        .insertInto('admin.experiment_assignment')
        .values({ experiment_key: 'mentor_persona', user_id: uid, variant })
        .execute();
      const att = await db
        .insertInto('attempt.attempt')
        .values({
          tenant_id: tenantId,
          user_id: uid,
          activity_id: act.id,
          activity_version_id: ver.id,
          mode: 'GUIDED_LAB',
          status: 'PASSED',
          active_seconds: 3600,
        })
        .returningAll()
        .executeTakeFirstOrThrow();
      await db
        .insertInto('skill.mastery_evidence')
        .values({
          user_id: uid,
          skill_id: skill.id,
          attempt_id: att.id,
          delta,
          p_before: 0.3,
          p_after: 0.3 + delta,
          weight: 1,
        })
        .execute();
    }

    const rows = await ns.metrics({ experimentKey: 'mentor_persona' });
    const byGroup = Object.fromEntries(rows.map((r) => [r.group, r]));
    expect(byGroup['warm'].masteryGainPerLearnerHour).toBeGreaterThan(
      byGroup['control'].masteryGainPerLearnerHour,
    );
  });
});
