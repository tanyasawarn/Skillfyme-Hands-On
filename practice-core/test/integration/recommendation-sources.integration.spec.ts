import type { Kysely } from 'kysely';
import { ConfigService } from '@nestjs/config';
import type { Database } from '../../src/db/schema';
import { RecommendationService } from '../../src/modules/recommendation/recommendation.service';
import { EligibilityService } from '../../src/modules/attempt/eligibility.service';
import { MasteryService } from '../../src/modules/skill/mastery.service';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { BktService } from '../../src/modules/skill/bkt.service';
import { EloService } from '../../src/modules/skill/elo.service';
import { createTestDb, truncateAll } from './test-db';

/**
 * PLAN.md G8/G9/G10 (Phase 4): the doc §2.5 recommender now runs all five
 * candidate sources. This spec seeds the DB directly and asserts each of
 * the three NEW sources (spaced-repetition, progression, unblocking) and
 * the G10 skill-DAG ancestor-walk in remediation.
 */
describe('RecommendationService — the 5 candidate sources (§2.5, G8/G9/G10)', () => {
  let db: Kysely<Database>;
  let svc: RecommendationService;

  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    const skillRepo = new SkillRepository(db);
    const mastery = new MasteryService(db, new BktService(), new EloService());
    const eligibility = new EligibilityService(db, skillRepo, mastery, {
      get: () => undefined,
    } as unknown as ConfigService);
    svc = new RecommendationService(db, eligibility);
  });

  afterAll(async () => {
    await db.destroy();
  });

  // helpers -----------------------------------------------------------
  async function makeSkill(slug: string) {
    return db
      .insertInto('skill.skill')
      .values({ slug, name: slug, domain: 'test' })
      .returningAll()
      .executeTakeFirstOrThrow();
  }
  async function makeActivity(
    slug: string,
    skillId: string,
    difficulty: 'L1' | 'L2' | 'L3' | 'L4' | 'L5',
    isPrimary = true,
  ) {
    const a = await db
      .insertInto('content.activity')
      .values({ tenant_id: tenantId, slug, mode: 'GUIDED_LAB' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const av = await db
      .insertInto('content.activity_version')
      .values({
        activity_id: a.id,
        version: 1,
        status: 'PUBLISHED',
        spec_jsonb: {},
        difficulty_level: difficulty,
      })
      .returningAll()
      .executeTakeFirstOrThrow();
    await db
      .insertInto('content.activity_skill')
      .values({
        activity_version_id: av.id,
        skill_id: skillId,
        weight: 1,
        is_primary: isPrimary,
      })
      .execute();
    return { activityId: a.id, activityVersionId: av.id };
  }
  async function setMastery(
    skillId: string,
    pMastery: number,
    band: string,
    reviewDueAt: Date | null,
  ) {
    await db
      .insertInto('skill.skill_mastery')
      .values({
        user_id: userId,
        skill_id: skillId,
        p_mastery: pMastery,
        band,
        review_due_at: reviewDueAt,
        evidence_count: 1,
        last_evidence_at: new Date(),
      })
      .execute();
  }

  beforeEach(async () => {
    await truncateAll(db);
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 'rec-tenant' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenantId, email: 'rec@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    userId = user.id;
  });

  it('SPACED_REPETITION: a Competent+ skill past its review_due_at surfaces a review activity', async () => {
    const s = await makeSkill('k8s.services');
    await makeActivity('lab.k8s.services-review', s.id, 'L2');
    await setMastery(
      s.id,
      0.8,
      'Proficient',
      new Date(Date.now() - 86_400_000),
    ); // due yesterday

    const recs = await svc.recommend(userId, tenantId, 10);
    const sr = recs.find((r) => r.reasonCode === 'SPACED_REPETITION');
    expect(sr).toBeDefined();
    expect(sr!.reasonParams.skill_id).toBe(s.id);
  });

  it('SPACED_REPETITION: a skill whose review_due_at is in the future does NOT surface', async () => {
    const s = await makeSkill('k8s.pods');
    await makeActivity('lab.k8s.pods-review', s.id, 'L2');
    await setMastery(
      s.id,
      0.8,
      'Proficient',
      new Date(Date.now() + 7 * 86_400_000),
    );

    const recs = await svc.recommend(userId, tenantId, 10);
    expect(
      recs.find((r) => r.reasonCode === 'SPACED_REPETITION'),
    ).toBeUndefined();
  });

  it('PROGRESSION: a Mastered skill offers the next-difficulty (L4/L5) unattempted activity', async () => {
    const s = await makeSkill('terraform.state');
    await makeActivity('lab.tf.state-advanced', s.id, 'L4');
    await setMastery(s.id, 0.95, 'Mastered', null);

    const recs = await svc.recommend(userId, tenantId, 10);
    const prog = recs.find((r) => r.reasonCode === 'PROGRESSION');
    expect(prog).toBeDefined();
    expect(prog!.reasonParams.next_level).toBe('L4');
  });

  it('UNBLOCKING: a weak skill that gates downstream skills surfaces its own prereq activity', async () => {
    const prereq = await makeSkill('linux.cli');
    const downstream = await makeSkill('docker.basics');
    // closure: linux.cli is an ancestor of docker.basics
    await db
      .insertInto('skill.skill_closure')
      .values({
        ancestor_id: prereq.id,
        descendant_id: downstream.id,
        depth: 1,
        edge_types: ['REQUIRES'],
      })
      .execute();
    await makeActivity('lab.linux.cli-basics', prereq.id, 'L1');
    await setMastery(prereq.id, 0.3, 'Novice', null); // weak

    const recs = await svc.recommend(userId, tenantId, 10);
    const unblock = recs.find((r) => r.reasonCode === 'UNBLOCKING');
    expect(unblock).toBeDefined();
    expect(unblock!.reasonParams.prerequisite_skill_id).toBe(prereq.id);
    expect(Number(unblock!.reasonParams.unblocks)).toBeGreaterThanOrEqual(1);
  });

  it('G10 ancestor-walk: a failed low-mastery skill also surfaces remediation for its unmastered REQUIRES ancestor', async () => {
    const ancestor = await makeSkill('k8s.core');
    const struggling = await makeSkill('k8s.troubleshooting');
    await db
      .insertInto('skill.skill_closure')
      .values({
        ancestor_id: ancestor.id,
        descendant_id: struggling.id,
        depth: 1,
        edge_types: ['REQUIRES'],
      })
      .execute();
    const ancAct = await makeActivity(
      'lab.k8s.core-fundamentals',
      ancestor.id,
      'L1',
    );

    // a FAILED attempt on an activity targeting the struggling skill, in 30d
    const failAct = await makeActivity(
      'lab.k8s.troubleshoot-hard',
      struggling.id,
      'L3',
    );
    await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: userId,
        activity_id: failAct.activityId,
        activity_version_id: failAct.activityVersionId,
        mode: 'GUIDED_LAB',
        status: 'FAILED',
      })
      .execute();
    await setMastery(struggling.id, 0.3, 'Novice', null);
    await setMastery(ancestor.id, 0.4, 'Developing', null); // unmastered ancestor

    const recs = await svc.recommend(userId, tenantId, 10);
    const ancRemediation = recs.find(
      (r) =>
        r.reasonCode === 'REMEDIATION' &&
        r.activityId === ancAct.activityId &&
        r.reasonParams.prerequisite_skill_id === ancestor.id,
    );
    expect(ancRemediation).toBeDefined();
    expect(ancRemediation!.reasonParams.blocked_skill_id).toBe(struggling.id);
  });

  it('weighted scoring: a REMEDIATION candidate outranks a SPACED_REPETITION one for the same learner', async () => {
    // remediation setup
    const strug = await makeSkill('cicd.pipelines');
    await makeActivity('lab.cicd.pipelines-basics', strug.id, 'L1');
    const failAct = await makeActivity(
      'lab.cicd.pipelines-hard',
      strug.id,
      'L3',
    );
    await db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: userId,
        activity_id: failAct.activityId,
        activity_version_id: failAct.activityVersionId,
        mode: 'GUIDED_LAB',
        status: 'FAILED',
      })
      .execute();
    await setMastery(strug.id, 0.3, 'Novice', null);
    // spaced-rep setup
    const rev = await makeSkill('observability.metrics');
    await makeActivity('lab.obs.metrics-review', rev.id, 'L2');
    await setMastery(
      rev.id,
      0.85,
      'Proficient',
      new Date(Date.now() - 86_400_000),
    );

    const recs = await svc.recommend(userId, tenantId, 10);
    const remIdx = recs.findIndex((r) => r.reasonCode === 'REMEDIATION');
    const srIdx = recs.findIndex((r) => r.reasonCode === 'SPACED_REPETITION');
    expect(remIdx).toBeGreaterThanOrEqual(0);
    expect(srIdx).toBeGreaterThanOrEqual(0);
    expect(remIdx).toBeLessThan(srIdx);
  });
});
