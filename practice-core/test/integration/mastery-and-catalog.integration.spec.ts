import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { BktService } from '../../src/modules/skill/bkt.service';
import { EloService } from '../../src/modules/skill/elo.service';
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
    mastery = new MasteryService(db, new BktService(), new EloService());
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

  describe('recordEloMatch (doc §2.6 difficulty calibration)', () => {
    async function seedSkillAndActivity(
      tenantId: string,
      skillSlug: string,
      activitySlug: string,
    ) {
      const skill = await skillRepo.createSkill({
        slug: skillSlug,
        name: skillSlug,
        domain: 'k8s',
      });
      const version = await catalog.publishNewVersion({
        tenantId,
        activitySlug,
        mode: 'GUIDED_LAB',
        spec: {
          id: activitySlug,
          version: 1,
          meta: { difficulty_level: 'L2', estimated_minutes: 20 },
          environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.02 },
          skills: [{ skill: skillSlug, weight: 1.0, primary: true }],
        },
      });
      return { skill, version };
    }

    it('seeds both ratings at the population default (1200) on a learner and activity with no prior rating', async () => {
      const { tenant, user } = await seedTenantAndUser();
      const { skill, version } = await seedSkillAndActivity(
        tenant.id,
        'k8s.match-default',
        'lab.match-default',
      );

      const result = await mastery.recordEloMatch({
        userId: user.id,
        skillId: skill.id,
        activityVersionId: version.id,
        outcome: 1,
      });
      // 1200 vs 1200 -> expectedPass 0.5, outcome 1 -> difficultyAdjust computed pre-match is 0.
      expect(result.difficultyAdjust).toBeCloseTo(0, 5);

      const activityRow = await db
        .selectFrom('content.activity_version')
        .select('difficulty_elo')
        .where('id', '=', version.id)
        .executeTakeFirstOrThrow();
      // K_a=4, surprise=0.5 -> activity rating moves down from 1200.
      expect(Number(activityRow.difficulty_elo)).toBeCloseTo(1198, 5);
    });

    it("seeds a new skill_mastery row (p_mastery from bkt_p_init, elo_rating from this match) on a learner's first Elo match", async () => {
      const { tenant, user } = await seedTenantAndUser();
      const { skill, version } = await seedSkillAndActivity(
        tenant.id,
        'k8s.match-noseed',
        'lab.match-noseed',
      );

      await mastery.recordEloMatch({
        userId: user.id,
        skillId: skill.id,
        activityVersionId: version.id,
        outcome: 1,
      });

      const stored = await mastery.getMastery(user.id, skill.id);
      expect(stored).toBeDefined();
      expect(Number(stored!.p_mastery)).toBeCloseTo(
        Number(skill.bkt_p_init),
        5,
      );
      // 1200 vs 1200 -> expectedPass 0.5, outcome 1 -> surprise 0.5 -> +16.
      expect(Number(stored!.elo_rating)).toBeCloseTo(1216, 5);
    });

    it('does not clobber p_mastery/evidence_count when recordEvidence has already seeded the row before the first Elo match', async () => {
      const { tenant, user } = await seedTenantAndUser();
      const { skill, version } = await seedSkillAndActivity(
        tenant.id,
        'k8s.match-preseeded',
        'lab.match-preseeded',
      );

      await db
        .insertInto('skill.skill_mastery')
        .values({
          user_id: user.id,
          skill_id: skill.id,
          p_mastery: 0.7321,
          evidence_count: 3,
        })
        .execute();

      await mastery.recordEloMatch({
        userId: user.id,
        skillId: skill.id,
        activityVersionId: version.id,
        outcome: 1,
      });

      const stored = await mastery.getMastery(user.id, skill.id);
      expect(Number(stored!.p_mastery)).toBeCloseTo(0.7321, 5);
      expect(stored!.evidence_count).toBe(3);
      expect(Number(stored!.elo_rating)).toBeCloseTo(1216, 5);
    });

    it('serializes concurrent matches on the same (user, skill) instead of losing an update (regression: FOR UPDATE row lock)', async () => {
      // Two skills seeded identically: one gets N matches fired
      // sequentially (the known-correct reference outcome), the other
      // gets the same N matches fired concurrently via Promise.all.
      // Without the FOR UPDATE row lock, concurrent transactions can
      // read the same pre-match elo_rating/difficulty_elo and overwrite
      // each other -- the concurrent run's final ratings would diverge
      // from the sequential reference because fewer than N updates
      // actually applied. With the lock, concurrent calls are
      // serialized (just reordered relative to each other, which
      // doesn't matter here since every match uses the same outcome),
      // so both runs must converge on the same final ratings.
      const { tenant, user: sequentialUser } = await seedTenantAndUser();
      const concurrentUser = await db
        .insertInto('learner.user_account')
        .values({ tenant_id: tenant.id, email: 'learner-concurrent@test.dev' })
        .returningAll()
        .executeTakeFirstOrThrow();

      const { skill: seqSkill, version: seqVersion } =
        await seedSkillAndActivity(
          tenant.id,
          'k8s.match-concurrent-seq',
          'lab.match-concurrent-seq',
        );
      const { skill: conSkill, version: conVersion } =
        await seedSkillAndActivity(
          tenant.id,
          'k8s.match-concurrent-con',
          'lab.match-concurrent-con',
        );

      const N = 8;
      for (let i = 0; i < N; i++) {
        await mastery.recordEloMatch({
          userId: sequentialUser.id,
          skillId: seqSkill.id,
          activityVersionId: seqVersion.id,
          outcome: 1,
        });
      }

      await Promise.all(
        Array.from({ length: N }, () =>
          mastery.recordEloMatch({
            userId: concurrentUser.id,
            skillId: conSkill.id,
            activityVersionId: conVersion.id,
            outcome: 1,
          }),
        ),
      );

      const sequentialResult = await mastery.getMastery(
        sequentialUser.id,
        seqSkill.id,
      );
      const concurrentResult = await mastery.getMastery(
        concurrentUser.id,
        conSkill.id,
      );
      const sequentialActivity = await db
        .selectFrom('content.activity_version')
        .select('difficulty_elo')
        .where('id', '=', seqVersion.id)
        .executeTakeFirstOrThrow();
      const concurrentActivity = await db
        .selectFrom('content.activity_version')
        .select('difficulty_elo')
        .where('id', '=', conVersion.id)
        .executeTakeFirstOrThrow();

      expect(Number(concurrentResult!.elo_rating)).toBeCloseTo(
        Number(sequentialResult!.elo_rating),
        5,
      );
      expect(Number(concurrentActivity.difficulty_elo)).toBeCloseTo(
        Number(sequentialActivity.difficulty_elo),
        5,
      );
      // Sanity: N matches at outcome=1 must have actually moved the
      // learner rating up from the 1200 default -- proves this isn't
      // trivially passing because nothing applied on either side.
      expect(Number(concurrentResult!.elo_rating)).toBeGreaterThan(1200);
    });

    it('updates an existing learner elo_rating when a skill_mastery row already exists', async () => {
      const { tenant, user } = await seedTenantAndUser();
      const { skill, version } = await seedSkillAndActivity(
        tenant.id,
        'k8s.match-existing',
        'lab.match-existing',
      );

      // Seed the skill_mastery row directly (not via recordEvidence) --
      // recordEvidence also writes a skill.mastery_evidence row, which
      // recordEloMatch's own learnerMatchCount query counts (mastery.
      // service.ts: "Attempt count for this learner in the activity's
      // skill-cluster, before this match -- drives K_l decay"). Real
      // production code (evaluation.service.ts) always calls
      // recordEloMatch BEFORE recordEvidence for exactly this reason
      // (its own doc comment: "run before recordEvidence... so its
      // difficultyAdjust reflects pre-match ratings") -- calling
      // recordEvidence first here would inflate learnerMatchCount to 1
      // before this test's own first real Elo match, decaying K_l from
      // 32 to 31 and silently changing the expected result. A direct
      // insert seeds the row this test actually needs (an existing
      // skill_mastery row with a null elo_rating to update) without
      // that side effect.
      await db
        .insertInto('skill.skill_mastery')
        .values({
          user_id: user.id,
          skill_id: skill.id,
          p_mastery: skill.bkt_p_init,
        })
        .execute();

      const before = await mastery.getMastery(user.id, skill.id);
      expect(before!.elo_rating).toBeNull();

      await mastery.recordEloMatch({
        userId: user.id,
        skillId: skill.id,
        activityVersionId: version.id,
        outcome: 1,
      });

      const after = await mastery.getMastery(user.id, skill.id);
      // learner Elo defaulted to 1200 (no prior elo_rating), K_l=32 at 0
      // prior matches (skill.mastery_evidence genuinely empty here),
      // expectedPass=0.5, outcome=1 -> surprise 0.5 -> +16.
      expect(Number(after!.elo_rating)).toBeCloseTo(1216, 5);
    });

    it('raises difficulty_elo on a fail (learner under-performs an easy activity, activity looks harder than measured)', async () => {
      const { tenant, user } = await seedTenantAndUser();
      const { skill, version } = await seedSkillAndActivity(
        tenant.id,
        'k8s.match-fail',
        'lab.match-fail',
      );

      await mastery.recordEloMatch({
        userId: user.id,
        skillId: skill.id,
        activityVersionId: version.id,
        outcome: 0,
      });

      const activityRow = await db
        .selectFrom('content.activity_version')
        .select('difficulty_elo')
        .where('id', '=', version.id)
        .executeTakeFirstOrThrow();
      expect(Number(activityRow.difficulty_elo)).toBeGreaterThan(1200);
    });

    it('freezes activity Elo once the activity has more than 500 attempts', async () => {
      const { tenant, user } = await seedTenantAndUser();
      const { skill, version } = await seedSkillAndActivity(
        tenant.id,
        'k8s.match-frozen',
        'lab.match-frozen',
      );

      // Seed 501 attempt rows against this version so activityAttemptCount > 500.
      const rows = Array.from({ length: 501 }, () => ({
        tenant_id: tenant.id,
        user_id: user.id,
        activity_id: version.activity_id,
        activity_version_id: version.id,
        mode: 'GUIDED_LAB' as const,
      }));
      await db.insertInto('attempt.attempt').values(rows).execute();

      await mastery.recordEloMatch({
        userId: user.id,
        skillId: skill.id,
        activityVersionId: version.id,
        outcome: 0,
      });

      const activityRow = await db
        .selectFrom('content.activity_version')
        .select('difficulty_elo')
        .where('id', '=', version.id)
        .executeTakeFirstOrThrow();
      expect(Number(activityRow.difficulty_elo)).toBe(1200); // unchanged -- K_a frozen to 0
    });
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
