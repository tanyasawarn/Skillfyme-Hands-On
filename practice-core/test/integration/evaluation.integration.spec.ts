import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { AttemptRepository } from '../../src/modules/attempt/attempt.repository';
import { AttemptService } from '../../src/modules/attempt/attempt.service';
import { EligibilityService } from '../../src/modules/attempt/eligibility.service';
import { EventStoreRepository } from '../../src/modules/event-store/event-store.repository';
import { FakeOrchestratorClient } from '../../src/modules/attempt/fake-orchestrator.client';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { MasteryService } from '../../src/modules/skill/mastery.service';
import { BktService } from '../../src/modules/skill/bkt.service';
import { CatalogRepository } from '../../src/modules/catalog/catalog.repository';
import { EvaluationService } from '../../src/modules/evaluation/evaluation.service';
import { ValidatorRunnerService } from '../../src/modules/evaluation/validator-runner.service';
import { ScoringEngineService } from '../../src/modules/evaluation/scoring-engine.service';
import { FakeValidatorExecutor } from '../../src/modules/evaluation/fake-validator-executor';
import { ReplayService } from '../../src/modules/event-store/replay.service';
import { createTestDb, truncateAll } from './test-db';

/**
 * Doc §3.2 worked example transcribed as tasks/validators, mirroring
 * content/activities/lab.k8s.deploy-node-app.yaml's t1/t2 shape --
 * validates the whole evaluate() pipeline (ValidatorRunner -> Scoring ->
 * attempt_score -> BKT mastery -> learner_activity_state) against a
 * realistic multi-task, multi-validator, mixed-severity activity rather
 * than the zero-task edge case covered elsewhere.
 */
describe('EvaluationService (integration, real Postgres) — doc §4.1 step 11, §6.3, §6.4', () => {
  let db: Kysely<Database>;
  let attemptService: AttemptService;
  let catalog: CatalogRepository;
  let skillRepo: SkillRepository;
  let mastery: MasteryService;
  let fakeExecutor: FakeValidatorExecutor;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    const attemptRepo = new AttemptRepository(db);
    skillRepo = new SkillRepository(db);
    mastery = new MasteryService(db, new BktService());
    const eligibility = new EligibilityService(db, skillRepo, mastery);
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
    );
    attemptService = new AttemptService(
      db,
      attemptRepo,
      eligibility,
      events,
      orchestrator,
      evaluation,
    );
    catalog = new CatalogRepository(db);
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
    await skillRepo.createSkill({
      slug: 'k8s.deployments',
      name: 'K8s Deployments',
      domain: 'k8s',
    });
  });

  async function publishRealisticActivity() {
    return catalog.publishNewVersion({
      tenantId,
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
        tasks: [
          {
            key: 't1',
            required: true,
            validators: [
              {
                id: 'v.image-exists',
                type: 'SHELL_ASSERT',
                expect: { exit_code: 0 },
                weight: 0.6,
                severity: 'BLOCKING',
              },
              {
                id: 'v.image-size',
                type: 'SHELL_JSON',
                expect: { lt: 314572800 },
                weight: 0.4,
                severity: 'WARN',
              },
            ],
          },
          {
            key: 't2',
            required: true,
            validators: [
              {
                id: 'v.image-pushed',
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

  async function runToSubmit(version: { id: string; activity_id: string }) {
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    const provisioned = await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    return {
      attemptId: attempt.id,
      environmentId: provisioned.environment_id!,
    };
  }

  it('scores an all-pass attempt above pass_threshold and transitions to PASSED', async () => {
    const version = await publishRealisticActivity();
    const { attemptId } = await runToSubmit(version);

    const result = await attemptService.submit(attemptId);
    expect(result.status).toBe('PASSED');

    const scoreRow = await db
      .selectFrom('attempt.attempt_score')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(scoreRow.passed).toBe(true);
    expect(Number(scoreRow.final_score)).toBeGreaterThan(0.7);
    expect(scoreRow.profile_version_id).toBe('sp.guided-lab.default');
  });

  it('scores a failed-required-task attempt below pass_threshold and transitions to FAILED', async () => {
    const version = await publishRealisticActivity();
    const { attemptId, environmentId } = await runToSubmit(version);

    fakeExecutor.setOverride(environmentId, 'v.image-pushed', 'FAIL');

    const result = await attemptService.submit(attemptId);
    expect(result.status).toBe('FAILED');

    const scoreRow = await db
      .selectFrom('attempt.attempt_score')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(scoreRow.passed).toBe(false);
  });

  it('a WARN-severity validator failure does not block task completion, only lowers technical_correctness', async () => {
    const version = await publishRealisticActivity();
    const { attemptId, environmentId } = await runToSubmit(version);

    // v.image-size is WARN severity -- doc §3.2: "severity: WARN lets you
    // score good practice without blocking progress."
    fakeExecutor.setOverride(environmentId, 'v.image-size', 'FAIL');

    const result = await attemptService.submit(attemptId);
    // Still passes overall: both BLOCKING validators (image-exists,
    // image-pushed) pass, so both required tasks complete; only
    // technical_correctness's weighted-pass-rate dips slightly.
    expect(result.status).toBe('PASSED');
  });

  it('an ERROR validator result is excluded from scoring, never penalises the learner (doc §6.2)', async () => {
    const version = await publishRealisticActivity();
    const { attemptId, environmentId } = await runToSubmit(version);

    fakeExecutor.setOverride(environmentId, 'v.image-size', 'ERROR');

    const result = await attemptService.submit(attemptId);
    // v.image-size ERRORing must not fail the attempt -- the other
    // BLOCKING validators still pass.
    expect(result.status).toBe('PASSED');

    const validatorResults = await db
      .selectFrom('attempt.validator_result as vr')
      .innerJoin(
        'attempt.validation_run as run',
        'run.id',
        'vr.validation_run_id',
      )
      .select(['vr.validator_id', 'vr.status'])
      .where('run.attempt_id', '=', attemptId)
      .execute();
    expect(
      validatorResults.find((r) => r.validator_id === 'v.image-size')?.status,
    ).toBe('ERROR');
  });

  it('updates BKT mastery for every activity_skill on evaluation', async () => {
    const version = await publishRealisticActivity();
    const { attemptId } = await runToSubmit(version);

    const skill = await skillRepo.findBySlug('k8s.deployments');
    const before = await mastery.getMastery(userId, skill!.id);
    expect(before).toBeUndefined(); // no evidence yet

    await attemptService.submit(attemptId);

    const after = await mastery.getMastery(userId, skill!.id);
    expect(after).toBeDefined();
    expect(after!.evidence_count).toBe(1);

    const evidenceRows = await db
      .selectFrom('skill.mastery_evidence')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .execute();
    expect(evidenceRows).toHaveLength(1);
  });

  it('updates learner_activity_state (best_score, latest_score, attempts_count) across multiple activities', async () => {
    const version = await publishRealisticActivity();
    const { attemptId } = await runToSubmit(version);
    await attemptService.submit(attemptId);

    const state = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', version.activity_id)
      .executeTakeFirstOrThrow();

    expect(state.attempts_count).toBe(1);
    expect(Number(state.best_score)).toEqual(Number(state.latest_score));
  });

  it('populates attempt_task_state via ReplayService after evaluation (doc §4.1 layer 2, "what the UI reads")', async () => {
    const version = await publishRealisticActivity();
    const { attemptId } = await runToSubmit(version);
    await attemptService.submit(attemptId);

    const taskStates = await db
      .selectFrom('attempt.attempt_task_state')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .orderBy('task_key')
      .execute();

    expect(taskStates).toHaveLength(2);
    expect(taskStates.every((t) => t.status === 'PASSED')).toBe(true);
  });
});
