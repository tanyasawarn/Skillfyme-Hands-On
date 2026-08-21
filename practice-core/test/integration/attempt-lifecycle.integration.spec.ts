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

describe('AttemptService (integration, real Postgres) — doc §4.1 state machine', () => {
  let db: Kysely<Database>;
  let attemptService: AttemptService;
  let eligibility: EligibilityService;
  let catalog: CatalogRepository;
  let skillRepo: SkillRepository;
  let tenantId: string;
  let userId: string;

  beforeAll(() => {
    db = createTestDb();
    const attemptRepo = new AttemptRepository(db);
    skillRepo = new SkillRepository(db);
    const mastery = new MasteryService(db, new BktService());
    eligibility = new EligibilityService(db, skillRepo, mastery);
    const events = new EventStoreRepository(db);
    const orchestrator = new FakeOrchestratorClient(5);
    const validatorRunner = new ValidatorRunnerService(
      db,
      events,
      new FakeValidatorExecutor(),
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
      slug: 'k8s.core',
      name: 'K8s Core',
      domain: 'k8s',
    });
  });

  async function publishActivity(slug: string) {
    return catalog.publishNewVersion({
      tenantId,
      activitySlug: slug,
      mode: 'GUIDED_LAB',
      spec: {
        id: slug,
        version: 1,
        meta: { difficulty_level: 'L1', estimated_minutes: 20 },
        environment: { blueprint: 'bp.test.v1', cost_budget_usd: 0.05 },
        skills: [{ skill: 'k8s.core', weight: 1.0, primary: true }],
      },
    });
  }

  it('walks the full CREATED -> READY -> IN_PROGRESS -> SUBMITTED path with correct events', async () => {
    const version = await publishActivity('lab.walkthrough');

    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    expect(attempt.status).toBe('CREATED');

    const provisioned = await attemptService.provision(attempt.id);
    expect(provisioned.status).toBe('READY');
    expect(provisioned.environment_id).toMatch(/^fake-env-/);

    const started = await attemptService.markStarted(attempt.id);
    expect(started.status).toBe('IN_PROGRESS');
    expect(started.started_at).not.toBeNull();

    // submit() now runs evaluation synchronously (FakeOrchestratorClient
    // has no real environment to tear down, so there's no ENV_DESTROYED
    // event to wait on -- see the doc comment on AttemptService.submit()).
    // The returned attempt reflects the *post-evaluation* state, not the
    // transient SUBMITTED status.
    const submitted = await attemptService.submit(attempt.id);
    expect(['PASSED', 'FAILED']).toContain(submitted.status);
    expect(submitted.submitted_at).not.toBeNull();
    expect(submitted.completed_at).not.toBeNull();

    const eventLog = await db
      .selectFrom('attempt.attempt_events')
      .select(['type', 'actor'])
      .where('attempt_id', '=', attempt.id)
      .orderBy('seq')
      .execute();
    // lab.walkthrough was published with no tasks (see publishActivity
    // helper), so evaluation runs zero validators -- only the
    // request/complete bookends appear, not per-task VALIDATOR_RESULT/
    // TASK_PASSED events.
    expect(eventLog.map((e) => e.type)).toEqual([
      'ATTEMPT_CREATED',
      'ENV_REQUESTED',
      'ENV_READY',
      'ATTEMPT_STARTED',
      'SUBMITTED',
      'VALIDATION_REQUESTED',
      'EVALUATED',
    ]);
  });

  it('is idempotent on duplicate create with the same idempotency key (doc §4.4)', async () => {
    const version = await publishActivity('lab.idempotent');

    const first = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'key-1',
    });
    const second = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
      idempotencyKey: 'key-1',
    });

    expect(second.id).toBe(first.id);

    const count = await db
      .selectFrom('attempt.attempt')
      .select((eb) => eb.fn.countAll<number>().as('c'))
      .where('user_id', '=', userId)
      .executeTakeFirstOrThrow();
    expect(Number(count.c)).toBe(1);

    const createdEvents = await db
      .selectFrom('attempt.attempt_events')
      .selectAll()
      .where('attempt_id', '=', first.id)
      .where('type', '=', 'ATTEMPT_CREATED')
      .execute();
    expect(createdEvents).toHaveLength(1); // not double-appended on the duplicate call
  });

  it('rejects a second attempt while the first still holds the concurrent-environment slot (doc §4.4), across every active status', async () => {
    const version = await publishActivity('lab.quota');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });

    // CREATED does not yet hold the environment slot -- provisioning does.
    await expect(
      attemptService.createAttempt({
        tenantId,
        userId,
        activityVersionId: version.id,
        idempotencyKey: 'other',
      }),
    ).resolves.toBeDefined();

    // Once provisioned (READY) through IN_PROGRESS, the slot is held --
    // this is the concurrency bug originally caught by manual boot-testing.
    await attemptService.provision(attempt.id);
    let result = await eligibility.check(userId, version.id);
    expect(result.eligible).toBe(false);
    expect(
      result.reasons.some((r) => r.includes('concurrent-environment')),
    ).toBe(true);

    await attemptService.markStarted(attempt.id);
    result = await eligibility.check(userId, version.id);
    expect(result.eligible).toBe(false);

    // submit() evaluates synchronously in Phase 1 (see the doc comment on
    // AttemptService.submit()), landing the attempt directly on
    // PASSED/FAILED -- neither is in the eligibility gate's active-status
    // list (doc §4.1's SUBMITTED/EVALUATING window only applies to the
    // real-orchestrator path, where teardown happens after grading, not
    // before it). The slot is correctly freed once evaluation completes.
    await attemptService.submit(attempt.id);
    result = await eligibility.check(userId, version.id);
    expect(result.eligible).toBe(true);
  });

  it('rejects createAttempt outright when eligibility fails (BadRequestException with reasons)', async () => {
    const version = await publishActivity('lab.reject-test');
    await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(
      (
        await attemptService.createAttempt({
          tenantId,
          userId,
          activityVersionId: version.id,
        })
      ).id,
    );

    await expect(
      attemptService.createAttempt({
        tenantId,
        userId,
        activityVersionId: version.id,
        idempotencyKey: 'blocked',
      }),
    ).rejects.toThrow(/not eligible/);
  });

  it('handleEnvironmentDestroyed transitions SUBMITTED -> EVALUATING and is idempotent', async () => {
    const version = await publishActivity('lab.env-destroyed');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    await attemptService.provision(attempt.id);
    await attemptService.markStarted(attempt.id);
    await attemptService.submit(attempt.id);

    await attemptService.handleEnvironmentDestroyed(attempt.id, 'submit');
    const after = await db
      .selectFrom('attempt.attempt')
      .selectAll()
      .where('id', '=', attempt.id)
      .executeTakeFirstOrThrow();
    expect(after.status).toBe('EVALUATING');
    expect(after.environment_id).toBeNull();

    // Idempotent: calling again on an attempt no longer in an active state must not throw.
    await expect(
      attemptService.handleEnvironmentDestroyed(attempt.id, 'submit'),
    ).resolves.not.toThrow();
  });

  it('throws ConflictException on a stale-version transition (optimistic concurrency, doc §4.4)', async () => {
    const version = await publishActivity('lab.concurrency');
    const attempt = await attemptService.createAttempt({
      tenantId,
      userId,
      activityVersionId: version.id,
    });
    const attemptRepo = new AttemptRepository(db);

    // Simulate a concurrent writer bumping the version first.
    await attemptRepo.transition(attempt.id, attempt.version, {
      status: 'PROVISIONING',
    });

    // Now attempt a transition using the stale (pre-bump) version number.
    await expect(
      attemptRepo.transition(attempt.id, attempt.version, { status: 'READY' }),
    ).rejects.toThrow(/modified concurrently/);
  });
});
