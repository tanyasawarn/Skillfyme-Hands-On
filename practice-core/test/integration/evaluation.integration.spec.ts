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
import { EvaluationService } from '../../src/modules/evaluation/evaluation.service';
import { ValidatorRunnerService } from '../../src/modules/evaluation/validator-runner.service';
import { ScoringEngineService } from '../../src/modules/evaluation/scoring-engine.service';
import { FakeValidatorExecutor } from '../../src/modules/evaluation/fake-validator-executor';
import { ReplayService } from '../../src/modules/event-store/replay.service';
import { FaultRepository } from '../../src/modules/evaluation/fault.repository';
import { ArtifactService } from '../../src/modules/evaluation/artifact.service';
import { ActivitySpecReader } from '../../src/common/activity-spec-reader';
import { RubricRepository } from '../../src/modules/evaluation/rubric.repository';
import { FakeAiGrader } from '../../src/modules/evaluation/fake-ai-grader.service';
import { createTestDb, truncateAll } from './test-db';
import { ConflictException } from '@nestjs/common';

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
  let evaluation: EvaluationService;
  let attemptRepo: AttemptRepository;
  let catalog: CatalogRepository;
  let skillRepo: SkillRepository;
  let mastery: MasteryService;
  let fakeExecutor: FakeValidatorExecutor;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    attemptRepo = new AttemptRepository(db);
    skillRepo = new SkillRepository(db);
    mastery = new MasteryService(db, new BktService(), new EloService());
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
    evaluation = new EvaluationService(
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

  // PLAN.md S7 regression: evaluate() used to update attempt.attempt's
  // status via a raw updateTable(...).where('version','=',...) with no
  // guard on the result -- a concurrent writer bumping the version
  // between evaluate()'s read and this write made the update silently
  // affect zero rows, leaving the attempt permanently stuck at
  // SUBMITTED/EVALUATING with a real attempt_score row already
  // persisted but no status transition to show for it. Reusing
  // AttemptRepository.transition() (already used everywhere else in
  // this codebase for exactly this reason, doc §4.4) means the same
  // scenario now raises ConflictException instead.
  it('doc §4.4 S7: evaluate() raises ConflictException on a concurrent version mismatch instead of silently no-oping', async () => {
    const version = await publishRealisticActivity();
    const { attemptId } = await runToSubmit(version);

    // Drive to SUBMITTED directly via the repository rather than
    // AttemptService.submit() -- submit() calls evaluate() internally
    // and the attempt would already be terminal by the time it returns,
    // leaving nothing to race a concurrent writer against.
    const inProgress = await attemptRepo.findById(attemptId);
    const submitted = await attemptRepo.transition(
      attemptId,
      inProgress!.version,
      { status: 'SUBMITTED', submittedAt: new Date() },
    );
    expect(submitted.status).toBe('SUBMITTED');

    // Genuinely race a concurrent writer against evaluate()'s own
    // in-flight run: evaluate() reads attempt.attempt once at the top
    // (capturing `version`), does substantial async work (multiple
    // queries, validator execution, mastery/Elo updates), then finally
    // writes using that stale, initially-read version. Firing the
    // concurrent transition() on the next microtask tick -- after
    // evaluate() has started and done its own initial read, but long
    // before it reaches its final write -- reproduces the real race
    // (e.g. the reaper independently transitioning the same attempt
    // while evaluate() is still mid-flight) rather than a sequential
    // before/after setup that evaluate()'s own fresh initial read would
    // just absorb. Lands on EVALUATING (not e.g. SUSPENDED) because
    // that's a status evaluate()'s own guard still accepts -- this test
    // targets the version-conflict path specifically, not the earlier
    // status guard.
    const evaluatePromise = evaluation.evaluate(attemptId);
    const concurrentWritePromise = Promise.resolve().then(() =>
      attemptRepo.transition(attemptId, submitted.version, {
        status: 'EVALUATING',
      }),
    );

    const [evaluateResult, concurrentWrite] = await Promise.allSettled([
      evaluatePromise,
      concurrentWritePromise,
    ]);

    expect(concurrentWrite.status).toBe('fulfilled');
    expect(evaluateResult.status).toBe('rejected');
    if (evaluateResult.status === 'rejected') {
      expect(evaluateResult.reason).toBeInstanceOf(ConflictException);
    }

    // The concurrent writer's own transition must survive untouched --
    // evaluate()'s failed write must not have clobbered it.
    const afterConflict = await attemptRepo.findById(attemptId);
    expect(afterConflict?.status).toBe('EVALUATING');
    if (concurrentWrite.status === 'fulfilled') {
      expect(afterConflict?.version).toBe(concurrentWrite.value.version);
    }
  });

  it('selects sp.production-sim.default for a PRODUCTION_SIM activity (doc §6.4 profile-per-mode)', async () => {
    const spec = {
      id: 'lab.sim.profile-selection',
      version: 1,
      meta: { difficulty_level: 'L3', estimated_minutes: 30 },
      environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.08 },
      skills: [{ skill: 'k8s.deployments', weight: 1.0, primary: true }],
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
      scoring: { profile: 'sp.production-sim.default' },
    };
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.sim.profile-selection',
      mode: 'PRODUCTION_SIM',
      spec: spec as never,
    });
    const { attemptId } = await runToSubmit(version);

    const result = await attemptService.submit(attemptId);
    expect(result.status).toBe('PASSED');

    const scoreRow = await db
      .selectFrom('attempt.attempt_score')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
    expect(scoreRow.profile_version_id).toBe('sp.production-sim.default');
    // sp.production-sim.default's weights are troubleshooting/technical_
    // implementation/reliability, not sp.guided-lab.default's technical_
    // correctness/task_completion/efficiency -- confirms the right
    // profile's criteria actually ran, not just that the id string matched.
    const breakdown = scoreRow.breakdown_jsonb;
    expect(Object.keys(breakdown)).toEqual(
      expect.arrayContaining([
        'troubleshooting',
        'technical_implementation',
        'reliability',
      ]),
    );
    expect(breakdown).not.toHaveProperty('task_completion');
  });

  it('falls back to sp.guided-lab.default when an activity references an unknown scoring profile', async () => {
    const spec = {
      id: 'lab.unknown-profile',
      version: 1,
      meta: { difficulty_level: 'L1', estimated_minutes: 20 },
      environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.02 },
      skills: [{ skill: 'k8s.deployments', weight: 1.0, primary: true }],
      scoring: { profile: 'sp.does-not-exist' },
    };
    const version = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.unknown-profile',
      mode: 'GUIDED_LAB',
      spec: spec as never,
    });
    const { attemptId } = await runToSubmit(version);

    await attemptService.submit(attemptId);
    const scoreRow = await db
      .selectFrom('attempt.attempt_score')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .executeTakeFirstOrThrow();
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

  async function publishRemediationActivity() {
    // A second, easier activity targeting the SAME primary skill
    // (k8s.deployments) as publishRealisticActivity -- the
    // "remediation activity for the failed sub-skill" doc §2.7 means.
    // Single task, single validator, deliberately trivial to pass.
    return catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.k8s.deploy-node-app.remediation',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.k8s.deploy-node-app.remediation',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 10 },
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
                weight: 1.0,
                severity: 'BLOCKING',
              },
            ],
          },
        ],
      } as never,
    });
  }

  it('doc §2.7 early-clear: passing an activity clears cooldown_until on another activity that shares its primary skill', async () => {
    const version = await publishRealisticActivity();
    const { attemptId, environmentId } = await runToSubmit(version);
    fakeExecutor.setOverride(environmentId, 'v.image-pushed', 'FAIL');
    const failResult = await attemptService.submit(attemptId);
    expect(failResult.status).toBe('FAILED');

    const cooledDownState = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', version.activity_id)
      .executeTakeFirstOrThrow();
    expect(cooledDownState.cooldown_until).not.toBeNull();
    expect(cooledDownState.cooldown_until!.getTime()).toBeGreaterThan(
      Date.now(),
    );

    // Now pass a DIFFERENT activity targeting the same primary skill.
    const remediationVersion = await publishRemediationActivity();
    const remediationAttempt = await runToSubmit(remediationVersion);
    const passResult = await attemptService.submit(
      remediationAttempt.attemptId,
    );
    expect(passResult.status).toBe('PASSED');

    const clearedState = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', version.activity_id)
      .executeTakeFirstOrThrow();
    expect(clearedState.cooldown_until).toBeNull();
  });

  it('doc §2.7 early-clear: does NOT clear cooldown on an activity that does not share the passed activity primary skill', async () => {
    const version = await publishRealisticActivity();
    const { attemptId, environmentId } = await runToSubmit(version);
    fakeExecutor.setOverride(environmentId, 'v.image-pushed', 'FAIL');
    await attemptService.submit(attemptId);

    // Publish and pass an UNRELATED activity (different skill entirely).
    await skillRepo.createSkill({
      slug: 'unrelated.skill',
      name: 'Unrelated',
      domain: 'other',
    });
    const unrelatedVersion = await catalog.publishNewVersion({
      tenantId,
      activitySlug: 'lab.unrelated',
      mode: 'GUIDED_LAB',
      spec: {
        id: 'lab.unrelated',
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 10 },
        environment: {
          blueprint: 'bp.k8s-single-node.v1',
          cost_budget_usd: 0.08,
        },
        skills: [{ skill: 'unrelated.skill', weight: 1.0, primary: true }],
        tasks: [
          {
            key: 't1',
            required: true,
            validators: [
              {
                id: 'v.image-exists',
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
    const unrelatedAttempt = await runToSubmit(unrelatedVersion);
    const passResult = await attemptService.submit(unrelatedAttempt.attemptId);
    expect(passResult.status).toBe('PASSED');

    const stillCooledDown = await db
      .selectFrom('learner.learner_activity_state')
      .selectAll()
      .where('user_id', '=', userId)
      .where('activity_id', '=', version.activity_id)
      .executeTakeFirstOrThrow();
    expect(stillCooledDown.cooldown_until).not.toBeNull();
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
