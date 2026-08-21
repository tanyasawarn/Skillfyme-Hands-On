import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { BktService } from '../../src/modules/skill/bkt.service';
import { MasteryService } from '../../src/modules/skill/mastery.service';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { createTestDb, truncateAll } from './test-db';

describe('MasteryService + CatalogRepository (integration, real Postgres)', () => {
  let db: Kysely<Database>;
  let skillRepo: SkillRepository;
  let mastery: MasteryService;
  let catalog: CatalogRepository;

  beforeAll(() => {
    db = createTestDb();
    skillRepo = new SkillRepository(db);
    mastery = new MasteryService(db, new BktService());
    catalog = new CatalogRepository(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
  });

  async function seedTenantAndUser() {
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
    return { tenant, user };
  }

  async function seedAttempt(
    tenantId: string,
    userId: string,
    activityId: string,
    versionId: string,
  ) {
    return db
      .insertInto('attempt.attempt')
      .values({
        tenant_id: tenantId,
        user_id: userId,
        activity_id: activityId,
        activity_version_id: versionId,
        mode: 'GUIDED_LAB',
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  it('records mastery evidence and persists both skill_mastery and mastery_evidence atomically', async () => {
    const { tenant, user } = await seedTenantAndUser();
    const skill = await skillRepo.createSkill({
      slug: 'k8s.deployments',
      name: 'K8s Deployments',
      domain: 'k8s',
    });

    const version = await catalog.publishNewVersion({
      tenantId: tenant.id,
      activitySlug: 'lab.k8s.deploy-node-app',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.k8s.deploy-node-app',
        version: 1,
        meta: { difficulty_level: 'L2', estimated_minutes: 35 },
        environment: {
          blueprint: 'bp.k8s-single-node.v1',
          cost_budget_usd: 0.08,
        },
        skills: [{ skill: 'k8s.deployments', weight: 1.0, primary: true }],
      },
    });

    const attempt = await seedAttempt(
      tenant.id,
      user.id,
      version.activity_id,
      version.id,
    );

    const { pBefore, pAfter } = await mastery.recordEvidence({
      userId: user.id,
      skillId: skill.id,
      attemptId: attempt.id,
      score: 0.86,
      weight: 1.0,
      passThreshold: 0.7,
      difficultyAdjust: 0,
      wasGenuineAttempt: true,
    });

    expect(pAfter).toBeGreaterThan(pBefore);

    const stored = await mastery.getMastery(user.id, skill.id);
    expect(stored).toBeDefined();
    expect(Number(stored!.p_mastery)).toBeCloseTo(pAfter, 5);
    expect(stored!.evidence_count).toBe(1);

    const evidenceRows = await db
      .selectFrom('skill.mastery_evidence')
      .selectAll()
      .where('attempt_id', '=', attempt.id)
      .execute();
    expect(evidenceRows).toHaveLength(1);
    expect(evidenceRows[0].skill_id).toBe(skill.id);
  });

  it('accumulates evidence_count across repeated attempts on the same skill (upsert path)', async () => {
    const { user } = await seedTenantAndUser();
    const skill = await skillRepo.createSkill({
      slug: 'docker.basics',
      name: 'Docker Basics',
      domain: 'docker',
    });
    const tenant = await db
      .selectFrom('learner.tenant')
      .selectAll()
      .executeTakeFirstOrThrow();
    const version = await catalog.publishNewVersion({
      tenantId: tenant.id,
      activitySlug: 'lab.docker.build',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.docker.build',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.docker.v1', cost_budget_usd: 0.02 },
        skills: [{ skill: 'docker.basics', weight: 1.0, primary: true }],
      },
    });

    for (let i = 0; i < 3; i++) {
      const attempt = await seedAttempt(
        tenant.id,
        user.id,
        version.activity_id,
        version.id,
      );
      await mastery.recordEvidence({
        userId: user.id,
        skillId: skill.id,
        attemptId: attempt.id,
        score: 0.9,
        weight: 1.0,
        passThreshold: 0.7,
        difficultyAdjust: 0,
        wasGenuineAttempt: true,
      });
    }

    const stored = await mastery.getMastery(user.id, skill.id);
    expect(stored!.evidence_count).toBe(3);
  });

  it('gates REQUIRES ancestors correctly via meetsRequiresGate (doc §2.5 stage 2)', async () => {
    const { user } = await seedTenantAndUser();
    const prereq = await skillRepo.createSkill({
      slug: 'linux.cli',
      name: 'Linux CLI',
      domain: 'linux',
    });
    const target = await skillRepo.createSkill({
      slug: 'docker.basics',
      name: 'Docker Basics',
      domain: 'docker',
    });
    await skillRepo.addEdge({
      fromSkillId: prereq.id,
      toSkillId: target.id,
      type: 'REQUIRES',
    });
    await skillRepo.rebuildClosure();

    // No evidence yet: gate should fail (mastery defaults to 0, below 0.55 threshold).
    const ancestorsBeforeGate = await skillRepo.getRequiresAncestors(target.id);
    expect(await mastery.meetsRequiresGate(user.id, ancestorsBeforeGate)).toBe(
      false,
    );

    // Record strong evidence on the prerequisite until it clears 0.55.
    const tenant = await db
      .selectFrom('learner.tenant')
      .selectAll()
      .executeTakeFirstOrThrow();
    const version = await catalog.publishNewVersion({
      tenantId: tenant.id,
      activitySlug: 'lab.linux.basics',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.linux.basics',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 15 },
        environment: { blueprint: 'bp.linux.v1', cost_budget_usd: 0.01 },
        skills: [{ skill: 'linux.cli', weight: 1.0, primary: true }],
      },
    });

    let p = 0;
    for (let i = 0; i < 6; i++) {
      const attempt = await seedAttempt(
        tenant.id,
        user.id,
        version.activity_id,
        version.id,
      );
      const r = await mastery.recordEvidence({
        userId: user.id,
        skillId: prereq.id,
        attemptId: attempt.id,
        score: 0.95,
        weight: 1.0,
        passThreshold: 0.6,
        difficultyAdjust: 0,
        wasGenuineAttempt: true,
      });
      p = r.pAfter;
      if (p >= 0.55) break;
    }

    expect(p).toBeGreaterThanOrEqual(0.55);
    expect(await mastery.meetsRequiresGate(user.id, ancestorsBeforeGate)).toBe(
      true,
    );
  });

  it('CatalogRepository.publishNewVersion rejects publishing an out-of-sequence version', async () => {
    const { tenant } = await seedTenantAndUser();
    await skillRepo.createSkill({
      slug: 'k8s.core',
      name: 'K8s Core',
      domain: 'k8s',
    });

    await catalog.publishNewVersion({
      tenantId: tenant.id,
      activitySlug: 'lab.k8s.core-concepts',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.k8s.core-concepts',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.k8s.v1', cost_budget_usd: 0.03 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
      },
    });

    await expect(
      catalog.publishNewVersion({
        tenantId: tenant.id,
        activitySlug: 'lab.k8s.core-concepts',
        mode: 'GUIDED_LAB',
        spec: {
          id: 'lab.k8s.core-concepts',
          version: 3, // should be 2
          meta: { difficulty_level: 'L1', estimated_minutes: 20 },
          environment: { blueprint: 'bp.k8s.v1', cost_budget_usd: 0.03 },
          skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
        },
      }),
    ).rejects.toThrow(/next available version/);
  });

  it('CatalogRepository.publishNewVersion rejects unknown skill slugs', async () => {
    const { tenant } = await seedTenantAndUser();
    await expect(
      catalog.publishNewVersion({
        tenantId: tenant.id,
        activitySlug: 'lab.bad-skill',
        mode: 'GUIDED_LAB',
        spec: {
          id: 'lab.bad-skill',
          version: 1,
          meta: { difficulty_level: 'L1', estimated_minutes: 10 },
          environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.01 },
          skills: [{ skill: 'nonexistent.skill', weight: 1.0, primary: true }],
        },
      }),
    ).rejects.toThrow(/unknown skill slugs/);
  });

  it('PUBLISHED activity_version cannot be mutated at the DB level (doc §3.6 rule 11)', async () => {
    const { tenant } = await seedTenantAndUser();
    await skillRepo.createSkill({
      slug: 'k8s.yaml',
      name: 'K8s YAML',
      domain: 'k8s',
    });
    const version = await catalog.publishNewVersion({
      tenantId: tenant.id,
      activitySlug: 'lab.k8s.yaml-basics',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.k8s.yaml-basics',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 15 },
        environment: { blueprint: 'bp.k8s.v1', cost_budget_usd: 0.02 },
        skills: [{ skill: 'k8s.yaml', weight: 1.0, primary: true }],
      },
    });

    await expect(
      db
        .updateTable('content.activity_version')
        .set({ estimated_minutes: 999 })
        .where('id', '=', version.id)
        .execute(),
    ).rejects.toThrow(/immutable/);
  });
});
