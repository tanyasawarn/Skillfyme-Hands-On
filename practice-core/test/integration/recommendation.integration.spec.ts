import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AttemptRepository } from '../../src/modules/attempt/attempt.repository';
import { AttemptService } from '../../src/modules/attempt/attempt.service';
import { EligibilityService } from '../../src/modules/attempt/eligibility.service';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { FakeOrchestratorClient } from '../../src/modules/attempt/fake-orchestrator.client';
import { ConfigService } from '@nestjs/config';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { MasteryService } from '../../src/modules/skill/mastery.service';
import { BktService } from '../../src/modules/skill/bkt.service';
import { EloService } from '../../src/modules/skill/elo.service';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { CurriculumRepository } from '../../src/modules/curriculum/curriculum.repository';
import { EvaluationService } from '../../src/modules/evaluation/evaluation.service';
import { ValidatorRunnerService } from '../../src/modules/evaluation/validator-runner.service';
import { ScoringEngineService } from '../../src/modules/evaluation/scoring-engine.service';
import { FakeValidatorExecutor } from '../../src/modules/evaluation/fake-validator-executor';
import { ReplayService } from '../../src/modules/event-store/replay.service';
import { RecommendationService } from '../../src/modules/recommendation/recommendation.service';
import { FaultRepository } from '../../src/modules/evaluation/fault.repository';
import { ArtifactService } from '../../src/modules/evaluation/artifact.service';
import { ActivitySpecReader } from '../../src/common/activity-spec-reader';
import { RubricRepository } from '../../src/modules/evaluation/rubric.repository';
import { FakeAiGrader } from '../../src/modules/evaluation/fake-ai-grader.service';
import { createTestDb, truncateAll } from './test-db';

describe('RecommendationService (integration, real Postgres) — doc §2.5 reduced Phase 1 pipeline', () => {
  let db: Kysely<Database>;
  let attemptService: AttemptService;
  let catalog: CatalogRepository;
  let curriculum: CurriculumRepository;
  let skillRepo: SkillRepository;
  let recommendation: RecommendationService;
  let fakeExecutor: FakeValidatorExecutor;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    const attemptRepo = new AttemptRepository(db);
    skillRepo = new SkillRepository(db);
    const mastery = new MasteryService(db, new BktService(), new EloService());
    const eligibility = new EligibilityService(db, skillRepo, mastery, {
      get: () => undefined,
    } as unknown as ConfigService);
    const events = new EventStoreRepository(db);
    const orchestrator = new FakeOrchestratorClient(5);
    fakeExecutor = new FakeValidatorExecutor();
    const validatorRunner = new ValidatorRunnerService(
      db,
      events,
      fakeExecutor,
    );
    const replay = new ReplayService(db, events);
    const evaluation = new EvaluationService(
      db,
      events,
      validatorRunner,
      new ScoringEngineService(),
      mastery,
      replay,
      new FaultRepository(),
      new ArtifactService(
        db,
        events,
        new RubricRepository(),
        new FakeAiGrader(),
        new ActivitySpecReader(db),
      ),
      attemptRepo,
    );
    attemptService = new AttemptService(
      db,
      attemptRepo,
      eligibility,
      events,
      orchestrator,
      evaluation,
      validatorRunner,
      replay,
    );
    catalog = new CatalogRepository(db);
    curriculum = new CurriculumRepository(db);
    recommendation = new RecommendationService(db, eligibility);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
    const tenant = await db
      .insertInto('learner.tenant')
      .values({ name: 't' })
      .returningAll()
      .executeTakeFirstOrThrow();
    const user = await db
      .insertInto('learner.user_account')
      .values({ tenant_id: tenant.id, email: 'l@test.dev' })
      .returningAll()
      .executeTakeFirstOrThrow();
    tenantId = tenant.id;
    userId = user.id;
  });

  async function seedCourseAndSkill() {
    await skillRepo.createSkill({
      slug: 'k8s.deployments',
      name: 'K8s Deployments',
      domain: 'k8s',
    });
    const course = await curriculum.createCourse({
      tenantId,
      slug: 'course.devops',
      title: 'DevOps',
    });
    const module_ = await curriculum.createModule({
      courseId: course.id,
      title: 'K8s',
      position: 1,
    });
    const topic = await curriculum.createTopic({
      moduleId: module_.id,
      title: 'Deployments',
      position: 1,
      slug: 'topic.k8s.deployments',
    });
    return { topic };
  }

  async function publishActivity(
    slug: string,
    primaryTopicSlug: string,
    difficulty: 'L1' | 'L2' = 'L2',
  ) {
    return catalog.publishNewVersion({
      tenantId,
      activitySlug: slug,
      mode: 'GUIDED_LAB',
      spec: {
        id: slug,
        version: 1,
        meta: { difficulty_level: difficulty, estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.02 },
        skills: [{ skill: 'k8s.deployments', weight: 1.0, primary: true }],
        curriculum: { primary_topic: primaryTopicSlug },
        tasks: [
          {
            key: 't1',
            required: true,
            validators: [
              {
                id: 'v1',
                type: 'SHELL_ASSERT',
                expect: { exit_code: 0 },
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
          },
        ],
      } as never,
    });
  }

  it('recommends a curriculum-adjacent activity for a cold-start learner with no attempt history', async () => {
    const { topic } = await seedCourseAndSkill();
    await publishActivity('lab.first', topic.slug!);

    const recs = await recommendation.recommend(userId, tenantId);
    expect(recs).toHaveLength(1);
    expect(recs[0].reasonCode).toBe('CURRICULUM_ADJACENT');
    expect(recs[0].reasonParams.topic).toBe('Deployments');
  });

  it('persists recommendations to learner.recommendation for later click-through tracking (doc §2.5)', async () => {
    const { topic } = await seedCourseAndSkill();
    await publishActivity('lab.first', topic.slug!);

    await recommendation.recommend(userId, tenantId);

    const rows = await db
      .selectFrom('learner.recommendation')
      .selectAll()
      .where('user_id', '=', userId)
      .execute();
    expect(rows).toHaveLength(1);
    expect(rows[0].reason_code).toBe('CURRICULUM_ADJACENT');
    // rules-v2: the recommender now runs all 5 doc §2.5 candidate sources
    // (curriculum-adjacent + remediation + spaced-repetition + progression
    // + unblocking) with cross-source weighted scoring (PLAN.md G8/G9/G10).
    expect(rows[0].ranker_version).toBe('rules-v2');
  });

  it('excludes activities the learner has already attempted', async () => {
    const { topic } = await seedCourseAndSkill();
    const version = await publishActivity('lab.attempted', topic.slug!);

    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    await attemptService.submit(attempt.id);

    const recs = await recommendation.recommend(userId, tenantId);
    expect(recs.find((r) => r.slug === 'lab.attempted')).toBeUndefined();
  });

  it('recommends remediation for a struggling skill with low mastery', async () => {
    const { topic } = await seedCourseAndSkill();
    const version = await publishActivity('lab.hard', topic.slug!, 'L2');
    const remediationVersion = await publishActivity(
      'lab.remediation',
      topic.slug!,
      'L1',
    );

    // Fail the harder activity to build up a low-mastery + FAILED-attempt signal.
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    const provisioned = await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    fakeExecutor.setOverride(provisioned.environment_id!, 'v1', 'FAIL');
    const result = await attemptService.submit(attempt.id);
    expect(result.status).toBe('FAILED');

    const recs = await recommendation.recommend(userId, tenantId);
    const remediationRec = recs.find(
      (r) => r.activityVersionId === remediationVersion.id,
    );
    expect(remediationRec).toBeDefined();
    expect(remediationRec!.reasonCode).toBe('REMEDIATION');
  });

  it('only recommends eligible activities (respects the REQUIRES prerequisite gate)', async () => {
    const prereq = await skillRepo.createSkill({
      slug: 'linux.cli',
      name: 'Linux CLI',
      domain: 'linux',
    });
    const target = await skillRepo.createSkill({
      slug: 'k8s.deployments',
      name: 'K8s Deployments',
      domain: 'k8s',
    });
    await skillRepo.addEdge({
      fromSkillId: prereq.id,
      toSkillId: target.id,
      type: 'REQUIRES',
    });
    await skillRepo.rebuildClosure();

    const course = await curriculum.createCourse({
      tenantId,
      slug: 'course.devops',
      title: 'DevOps',
    });
    const module_ = await curriculum.createModule({
      courseId: course.id,
      title: 'K8s',
      position: 1,
    });
    const topic = await curriculum.createTopic({
      moduleId: module_.id,
      title: 'Deployments',
      position: 1,
      slug: 'topic.gated',
    });

    await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.gated',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.gated',
        version: 1,
        meta: { difficulty_level: 'L2', estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.02 },
        skills: [{ skill: 'k8s.deployments', weight: 1.0, primary: true }],
        curriculum: { primary_topic: 'topic.gated' },
        tasks: [
          {
            key: 't1',
            required: true,
            validators: [
              {
                id: 'v1',
                type: 'SHELL_ASSERT',
                expect: {},
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
          },
        ],
      } as never,
    });

    // Learner has zero mastery of the REQUIRES prerequisite -> ineligible,
    // must not appear in recommendations even though it's curriculum-adjacent.
    const recs = await recommendation.recommend(userId, tenantId);
    expect(recs.find((r) => r.slug === 'lab.gated')).toBeUndefined();
  });
});
